package worker

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"go-avatar-service/internal/domain"
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
	publisher.EXPECT().PublishUpload(mock.Anything, mock.Anything).Return(assert.AnError).Twice()

	r := NewReconciler(repo, publisher, time.Minute, reconcileAge, discardLogger())
	r.reconcile(t.Context())
}

func TestReconcilerRunStopsOnContextCancel(t *testing.T) {
	repo := NewMockRepository(t)
	repo.EXPECT().ListPendingOlderThan(mock.Anything, reconcileAge, reconcileBatch).
		Return(nil, nil).Maybe()

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
