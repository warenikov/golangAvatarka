package postgres

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"go-avatar-service/internal/config"
	"go-avatar-service/internal/domain"
)

var testPool *pgxpool.Pool

func TestMain(m *testing.M) {
	flag.Parse()

	if testing.Short() {
		os.Exit(0)
	}

	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("avatars"),
		tcpostgres.WithUsername("avatars"),
		tcpostgres.WithPassword("avatars"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(90*time.Second),
		),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "не удалось поднять postgres: %v\n", err)
		os.Exit(1)
	}

	code := runTests(ctx, m, container)

	if err = testcontainers.TerminateContainer(container); err != nil {
		fmt.Fprintf(os.Stderr, "не удалось остановить контейнер: %v\n", err)
	}

	os.Exit(code)
}

func runTests(ctx context.Context, m *testing.M, container *tcpostgres.PostgresContainer) int {
	cfg, err := containerConfig(ctx, container)
	if err != nil {
		fmt.Fprintf(os.Stderr, "конфиг контейнера: %v\n", err)

		return 1
	}

	testPool, err = NewPool(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "пул соединений: %v\n", err)

		return 1
	}
	defer testPool.Close()

	if err = Migrate(ctx, testPool); err != nil {
		fmt.Fprintf(os.Stderr, "миграции: %v\n", err)

		return 1
	}

	return m.Run()
}

func containerConfig(ctx context.Context, container *tcpostgres.PostgresContainer) (config.DB, error) {
	host, err := container.Host(ctx)
	if err != nil {
		return config.DB{}, fmt.Errorf("host: %w", err)
	}

	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		return config.DB{}, fmt.Errorf("port: %w", err)
	}

	num, err := strconv.Atoi(port.Port())
	if err != nil {
		return config.DB{}, fmt.Errorf("port number: %w", err)
	}

	return config.DB{
		Host: host, Port: num,
		User: "avatars", Password: "avatars", Name: "avatars",
		SSLMode: "disable", MaxConns: 4, MinConns: 1,
	}, nil
}

func newRepo(t *testing.T) *AvatarRepository {
	t.Helper()

	_, err := testPool.Exec(context.Background(), "TRUNCATE avatars")
	require.NoError(t, err)

	return NewAvatarRepository(testPool)
}

func newAvatar(userID string) *domain.Avatar {
	id := uuid.New()

	return &domain.Avatar{
		ID:               id,
		UserID:           userID,
		FileName:         "avatar.jpg",
		MimeType:         "image/jpeg",
		SizeBytes:        1024,
		S3Key:            fmt.Sprintf("avatars/%s/%s/original", userID, id),
		ThumbnailS3Keys:  map[string]string{},
		UploadStatus:     domain.UploadStatusUploaded,
		ProcessingStatus: domain.ProcessingStatusPending,
	}
}

func TestCreateAndGetByID(t *testing.T) {
	repo := newRepo(t)
	ctx := t.Context()

	want := newAvatar("user-1")
	require.NoError(t, repo.Create(ctx, want))
	require.False(t, want.CreatedAt.IsZero(), "Create должен заполнить created_at")

	got, err := repo.GetByID(ctx, want.ID)
	require.NoError(t, err)
	require.Equal(t, want.ID, got.ID)
	require.Equal(t, want.UserID, got.UserID)
	require.Equal(t, want.S3Key, got.S3Key)
	require.Equal(t, domain.ProcessingStatusPending, got.ProcessingStatus)
	require.Empty(t, got.ThumbnailS3Keys)
	require.Nil(t, got.Width, "ширина неизвестна до обработки")
	require.Nil(t, got.DeletedAt)
}

func TestGetByIDNotFound(t *testing.T) {
	repo := newRepo(t)

	_, err := repo.GetByID(t.Context(), uuid.New())
	require.ErrorIs(t, err, domain.ErrAvatarNotFound)
}

func TestGetCurrentReturnsNewest(t *testing.T) {
	repo := newRepo(t)
	ctx := t.Context()

	for range 3 {
		require.NoError(t, repo.Create(ctx, newAvatar("user-1")))
		time.Sleep(time.Millisecond)
	}
	newest := newAvatar("user-1")
	require.NoError(t, repo.Create(ctx, newest))

	got, err := repo.GetCurrentByUserID(ctx, "user-1")
	require.NoError(t, err)
	require.Equal(t, newest.ID, got.ID)
}

func TestListByUserIDIsolatesUsers(t *testing.T) {
	repo := newRepo(t)
	ctx := t.Context()

	require.NoError(t, repo.Create(ctx, newAvatar("user-1")))
	require.NoError(t, repo.Create(ctx, newAvatar("user-1")))
	require.NoError(t, repo.Create(ctx, newAvatar("user-2")))

	got, err := repo.ListByUserID(ctx, "user-1")
	require.NoError(t, err)
	require.Len(t, got, 2)

	empty, err := repo.ListByUserID(ctx, "user-404")
	require.NoError(t, err)
	require.Empty(t, empty, "у неизвестного пользователя пустой список, а не ошибка")
}

