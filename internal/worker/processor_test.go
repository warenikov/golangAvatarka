package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"go-avatar-service/internal/domain"
)

const (
	testUserID    = "user-1"
	testMaxPixels = 1 << 24
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 200, A: 255})
		}
	}

	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))

	return buf.Bytes()
}

func uploadEvent(t *testing.T, avatarID uuid.UUID, key string) []byte {
	t.Helper()

	body, err := json.Marshal(domain.AvatarUploadEvent{
		AvatarID: avatarID.String(), UserID: testUserID, S3Key: key,
	})
	require.NoError(t, err)

	return body
}

func pendingAvatar(id uuid.UUID) *domain.Avatar {
	return &domain.Avatar{
		ID: id, UserID: testUserID,
		S3Key:            domain.OriginalObjectKey(testUserID, id),
		ThumbnailS3Keys:  map[string]string{},
		ProcessingStatus: domain.ProcessingStatusPending,
	}
}

func TestHandleUploadBuildsThumbnails(t *testing.T) {
	id := uuid.New()
	avatar := pendingAvatar(id)
	source := pngBytes(t, 400, 200)

	repo := NewMockRepository(t)
	storage := NewMockObjectStorage(t)

	repo.EXPECT().GetByID(mock.Anything, id).Return(avatar, nil).Once()
	storage.EXPECT().Get(mock.Anything, avatar.S3Key).Return(&domain.Object{
		Body: io.NopCloser(bytes.NewReader(source)), ContentType: "image/png", Size: int64(len(source)),
	}, nil).Once()

	uploaded := make(map[string][]byte, 2)
	storage.EXPECT().Put(mock.Anything, mock.Anything, mock.Anything, mock.Anything, "image/jpeg").
		Run(func(_ context.Context, key string, r io.Reader, _ int64, _ string) {
			data, err := io.ReadAll(r)
			require.NoError(t, err)
			uploaded[key] = data
		}).
		Return(nil).Twice()

	var gotThumbnails map[string]string
	var gotWidth, gotHeight int
	repo.EXPECT().UpdateProcessingResult(mock.Anything, id, mock.Anything, mock.Anything, mock.Anything).
		Run(func(_ context.Context, _ uuid.UUID, thumbnails map[string]string, w, h int) {
			gotThumbnails, gotWidth, gotHeight = thumbnails, w, h
		}).
		Return(true, nil).Once()

	p := NewProcessor(repo, storage, testMaxPixels, discardLogger())
	require.NoError(t, p.HandleUpload(t.Context(), uploadEvent(t, id, avatar.S3Key)))

	assert.Equal(t, 400, gotWidth)
	assert.Equal(t, 200, gotHeight)
	assert.Equal(t, domain.ThumbnailKeys(testUserID, id), gotThumbnails)

	require.Len(t, uploaded, 2)
	for size, key := range domain.ThumbnailKeys(testUserID, id) {
		data, ok := uploaded[key]
		require.True(t, ok, "миниатюра %s не загружена", size)

		cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
		require.NoError(t, err)
		assert.Equal(t, "jpeg", format)
		assert.Equal(t, cfg.Width, cfg.Height, "миниатюра должна быть квадратной")
	}
}

// Повторная доставка того же события — штатная ситуация для RabbitMQ:
// подтверждение могло не дойти. Второй проход не должен ничего перезаписывать.
func TestHandleUploadIsIdempotent(t *testing.T) {
	id := uuid.New()
	avatar := pendingAvatar(id)
	source := pngBytes(t, 120, 120)
	body := uploadEvent(t, id, avatar.S3Key)

	repo := NewMockRepository(t)
	storage := NewMockObjectStorage(t)

	// Первый проход: аватарка в ожидании, работа выполняется целиком.
	repo.EXPECT().GetByID(mock.Anything, id).Return(avatar, nil).Once()
	storage.EXPECT().Get(mock.Anything, avatar.S3Key).Return(&domain.Object{
		Body: io.NopCloser(bytes.NewReader(source)), Size: int64(len(source)),
	}, nil).Once()
	storage.EXPECT().Put(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil).Twice()
	repo.EXPECT().UpdateProcessingResult(mock.Anything, id, mock.Anything, mock.Anything, mock.Anything).
		Return(true, nil).Once()

	// Второй проход: статус уже completed — ни скачивания, ни выгрузки, ни записи в базу.
	processed := pendingAvatar(id)
	processed.ProcessingStatus = domain.ProcessingStatusCompleted
	processed.ThumbnailS3Keys = domain.ThumbnailKeys(testUserID, id)
	repo.EXPECT().GetByID(mock.Anything, id).Return(processed, nil).Once()

	p := NewProcessor(repo, storage, testMaxPixels, discardLogger())
	require.NoError(t, p.HandleUpload(t.Context(), body))
	require.NoError(t, p.HandleUpload(t.Context(), body))

	storage.AssertNumberOfCalls(t, "Put", 2)
	repo.AssertNumberOfCalls(t, "UpdateProcessingResult", 1)
}

