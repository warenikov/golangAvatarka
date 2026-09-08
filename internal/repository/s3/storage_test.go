package s3

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcminio "github.com/testcontainers/testcontainers-go/modules/minio"

	"go-avatar-service/internal/config"
	"go-avatar-service/internal/domain"
)

var testCfg config.S3

func TestMain(m *testing.M) {
	flag.Parse()

	if testing.Short() {
		os.Exit(0)
	}

	ctx := context.Background()

	container, err := tcminio.Run(ctx, "minio/minio:RELEASE.2024-12-18T13-15-44Z",
		tcminio.WithUsername("minioadmin"),
		tcminio.WithPassword("minioadmin"),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "не удалось поднять minio: %v\n", err)
		os.Exit(1)
	}

	code := runTests(ctx, m, container)

	if err = testcontainers.TerminateContainer(container); err != nil {
		fmt.Fprintf(os.Stderr, "не удалось остановить контейнер: %v\n", err)
	}

	os.Exit(code)
}

func runTests(ctx context.Context, m *testing.M, container *tcminio.MinioContainer) int {
	endpoint, err := container.ConnectionString(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "адрес minio: %v\n", err)

		return 1
	}

	testCfg = config.S3{
		Endpoint:  endpoint,
		AccessKey: container.Username,
		SecretKey: container.Password,
		Bucket:    "avatars",
		Region:    "us-east-1",
		UseSSL:    false,
	}

	return m.Run()
}

func putString(ctx context.Context, storage *Storage, key, body string) error {
	return storage.Put(ctx, key, strings.NewReader(body), int64(len(body)), "image/jpeg")
}

func newStorage(t *testing.T) *Storage {
	t.Helper()

	cfg := testCfg
	cfg.Bucket = "test-" + strings.ToLower(uuid.NewString())

	storage, err := NewStorage(t.Context(), cfg)
	require.NoError(t, err)

	return storage
}

func TestNewStorageCreatesBucket(t *testing.T) {
	storage := newStorage(t)

	exists, err := storage.client.BucketExists(t.Context(), storage.bucket)
	require.NoError(t, err)
	require.True(t, exists, "бакет должен создаваться при старте")
}

func TestNewStorageIsIdempotent(t *testing.T) {
	cfg := testCfg
	cfg.Bucket = "test-" + strings.ToLower(uuid.NewString())

	_, err := NewStorage(t.Context(), cfg)
	require.NoError(t, err)

	_, err = NewStorage(t.Context(), cfg)
	require.NoError(t, err, "повторный запуск не должен падать на существующем бакете")
}

func TestPutAndGet(t *testing.T) {
	storage := newStorage(t)
	ctx := t.Context()

	userID := "user-1"
	avatarID := uuid.New()
	key := domain.OriginalObjectKey(userID, avatarID)
	payload := []byte("притворимся, что это JPEG")

	require.NoError(t, storage.Put(ctx, key, bytes.NewReader(payload), int64(len(payload)), "image/jpeg"))

	obj, err := storage.Get(ctx, key)
	require.NoError(t, err)
	defer func() { require.NoError(t, obj.Body.Close()) }()

	require.Equal(t, "image/jpeg", obj.ContentType)
	require.Equal(t, int64(len(payload)), obj.Size)
	require.NotEmpty(t, obj.ETag)

	got, err := io.ReadAll(obj.Body)
	require.NoError(t, err)
	require.Equal(t, payload, got)
}

func TestGetMissingObject(t *testing.T) {
	storage := newStorage(t)

	_, err := storage.Get(t.Context(), domain.OriginalObjectKey("user-1", uuid.New()))
	require.ErrorIs(t, err, domain.ErrObjectNotFound)
}

func TestPutOverwritesSameKey(t *testing.T) {
	storage := newStorage(t)
	ctx := t.Context()

	key := domain.ThumbnailObjectKey("user-1", uuid.New(), domain.ThumbnailSmall)

	require.NoError(t, putString(ctx, storage, key, "первый"))
	require.NoError(t, putString(ctx, storage, key, "второй"))

	obj, err := storage.Get(ctx, key)
	require.NoError(t, err)
	defer func() { _ = obj.Body.Close() }()

	got, err := io.ReadAll(obj.Body)
	require.NoError(t, err)
	require.Equal(t, "второй", string(got), "повторная запись по тому же ключу перезаписывает объект")
}

func TestDeleteIsIdempotent(t *testing.T) {
	storage := newStorage(t)
	ctx := t.Context()

	key := domain.OriginalObjectKey("user-1", uuid.New())
	require.NoError(t, putString(ctx, storage, key, "данные"))

	require.NoError(t, storage.Delete(ctx, key))
	require.NoError(t, storage.Delete(ctx, key), "удаление отсутствующего объекта — не ошибка")

	_, err := storage.Get(ctx, key)
	require.ErrorIs(t, err, domain.ErrObjectNotFound)
}

func TestDeleteMany(t *testing.T) {
	storage := newStorage(t)
	ctx := t.Context()

	userID := "user-1"
	avatarID := uuid.New()

	keys := []string{domain.OriginalObjectKey(userID, avatarID)}
	for _, key := range domain.ThumbnailKeys(userID, avatarID) {
		keys = append(keys, key)
	}

	for _, key := range keys {
		require.NoError(t, putString(ctx, storage, key, "x"))
	}

	require.NoError(t, storage.DeleteMany(ctx, keys))

	for _, key := range keys {
		_, err := storage.Get(ctx, key)
		require.ErrorIsf(t, err, domain.ErrObjectNotFound, "ключ %s должен быть удалён", key)
	}

	require.NoError(t, storage.DeleteMany(ctx, nil), "пустой список — не ошибка")
}

func TestRejectsUnsafeKeys(t *testing.T) {
	storage := newStorage(t)
	ctx := t.Context()

	unsafe := []string{"", "/avatars/x", "avatars/../../etc/passwd", "avatars//x"}

	for _, key := range unsafe {
		t.Run(url.PathEscape(key), func(t *testing.T) {
			require.Error(t, storage.Put(ctx, key, strings.NewReader("x"), 1, "image/jpeg"))

			_, err := storage.Get(ctx, key)
			require.Error(t, err)

			require.Error(t, storage.Delete(ctx, key))
			require.Error(t, storage.DeleteMany(ctx, []string{key}))
		})
	}
}

func TestHealthChecker(t *testing.T) {
	storage := newStorage(t)

	checker := NewHealthChecker(storage)
	require.Equal(t, "s3", checker.Name())

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	require.NoError(t, checker.Check(ctx))
}