func TestSoftDeleteHidesAvatar(t *testing.T) {
	repo := newRepo(t)
	ctx := t.Context()

	a := newAvatar("user-1")
	require.NoError(t, repo.Create(ctx, a))

	_, err := repo.UpdateProcessingResult(ctx, a.ID,
		map[string]string{domain.ThumbnailSmall: "thumb-100"}, 800, 600)
	require.NoError(t, err)

	keys, err := repo.SoftDelete(ctx, a.ID, "user-1")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{a.S3Key, "thumb-100"}, keys)

	_, err = repo.GetByID(ctx, a.ID)
	require.ErrorIs(t, err, domain.ErrAvatarNotFound, "удалённая аватарка не должна читаться")
}

func TestSoftDeleteForeignAvatarIsForbidden(t *testing.T) {
	repo := newRepo(t)
	ctx := t.Context()

	a := newAvatar("user-1")
	require.NoError(t, repo.Create(ctx, a))

	_, err := repo.SoftDelete(ctx, a.ID, "user-2")
	require.ErrorIs(t, err, domain.ErrForbidden)

	_, err = repo.GetByID(ctx, a.ID)
	require.NoError(t, err, "чужая попытка удаления не должна ничего менять")
}

func TestSoftDeleteTwiceIsNotFound(t *testing.T) {
	repo := newRepo(t)
	ctx := t.Context()

	a := newAvatar("user-1")
	require.NoError(t, repo.Create(ctx, a))

	_, err := repo.SoftDelete(ctx, a.ID, "user-1")
	require.NoError(t, err)

	_, err = repo.SoftDelete(ctx, a.ID, "user-1")
	require.ErrorIs(t, err, domain.ErrAvatarNotFound)
}

func TestUpdateProcessingResultIsIdempotent(t *testing.T) {
	repo := newRepo(t)
	ctx := t.Context()

	a := newAvatar("user-1")
	require.NoError(t, repo.Create(ctx, a))

	thumbs := map[string]string{
		domain.ThumbnailSmall: "avatars/user-1/100x100.jpg",
		domain.ThumbnailLarge: "avatars/user-1/300x300.jpg",
	}

	updated, err := repo.UpdateProcessingResult(ctx, a.ID, thumbs, 1920, 1080)
	require.NoError(t, err)
	require.True(t, updated, "первая обработка должна изменить строку")

	updated, err = repo.UpdateProcessingResult(ctx, a.ID, map[string]string{"100x100": "другой"}, 1, 1)
	require.NoError(t, err)
	require.False(t, updated, "повторная обработка не должна ничего менять")

	got, err := repo.GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, domain.ProcessingStatusCompleted, got.ProcessingStatus)
	require.Equal(t, thumbs, got.ThumbnailS3Keys, "результат первой обработки должен сохраниться")
	require.Equal(t, 1920, *got.Width)
	require.Equal(t, 1080, *got.Height)
}

func TestListPendingOlderThan(t *testing.T) {
	repo := newRepo(t)
	ctx := t.Context()

	fresh := newAvatar("user-1")
	require.NoError(t, repo.Create(ctx, fresh))

	stuck := newAvatar("user-2")
	require.NoError(t, repo.Create(ctx, stuck))
	_, err := testPool.Exec(ctx,
		"UPDATE avatars SET created_at = NOW() - INTERVAL '10 minutes' WHERE id = $1", stuck.ID)
	require.NoError(t, err)

	done := newAvatar("user-3")
	require.NoError(t, repo.Create(ctx, done))
	_, err = testPool.Exec(ctx,
		"UPDATE avatars SET created_at = NOW() - INTERVAL '10 minutes', processing_status = 'completed' WHERE id = $1", done.ID)
	require.NoError(t, err)

	got, err := repo.ListPendingOlderThan(ctx, 5*time.Minute, 100)
	require.NoError(t, err)
	require.Len(t, got, 1, "только застрявший pending")
	require.Equal(t, stuck.ID, got[0].ID)
}

func TestMigrationsRollBack(t *testing.T) {
	ctx := t.Context()

	require.NoError(t, MigrateDown(ctx, testPool))

	var exists bool
	err := testPool.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'avatars')").Scan(&exists)
	require.NoError(t, err)
	require.False(t, exists, "после отката таблица avatars должна исчезнуть")

	require.NoError(t, Migrate(ctx, testPool), "миграция должна накатываться повторно")
}