func TestHandleUploadSkipsDeletedAvatar(t *testing.T) {
	id := uuid.New()

	repo := NewMockRepository(t)
	storage := NewMockObjectStorage(t)

	repo.EXPECT().GetByID(mock.Anything, id).Return(nil, domain.ErrAvatarNotFound).Once()

	p := NewProcessor(repo, storage, testMaxPixels, discardLogger())
	require.NoError(t, p.HandleUpload(t.Context(), uploadEvent(t, id, "any")),
		"удалённая до обработки аватарка не повод отправлять событие в ретрай")
}

// Битый файл не станет корректным от повтора: помечаем провал и подтверждаем событие.
func TestHandleUploadMarksUndecodableImageFailed(t *testing.T) {
	id := uuid.New()
	avatar := pendingAvatar(id)

	repo := NewMockRepository(t)
	storage := NewMockObjectStorage(t)

	repo.EXPECT().GetByID(mock.Anything, id).Return(avatar, nil).Once()
	storage.EXPECT().Get(mock.Anything, avatar.S3Key).Return(&domain.Object{
		Body: io.NopCloser(bytes.NewReader([]byte("совсем не картинка"))),
	}, nil).Once()
	repo.EXPECT().SetProcessingStatus(mock.Anything, id, domain.ProcessingStatusFailed).Return(nil).Once()

	p := NewProcessor(repo, storage, testMaxPixels, discardLogger())
	require.NoError(t, p.HandleUpload(t.Context(), uploadEvent(t, id, avatar.S3Key)))
}

func TestHandleUploadRejectsTooManyPixels(t *testing.T) {
	id := uuid.New()
	avatar := pendingAvatar(id)
	source := pngBytes(t, 100, 100)

	repo := NewMockRepository(t)
	storage := NewMockObjectStorage(t)

	repo.EXPECT().GetByID(mock.Anything, id).Return(avatar, nil).Once()
	storage.EXPECT().Get(mock.Anything, avatar.S3Key).Return(&domain.Object{
		Body: io.NopCloser(bytes.NewReader(source)),
	}, nil).Once()
	repo.EXPECT().SetProcessingStatus(mock.Anything, id, domain.ProcessingStatusFailed).Return(nil).Once()

	p := NewProcessor(repo, storage, 100, discardLogger())
	require.NoError(t, p.HandleUpload(t.Context(), uploadEvent(t, id, avatar.S3Key)))
}

