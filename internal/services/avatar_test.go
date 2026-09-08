package services_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/services"
)

const testUserID = "user-1"

type deps struct {
	repo      *MockAvatarRepository
	storage   *MockObjectStorage
	publisher *MockEventPublisher
}

func newService(t *testing.T) (*services.AvatarService, deps) {
	t.Helper()

	d := deps{
		repo:      NewMockAvatarRepository(t),
		storage:   NewMockObjectStorage(t),
		publisher: NewMockEventPublisher(t),
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	return services.NewAvatarService(d.repo, d.storage, d.publisher, log), d
}

func object(content string) *domain.Object {
	return &domain.Object{
		Body:        io.NopCloser(strings.NewReader(content)),
		ContentType: "image/png",
		Size:        int64(len(content)),
		ETag:        "etag",
	}
}

func uploadInput() services.UploadInput {
	return services.UploadInput{
		UserID:   testUserID,
		FileName: "avatar.png",
		MimeType: "image/png",
		Size:     1024,
		Body:     strings.NewReader("картинка"),
	}
}

func TestUploadStoresFileMetadataAndEvent(t *testing.T) {
	svc, d := newService(t)

	var storedKey string
	d.storage.EXPECT().
		Put(mock.Anything, mock.Anything, mock.Anything, int64(1024), "image/png").
		Run(func(_ context.Context, key string, _ io.Reader, _ int64, _ string) { storedKey = key }).
		Return(nil).Once()

	var created *domain.Avatar
	d.repo.EXPECT().Create(mock.Anything, mock.Anything).
		Run(func(_ context.Context, a *domain.Avatar) { created = a }).
		Return(nil).Once()

	var published domain.AvatarUploadEvent
	d.publisher.EXPECT().PublishUpload(mock.Anything, mock.Anything).
		Run(func(_ context.Context, e domain.AvatarUploadEvent) { published = e }).
		Return(nil).Once()

	avatar, err := svc.Upload(t.Context(), uploadInput())
	require.NoError(t, err)

	assert.Equal(t, testUserID, avatar.UserID)
	assert.Equal(t, "avatar.png", avatar.FileName)
	assert.Equal(t, "image/png", avatar.MimeType)
	assert.Equal(t, int64(1024), avatar.SizeBytes)
	assert.Equal(t, domain.UploadStatusUploaded, avatar.UploadStatus)
	assert.Equal(t, domain.ProcessingStatusPending, avatar.ProcessingStatus)
	assert.Empty(t, avatar.ThumbnailS3Keys)

	assert.Equal(t, domain.OriginalObjectKey(testUserID, avatar.ID), storedKey,
		"файл должен лечь по детерминированному ключу оригинала")
	assert.Same(t, avatar, created, "в репозиторий уходит та же сущность, что и в ответ")
	assert.Equal(t, avatar.ID.String(), published.AvatarID)
	assert.Equal(t, testUserID, published.UserID)
	assert.Equal(t, storedKey, published.S3Key)
}

func TestUploadRejectsInvalidUserID(t *testing.T) {
	svc, _ := newService(t)

	in := uploadInput()
	in.UserID = "../etc"

	avatar, err := svc.Upload(t.Context(), in)
	require.ErrorIs(t, err, domain.ErrInvalidUserID)
	assert.Nil(t, avatar, "ни хранилище, ни репозиторий не должны быть тронуты")
}

func TestUploadStorageFailure(t *testing.T) {
	svc, d := newService(t)

	d.storage.EXPECT().Put(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(assert.AnError).Once()

	avatar, err := svc.Upload(t.Context(), uploadInput())
	require.ErrorIs(t, err, assert.AnError)
	assert.Contains(t, err.Error(), "upload avatar")
	assert.Nil(t, avatar)
}

// Файл уже в хранилище, а строка в базе не появилась. Реконсилятор обходит
// только БД и такой объект не найдёт — его надо убрать сразу.
func TestUploadRepositoryFailureRemovesOrphan(t *testing.T) {
	svc, d := newService(t)

	var storedKey string
	d.storage.EXPECT().Put(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(_ context.Context, key string, _ io.Reader, _ int64, _ string) { storedKey = key }).
		Return(nil).Once()
	d.repo.EXPECT().Create(mock.Anything, mock.Anything).Return(assert.AnError).Once()

	var deletedKey string
	d.storage.EXPECT().Delete(mock.Anything, mock.Anything).
		Run(func(_ context.Context, key string) { deletedKey = key }).
		Return(nil).Once()

	avatar, err := svc.Upload(t.Context(), uploadInput())
	require.ErrorIs(t, err, assert.AnError)
	assert.Nil(t, avatar)
	assert.Equal(t, storedKey, deletedKey, "убирается ровно тот объект, который только что записали")
}

// Уборка идёт на контексте без отмены: запрос к этому моменту мог быть отменён,
// но мусор в хранилище от этого никуда не денется.
func TestUploadRemovesOrphanEvenIfRequestCancelled(t *testing.T) {
	svc, d := newService(t)

	ctx, cancel := context.WithCancel(t.Context())

	d.storage.EXPECT().Put(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil).Once()
	d.repo.EXPECT().Create(mock.Anything, mock.Anything).
		Run(func(context.Context, *domain.Avatar) { cancel() }).
		Return(assert.AnError).Once()

	var cleanupErr error
	d.storage.EXPECT().Delete(mock.Anything, mock.Anything).
		Run(func(cleanupCtx context.Context, _ string) { cleanupErr = cleanupCtx.Err() }).
		Return(nil).Once()

	_, err := svc.Upload(ctx, uploadInput())
	require.ErrorIs(t, err, assert.AnError)
	assert.NoError(t, cleanupErr, "контекст уборки не должен быть отменён вместе с запросом")
}

// Не удалось убрать — важнее вернуть исходную ошибку загрузки, а про файл
// остаётся запись в логе.
func TestUploadReportsOriginalErrorWhenCleanupFails(t *testing.T) {
	svc, d := newService(t)

	d.storage.EXPECT().Put(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil).Once()
	d.repo.EXPECT().Create(mock.Anything, mock.Anything).Return(assert.AnError).Once()
	d.storage.EXPECT().Delete(mock.Anything, mock.Anything).Return(errors.New("хранилище недоступно")).Once()

	avatar, err := svc.Upload(t.Context(), uploadInput())
	require.ErrorIs(t, err, assert.AnError)
	assert.NotContains(t, err.Error(), "хранилище недоступно")
	assert.Nil(t, avatar)
}

// Сбой публикации не должен ронять загрузку: файл и метаданные уже на месте,
// событие переиздаст реконсилятор.
func TestUploadSurvivesPublishFailure(t *testing.T) {
	svc, d := newService(t)

	d.storage.EXPECT().Put(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil).Once()
	d.repo.EXPECT().Create(mock.Anything, mock.Anything).Return(nil).Once()
	d.publisher.EXPECT().PublishUpload(mock.Anything, mock.Anything).Return(assert.AnError).Once()

	avatar, err := svc.Upload(t.Context(), uploadInput())
	require.NoError(t, err)
	require.NotNil(t, avatar)
	assert.Equal(t, domain.ProcessingStatusPending, avatar.ProcessingStatus)
}

func TestOpen(t *testing.T) {
	id := uuid.New()
	original := domain.OriginalObjectKey(testUserID, id)
	small := domain.ThumbnailObjectKey(testUserID, id, domain.ThumbnailSmall)

	processed := &domain.Avatar{
		ID: id, UserID: testUserID, S3Key: original,
		ThumbnailS3Keys:  map[string]string{domain.ThumbnailSmall: small},
		ProcessingStatus: domain.ProcessingStatusCompleted,
	}
	pending := &domain.Avatar{
		ID: id, UserID: testUserID, S3Key: original,
		ThumbnailS3Keys:  map[string]string{},
		ProcessingStatus: domain.ProcessingStatusPending,
	}

	tests := []struct {
		name    string
		avatar  *domain.Avatar
		size    string
		wantKey string
		// fallback: первый Get по ключу миниатюры отвечает «объекта нет».
		thumbMissingInStorage bool
	}{
		{name: "оригинал", avatar: processed, size: domain.SizeOriginal, wantKey: original},
		{name: "пустой размер — оригинал", avatar: processed, size: "", wantKey: original},
		{name: "миниатюра готова", avatar: processed, size: domain.ThumbnailSmall, wantKey: small},
		{
			name:    "миниатюры ещё нет в метаданных — отдаём оригинал",
			avatar:  pending,
			size:    domain.ThumbnailSmall,
			wantKey: original,
		},
		{
			name:                  "ключ есть, а объекта нет — откат на оригинал",
			avatar:                processed,
			size:                  domain.ThumbnailSmall,
			wantKey:               original,
			thumbMissingInStorage: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, d := newService(t)

			if tt.thumbMissingInStorage {
				d.storage.EXPECT().Get(mock.Anything, small).Return(nil, domain.ErrObjectNotFound).Once()
			}
			d.storage.EXPECT().Get(mock.Anything, tt.wantKey).Return(object("содержимое"), nil).Once()

			obj, err := svc.Open(t.Context(), tt.avatar, tt.size)
			require.NoError(t, err)
			defer func() { _ = obj.Body.Close() }()

			body, err := io.ReadAll(obj.Body)
			require.NoError(t, err)
			assert.Equal(t, "содержимое", string(body))
		})
	}
}

// Open не ходит в репозиторий: метаданные ему передают уже прочитанными,
// чтобы условный запрос мог ответить 304 без обращения к хранилищу.
func TestOpenStorageFailure(t *testing.T) {
	svc, d := newService(t)

	id := uuid.New()
	key := domain.OriginalObjectKey(testUserID, id)
	avatar := &domain.Avatar{ID: id, UserID: testUserID, S3Key: key}

	d.storage.EXPECT().Get(mock.Anything, key).Return(nil, assert.AnError).Once()

	_, err := svc.Open(t.Context(), avatar, domain.SizeOriginal)
	require.ErrorIs(t, err, assert.AnError)
	assert.Contains(t, err.Error(), "get avatar content")
}

func TestCurrentMetadata(t *testing.T) {
	id := uuid.New()

	t.Run("последняя аватарка пользователя", func(t *testing.T) {
		svc, d := newService(t)

		d.repo.EXPECT().GetCurrentByUserID(mock.Anything, testUserID).
			Return(&domain.Avatar{ID: id, UserID: testUserID}, nil).Once()

		avatar, err := svc.CurrentMetadata(t.Context(), testUserID)
		require.NoError(t, err)
		assert.Equal(t, id, avatar.ID)
	})

	t.Run("невалидный идентификатор", func(t *testing.T) {
		svc, _ := newService(t)

		_, err := svc.CurrentMetadata(t.Context(), "плохой id")
		require.ErrorIs(t, err, domain.ErrInvalidUserID)
	})

	t.Run("аватарки нет", func(t *testing.T) {
		svc, d := newService(t)

		d.repo.EXPECT().GetCurrentByUserID(mock.Anything, testUserID).
			Return(nil, domain.ErrAvatarNotFound).Once()

		_, err := svc.CurrentMetadata(t.Context(), testUserID)
		require.ErrorIs(t, err, domain.ErrAvatarNotFound)
	})
}

func TestMetadata(t *testing.T) {
	t.Run("метаданные аватарки", func(t *testing.T) {
		svc, d := newService(t)
		id := uuid.New()

		d.repo.EXPECT().GetByID(mock.Anything, id).Return(&domain.Avatar{ID: id}, nil).Once()

		avatar, err := svc.Metadata(t.Context(), id)
		require.NoError(t, err)
		assert.Equal(t, id, avatar.ID)
	})

	t.Run("аватарки нет", func(t *testing.T) {
		svc, d := newService(t)
		id := uuid.New()

		d.repo.EXPECT().GetByID(mock.Anything, id).Return(nil, domain.ErrAvatarNotFound).Once()

		_, err := svc.Metadata(t.Context(), id)
		require.ErrorIs(t, err, domain.ErrAvatarNotFound)
	})
}

func TestList(t *testing.T) {
	t.Run("список пользователя", func(t *testing.T) {
		svc, d := newService(t)

		d.repo.EXPECT().ListByUserID(mock.Anything, testUserID).
			Return([]domain.Avatar{{UserID: testUserID}, {UserID: testUserID}}, nil).Once()

		avatars, err := svc.List(t.Context(), testUserID)
		require.NoError(t, err)
		assert.Len(t, avatars, 2)
	})

	t.Run("невалидный идентификатор", func(t *testing.T) {
		svc, _ := newService(t)

		_, err := svc.List(t.Context(), "")
		require.ErrorIs(t, err, domain.ErrInvalidUserID)
	})
}

func TestDelete(t *testing.T) {
	id := uuid.New()
	keys := []string{"original", "100x100.jpg"}

	t.Run("удаление публикует событие с ключами", func(t *testing.T) {
		svc, d := newService(t)

		d.repo.EXPECT().SoftDelete(mock.Anything, id, testUserID).Return(keys, nil).Once()

		var published domain.AvatarDeleteEvent
		d.publisher.EXPECT().PublishDelete(mock.Anything, mock.Anything).
			Run(func(_ context.Context, e domain.AvatarDeleteEvent) { published = e }).
			Return(nil).Once()

		require.NoError(t, svc.Delete(t.Context(), id, testUserID))
		assert.Equal(t, id.String(), published.AvatarID)
		assert.Equal(t, keys, published.S3Keys)
	})

	t.Run("чужая аватарка", func(t *testing.T) {
		svc, d := newService(t)

		d.repo.EXPECT().SoftDelete(mock.Anything, id, testUserID).Return(nil, domain.ErrForbidden).Once()

		require.ErrorIs(t, svc.Delete(t.Context(), id, testUserID), domain.ErrForbidden)
	})

	t.Run("невалидный идентификатор", func(t *testing.T) {
		svc, _ := newService(t)

		require.ErrorIs(t, svc.Delete(t.Context(), id, "bad id"), domain.ErrInvalidUserID)
	})

	// Файлы удаляет воркер по событию. Не опубликовали — останется мусор в S3,
	// но метаданные уже помечены удалёнными, и клиенту отвечаем успехом.
	t.Run("сбой публикации не ломает удаление", func(t *testing.T) {
		svc, d := newService(t)

		d.repo.EXPECT().SoftDelete(mock.Anything, id, testUserID).Return(keys, nil).Once()
		d.publisher.EXPECT().PublishDelete(mock.Anything, mock.Anything).Return(assert.AnError).Once()

		require.NoError(t, svc.Delete(t.Context(), id, testUserID))
	})
}

func TestDeleteCurrent(t *testing.T) {
	id := uuid.New()

	t.Run("удаляет последнюю аватарку", func(t *testing.T) {
		svc, d := newService(t)

		d.repo.EXPECT().GetCurrentByUserID(mock.Anything, testUserID).
			Return(&domain.Avatar{ID: id, UserID: testUserID}, nil).Once()
		d.repo.EXPECT().SoftDelete(mock.Anything, id, testUserID).Return([]string{"original"}, nil).Once()
		d.publisher.EXPECT().PublishDelete(mock.Anything, mock.Anything).Return(nil).Once()

		require.NoError(t, svc.DeleteCurrent(t.Context(), testUserID))
	})

	t.Run("аватарки нет", func(t *testing.T) {
		svc, d := newService(t)

		d.repo.EXPECT().GetCurrentByUserID(mock.Anything, testUserID).
			Return(nil, domain.ErrAvatarNotFound).Once()

		require.ErrorIs(t, svc.DeleteCurrent(t.Context(), testUserID), domain.ErrAvatarNotFound)
	})

	t.Run("невалидный идентификатор", func(t *testing.T) {
		svc, _ := newService(t)

		require.ErrorIs(t, svc.DeleteCurrent(t.Context(), ""), domain.ErrInvalidUserID)
	})
}
