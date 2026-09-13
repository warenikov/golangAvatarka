package worker

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/observability"
)

const reconcileAge = 5 * time.Minute

func stuckAvatars(n int) []domain.Avatar {
	avatars := make([]domain.Avatar, 0, n)
	for range n {
		id := uuid.New()
		avatars = append(avatars, domain.Avatar{
			ID: id, UserID: testUserID,
			S3Key:            domain.OriginalObjectKey(testUserID, id),
			ProcessingStatus: domain.ProcessingStatusPending,
		})
	}

	return avatars
}

func TestReconcileRepublishesStuckAvatars(t *testing.T) {
	repo := NewMockRepository(t)
	publisher := NewMockEventPublisher(t)

	stuck := stuckAvatars(3)
	repo.EXPECT().ListPendingOlderThan(mock.Anything, reconcileAge, reconcileBatch).
		Return(stuck, nil).Once()
	repo.EXPECT().CountPendingOlderThan(mock.Anything, reconcileAge).Return(len(stuck), nil).Once()

	var mu sync.Mutex
	published := make([]domain.AvatarUploadEvent, 0, len(stuck))
	publisher.EXPECT().PublishUpload(mock.Anything, mock.Anything).
		Run(func(_ context.Context, e domain.AvatarUploadEvent) {
			mu.Lock()
			defer mu.Unlock()
			published = append(published, e)
		}).
		Return(nil).Times(len(stuck))

	r := NewReconciler(repo, publisher, time.Minute, reconcileAge, discardLogger())
	r.reconcile(t.Context())

	require.Len(t, published, len(stuck))
	for i, event := range published {
		assert.Equal(t, stuck[i].ID.String(), event.AvatarID)
		assert.Equal(t, stuck[i].UserID, event.UserID)
		assert.Equal(t, stuck[i].S3Key, event.S3Key)
	}
}

func TestReconcileWithNothingStuck(t *testing.T) {
	repo := NewMockRepository(t)

	repo.EXPECT().ListPendingOlderThan(mock.Anything, reconcileAge, reconcileBatch).
		Return(nil, nil).Once()
	repo.EXPECT().CountPendingOlderThan(mock.Anything, reconcileAge).Return(0, nil).Once()

	r := NewReconciler(repo, NewMockEventPublisher(t), time.Minute, reconcileAge, discardLogger())
	r.reconcile(t.Context())
}

func TestReconcileSurvivesRepositoryError(t *testing.T) {
	repo := NewMockRepository(t)

	repo.EXPECT().ListPendingOlderThan(mock.Anything, reconcileAge, reconcileBatch).
		Return(nil, assert.AnError).Once()

	r := NewReconciler(repo, NewMockEventPublisher(t), time.Minute, reconcileAge, discardLogger())
	r.reconcile(t.Context())
}

// Неудачная публикация одной аватарки не должна прерывать обход остальных:
// следующий тик подберёт то, что не уехало.
func TestReconcileContinuesAfterPublishError(t *testing.T) {
	repo := NewMockRepository(t)
	publisher := NewMockEventPublisher(t)

	stuck := stuckAvatars(2)
	repo.EXPECT().ListPendingOlderThan(mock.Anything, reconcileAge, reconcileBatch).
		Return(stuck, nil).Once()
	repo.EXPECT().CountPendingOlderThan(mock.Anything, reconcileAge).Return(len(stuck), nil).Once()
	publisher.EXPECT().PublishUpload(mock.Anything, mock.Anything).Return(assert.AnError).Twice()

	r := NewReconciler(repo, publisher, time.Minute, reconcileAge, discardLogger())
	r.reconcile(t.Context())
}

func TestReconcilerRunStopsOnContextCancel(t *testing.T) {
	repo := NewMockRepository(t)
	repo.EXPECT().ListPendingOlderThan(mock.Anything, reconcileAge, reconcileBatch).
		Return(nil, nil).Maybe()
	repo.EXPECT().CountPendingOlderThan(mock.Anything, reconcileAge).Return(0, nil).Maybe()

	ctx, cancel := context.WithCancel(t.Context())

	r := NewReconciler(repo, NewMockEventPublisher(t), 10*time.Millisecond, reconcileAge, discardLogger())

	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("реконсилятор не остановился по отмене контекста")
	}
}

// Гейдж отставания не должен упираться в размер пачки: и десять застрявших
// аватарок, и десять тысяч выглядели бы одинаково, а по аннотации алерта
// оператор увидел бы неверное число.
func TestBacklogGaugeIsNotCappedByBatchSize(t *testing.T) {
	repo := NewMockRepository(t)
	publisher := NewMockEventPublisher(t)

	// Переиздать за раз можно только пачку, а застряло кратно больше.
	batch := stuckAvatars(reconcileBatch)
	repo.EXPECT().ListPendingOlderThan(mock.Anything, reconcileAge, reconcileBatch).
		Return(batch, nil).Once()
	repo.EXPECT().CountPendingOlderThan(mock.Anything, reconcileAge).Return(5000, nil).Once()
	publisher.EXPECT().PublishUpload(mock.Anything, mock.Anything).Return(nil).Times(len(batch))

	reg := prometheus.NewRegistry()
	metrics, err := observability.NewBusiness(reg)
	require.NoError(t, err)

	r := NewReconciler(repo, publisher, time.Minute, reconcileAge, discardLogger()).WithMetrics(metrics)
	r.reconcile(t.Context())

	assert.InDelta(t, 5000.0, backlogValue(t, reg), 0.001,
		"в метрике должно быть реальное отставание, а не размер выборки")
}

// Сбой подсчёта не должен ронять переиздание: событие важнее метрики.
func TestBacklogCountFailureDoesNotStopRepublish(t *testing.T) {
	repo := NewMockRepository(t)
	publisher := NewMockEventPublisher(t)

	stuck := stuckAvatars(2)
	repo.EXPECT().ListPendingOlderThan(mock.Anything, reconcileAge, reconcileBatch).
		Return(stuck, nil).Once()
	repo.EXPECT().CountPendingOlderThan(mock.Anything, reconcileAge).
		Return(0, assert.AnError).Once()
	publisher.EXPECT().PublishUpload(mock.Anything, mock.Anything).Return(nil).Twice()

	r := NewReconciler(repo, publisher, time.Minute, reconcileAge, discardLogger())

	assert.NotPanics(t, func() { r.reconcile(t.Context()) })
}

func backlogValue(t *testing.T, reg *prometheus.Registry) float64 {
	t.Helper()

	families, err := reg.Gather()
	require.NoError(t, err)

	for _, f := range families {
		if f.GetName() == "avatar_processing_backlog" {
			return f.GetMetric()[0].GetGauge().GetValue()
		}
	}

	return -1
}