func TestHandleUploadErrors(t *testing.T) {
	id := uuid.New()
	source := pngBytes(t, 60, 60)

	tests := []struct {
		name    string
		body    []byte
		arrange func(*MockRepository, *MockObjectStorage)
		wantErr string
	}{
		{
			name:    "битый JSON",
			body:    []byte("{не json"),
			arrange: func(*MockRepository, *MockObjectStorage) {},
			wantErr: "unmarshal event",
		},
		{
			name:    "идентификатор не UUID",
			body:    []byte(`{"avatar_id":"не-uuid","user_id":"user-1","s3_key":"k"}`),
			arrange: func(*MockRepository, *MockObjectStorage) {},
			wantErr: "parse avatar id",
		},
		{
			name: "база недоступна",
			arrange: func(repo *MockRepository, _ *MockObjectStorage) {
				repo.EXPECT().GetByID(mock.Anything, id).Return(nil, assert.AnError).Once()
			},
			wantErr: assert.AnError.Error(),
		},
		{
			name: "оригинал не скачался",
			arrange: func(repo *MockRepository, storage *MockObjectStorage) {
				repo.EXPECT().GetByID(mock.Anything, id).Return(pendingAvatar(id), nil).Once()
				storage.EXPECT().Get(mock.Anything, mock.Anything).Return(nil, assert.AnError).Once()
			},
			wantErr: assert.AnError.Error(),
		},
		{
			name: "миниатюра не выгрузилась",
			arrange: func(repo *MockRepository, storage *MockObjectStorage) {
				repo.EXPECT().GetByID(mock.Anything, id).Return(pendingAvatar(id), nil).Once()
				storage.EXPECT().Get(mock.Anything, mock.Anything).Return(&domain.Object{
					Body: io.NopCloser(bytes.NewReader(source)),
				}, nil).Once()
				storage.EXPECT().Put(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
					Return(assert.AnError).Once()
			},
			wantErr: assert.AnError.Error(),
		},
		{
			name: "результат не записался",
			arrange: func(repo *MockRepository, storage *MockObjectStorage) {
				repo.EXPECT().GetByID(mock.Anything, id).Return(pendingAvatar(id), nil).Once()
				storage.EXPECT().Get(mock.Anything, mock.Anything).Return(&domain.Object{
					Body: io.NopCloser(bytes.NewReader(source)),
				}, nil).Once()
				storage.EXPECT().Put(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
					Return(nil).Twice()
				repo.EXPECT().UpdateProcessingResult(mock.Anything, id, mock.Anything, mock.Anything, mock.Anything).
					Return(false, assert.AnError).Once()
			},
			wantErr: assert.AnError.Error(),
		},
		{
			name: "статус провала не записался",
			arrange: func(repo *MockRepository, storage *MockObjectStorage) {
				repo.EXPECT().GetByID(mock.Anything, id).Return(pendingAvatar(id), nil).Once()
				storage.EXPECT().Get(mock.Anything, mock.Anything).Return(&domain.Object{
					Body: io.NopCloser(bytes.NewReader([]byte("мусор"))),
				}, nil).Once()
				repo.EXPECT().SetProcessingStatus(mock.Anything, id, domain.ProcessingStatusFailed).
					Return(assert.AnError).Once()
			},
			wantErr: assert.AnError.Error(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := NewMockRepository(t)
			storage := NewMockObjectStorage(t)
			tt.arrange(repo, storage)

			body := tt.body
			if body == nil {
				body = uploadEvent(t, id, domain.OriginalObjectKey(testUserID, id))
			}

			p := NewProcessor(repo, storage, testMaxPixels, discardLogger())
			err := p.HandleUpload(t.Context(), body)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestHandleUploadReadFailure(t *testing.T) {
	id := uuid.New()
	avatar := pendingAvatar(id)

	repo := NewMockRepository(t)
	storage := NewMockObjectStorage(t)

	repo.EXPECT().GetByID(mock.Anything, id).Return(avatar, nil).Once()
	storage.EXPECT().Get(mock.Anything, avatar.S3Key).Return(&domain.Object{
		Body: io.NopCloser(errorReader{}),
	}, nil).Once()

	p := NewProcessor(repo, storage, testMaxPixels, discardLogger())
	err := p.HandleUpload(t.Context(), uploadEvent(t, id, avatar.S3Key))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "read object")
}

// errorReader — поток, который обрывается на первом чтении.
type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, assert.AnError }

func TestHandleDelete(t *testing.T) {
	keys := []string{"avatars/user-1/a/original", "avatars/user-1/a/100x100.jpg"}

	body, err := json.Marshal(domain.AvatarDeleteEvent{AvatarID: "a", S3Keys: keys})
	require.NoError(t, err)

	t.Run("удаляет все ключи", func(t *testing.T) {
		repo := NewMockRepository(t)
		storage := NewMockObjectStorage(t)

		storage.EXPECT().DeleteMany(mock.Anything, keys).Return(nil).Once()

		p := NewProcessor(repo, storage, testMaxPixels, discardLogger())
		require.NoError(t, p.HandleDelete(t.Context(), body))
	})

	t.Run("битый JSON", func(t *testing.T) {
		p := NewProcessor(NewMockRepository(t), NewMockObjectStorage(t), testMaxPixels, discardLogger())

		err := p.HandleDelete(t.Context(), []byte("{не json"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unmarshal event")
	})

	t.Run("хранилище недоступно — событие уйдёт в ретрай", func(t *testing.T) {
		storage := NewMockObjectStorage(t)
		storage.EXPECT().DeleteMany(mock.Anything, keys).Return(assert.AnError).Once()

		p := NewProcessor(NewMockRepository(t), storage, testMaxPixels, discardLogger())
		require.ErrorIs(t, p.HandleDelete(t.Context(), body), assert.AnError)
	})
}
