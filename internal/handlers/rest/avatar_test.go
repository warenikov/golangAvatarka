package rest_test

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"go-avatar-service/internal/config"
	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/handlers/rest"
	"go-avatar-service/internal/services"
)

const (
	testUserID     = "user-1"
	testMaxUpload  = 1 << 20
	headerUserID   = "X-User-ID"
	pngContentType = "image/png"
)

type apiDeps struct {
	repo      *MockAvatarRepository
	storage   *MockObjectStorage
	publisher *MockEventPublisher
}

func newAPI(t *testing.T) (http.Handler, apiDeps) {
	t.Helper()

	d := apiDeps{
		repo:      NewMockAvatarRepository(t),
		storage:   NewMockObjectStorage(t),
		publisher: NewMockEventPublisher(t),
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	cfg := &config.Config{}
	cfg.App.MaxUploadBytes = testMaxUpload
	cfg.App.AllowedMIME = []string{"image/jpeg", "image/png", "image/webp"}

	svc := services.NewAvatarService(d.repo, d.storage, d.publisher, log)
	h := rest.NewAvatarHandler(svc, cfg, log)

	r := chi.NewRouter()
	r.Route("/api/v1", func(api chi.Router) {
		api.Post("/avatars", h.Upload)
		api.Get("/avatars/{avatar_id}", h.Get)
		api.Get("/avatars/{avatar_id}/metadata", h.Metadata)
		api.Delete("/avatars/{avatar_id}", h.Delete)
		api.Get("/users/{user_id}/avatar", h.GetCurrent)
		api.Get("/users/{user_id}/avatars", h.List)
		api.Delete("/users/{user_id}/avatar", h.DeleteCurrent)
	})

	return r, d
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 90, A: 255})
		}
	}

	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))

	return buf.Bytes()
}

func multipartBody(t *testing.T, field, filename string, content []byte) (string, *bytes.Buffer) {
	t.Helper()

	var body bytes.Buffer
	form := multipart.NewWriter(&body)

	if field != "" {
		part, err := form.CreateFormFile(field, filename)
		require.NoError(t, err)
		_, err = part.Write(content)
		require.NoError(t, err)
	} else {
		require.NoError(t, form.WriteField("note", "без файла"))
	}

	require.NoError(t, form.Close())

	return form.FormDataContentType(), &body
}

func uploadRequest(t *testing.T, userID, field string, content []byte) *http.Request {
	t.Helper()

	contentType, body := multipartBody(t, field, "avatar.png", content)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/avatars", body)
	req.Header.Set("Content-Type", contentType)
	if userID != "" {
		req.Header.Set(headerUserID, userID)
	}

	return req
}

func decodeJSON[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()

	var v T
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &v))

	return v
}

func storedAvatar(id uuid.UUID) *domain.Avatar {
	width, height := 400, 300

	return &domain.Avatar{
		ID: id, UserID: testUserID,
		FileName: "avatar.png", MimeType: pngContentType, SizeBytes: 2048,
		Width: &width, Height: &height,
		S3Key:            domain.OriginalObjectKey(testUserID, id),
		ThumbnailS3Keys:  domain.ThumbnailKeys(testUserID, id),
		UploadStatus:     domain.UploadStatusUploaded,
		ProcessingStatus: domain.ProcessingStatusCompleted,
		CreatedAt:        time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC),
		UpdatedAt:        time.Date(2026, 8, 27, 10, 1, 0, 0, time.UTC),
	}
}

func TestUploadCreatesAvatar(t *testing.T) {
	api, d := newAPI(t)

	d.storage.EXPECT().Put(mock.Anything, mock.Anything, mock.Anything, mock.Anything, pngContentType).
		Return(nil).Once()
	d.repo.EXPECT().Create(mock.Anything, mock.Anything).Return(nil).Once()
	d.publisher.EXPECT().PublishUpload(mock.Anything, mock.Anything).Return(nil).Once()

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, uploadRequest(t, testUserID, "file", pngBytes(t, 60, 40)))

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")

	body := decodeJSON[map[string]any](t, rec)
	assert.Equal(t, testUserID, body["user_id"])
	assert.Equal(t, "processing", body["status"])
	assert.Equal(t, "/api/v1/avatars/"+body["id"].(string), body["url"])

	_, err := uuid.Parse(body["id"].(string))
	assert.NoError(t, err, "идентификатор должен быть UUID")
}

// Готовый фронтенд шлёт файл в поле image, curl из документации — в file.
// Оба варианта должны приниматься.
func TestUploadAcceptsImageFieldName(t *testing.T) {
	api, d := newAPI(t)

	d.storage.EXPECT().Put(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil).Once()
	d.repo.EXPECT().Create(mock.Anything, mock.Anything).Return(nil).Once()
	d.publisher.EXPECT().PublishUpload(mock.Anything, mock.Anything).Return(nil).Once()

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, uploadRequest(t, testUserID, "image", pngBytes(t, 20, 20)))

	assert.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
}

func TestUploadRejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name       string
		userID     string
		field      string
		content    []byte
		wantStatus int
		wantError  string
	}{
		{
			name:       "нет заголовка X-User-ID",
			field:      "file",
			content:    pngBytes(t, 20, 20),
			wantStatus: http.StatusBadRequest,
			wantError:  "Invalid user id",
		},
		{
			name:       "недопустимый идентификатор",
			userID:     "user/../root",
			field:      "file",
			content:    pngBytes(t, 20, 20),
			wantStatus: http.StatusBadRequest,
			wantError:  "Invalid user id",
		},
		{
			name:       "файла нет в форме",
			userID:     testUserID,
			wantStatus: http.StatusBadRequest,
			wantError:  "File is required",
		},
		{
			name:       "текстовый файл под видом картинки",
			userID:     testUserID,
			field:      "file",
			content:    []byte(strings.Repeat("не картинка, а текст. ", 40)),
			wantStatus: http.StatusBadRequest,
			wantError:  "Invalid file format",
		},
		{
			name:       "файл больше лимита",
			userID:     testUserID,
			field:      "file",
			content:    bytes.Repeat([]byte("x"), testMaxUpload+1),
			wantStatus: http.StatusRequestEntityTooLarge,
			wantError:  "File too large",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api, _ := newAPI(t)

			rec := httptest.NewRecorder()
			api.ServeHTTP(rec, uploadRequest(t, tt.userID, tt.field, tt.content))

			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), tt.wantError)
		})
	}
}

func TestUploadStorageFailure(t *testing.T) {
	api, d := newAPI(t)

	d.storage.EXPECT().Put(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(assert.AnError).Once()

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, uploadRequest(t, testUserID, "file", pngBytes(t, 20, 20)))

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), assert.AnError.Error(), "внутренняя ошибка не раскрывается клиенту")
}

func TestGetStreamsContent(t *testing.T) {
	api, d := newAPI(t)

	id := uuid.New()
	avatar := storedAvatar(id)
	content := "содержимое-оригинала"

	d.repo.EXPECT().GetByID(mock.Anything, id).Return(avatar, nil).Once()
	d.storage.EXPECT().Get(mock.Anything, avatar.S3Key).Return(&domain.Object{
		Body: io.NopCloser(strings.NewReader(content)), ContentType: pngContentType, Size: int64(len(content)),
	}, nil).Once()

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/avatars/"+id.String(), nil))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, content, rec.Body.String())
	assert.Equal(t, pngContentType, rec.Header().Get("Content-Type"))
	assert.Equal(t, "max-age=86400", rec.Header().Get("Cache-Control"))
	assert.NotEmpty(t, rec.Header().Get("ETag"))
}

// Ревалидация закешированной аватарки не должна стоить похода в хранилище:
// ETag считается по метаданным, а тело объекта при 304 всё равно выбрасывается.
func TestGetReturnsNotModified(t *testing.T) {
	api, d := newAPI(t)

	id := uuid.New()
	avatar := storedAvatar(id)

	d.repo.EXPECT().GetByID(mock.Anything, id).Return(avatar, nil).Twice()
	d.storage.EXPECT().Get(mock.Anything, avatar.S3Key).Return(&domain.Object{
		Body: io.NopCloser(strings.NewReader("данные")), ContentType: pngContentType, Size: 6,
	}, nil).Once()

	first := httptest.NewRecorder()
	api.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/api/v1/avatars/"+id.String(), nil))
	require.Equal(t, http.StatusOK, first.Code)

	etag := first.Header().Get("ETag")
	require.NotEmpty(t, etag)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/avatars/"+id.String(), nil)
	req.Header.Set("If-None-Match", etag)

	second := httptest.NewRecorder()
	api.ServeHTTP(second, req)

	assert.Equal(t, http.StatusNotModified, second.Code)
	assert.Equal(t, etag, second.Header().Get("ETag"))
	assert.Equal(t, "max-age=86400", second.Header().Get("Cache-Control"))
	assert.Empty(t, second.Body.String())

	d.storage.AssertNumberOfCalls(t, "Get", 1)
	d.repo.AssertNumberOfCalls(t, "GetByID", 2)
}

// Заголовок может прийти списком или звёздочкой — и то, и другое обязано
// давать 304 без обращения к хранилищу.
func TestGetNotModifiedForListAndWildcard(t *testing.T) {
	tests := []struct {
		name  string
		build func(etag string) string
	}{
		{"точное совпадение", func(e string) string { return e }},
		{"звёздочка", func(string) string { return "*" }},
		{"список, совпадение последним", func(e string) string { return `"x", "y", ` + e }},
		{"слабый тег", func(e string) string { return "W/" + e }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api, d := newAPI(t)

			id := uuid.New()
			avatar := storedAvatar(id)

			d.repo.EXPECT().GetByID(mock.Anything, id).Return(avatar, nil).Twice()
			d.storage.EXPECT().Get(mock.Anything, avatar.S3Key).Return(&domain.Object{
				Body: io.NopCloser(strings.NewReader("данные")), ContentType: pngContentType, Size: 6,
			}, nil).Once()

			first := httptest.NewRecorder()
			api.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/api/v1/avatars/"+id.String(), nil))
			require.Equal(t, http.StatusOK, first.Code)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/avatars/"+id.String(), nil)
			req.Header.Set("If-None-Match", tt.build(first.Header().Get("ETag")))

			second := httptest.NewRecorder()
			api.ServeHTTP(second, req)

			assert.Equal(t, http.StatusNotModified, second.Code)
			d.storage.AssertNumberOfCalls(t, "Get", 1)
		})
	}
}

// Повреждённый заголовок не должен превращаться в ложный 304.
func TestGetIgnoresMalformedIfNoneMatch(t *testing.T) {
	api, d := newAPI(t)

	id := uuid.New()
	avatar := storedAvatar(id)

	d.repo.EXPECT().GetByID(mock.Anything, id).Return(avatar, nil).Once()
	d.storage.EXPECT().Get(mock.Anything, avatar.S3Key).Return(&domain.Object{
		Body: io.NopCloser(strings.NewReader("данные")), ContentType: pngContentType, Size: 6,
	}, nil).Once()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/avatars/"+id.String(), nil)
	req.Header.Set("If-None-Match", "abc123")

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code, "тег без кавычек невалиден — отдаём тело")
}

// Тот же контракт для /users/{user_id}/avatar.
func TestGetCurrentReturnsNotModified(t *testing.T) {
	api, d := newAPI(t)

	id := uuid.New()
	avatar := storedAvatar(id)

	d.repo.EXPECT().GetCurrentByUserID(mock.Anything, testUserID).Return(avatar, nil).Twice()
	d.storage.EXPECT().Get(mock.Anything, avatar.S3Key).Return(&domain.Object{
		Body: io.NopCloser(strings.NewReader("данные")), ContentType: pngContentType, Size: 6,
	}, nil).Once()

	path := "/api/v1/users/" + testUserID + "/avatar"

	first := httptest.NewRecorder()
	api.ServeHTTP(first, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusOK, first.Code)

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("If-None-Match", first.Header().Get("ETag"))

	second := httptest.NewRecorder()
	api.ServeHTTP(second, req)

	assert.Equal(t, http.StatusNotModified, second.Code)
	d.storage.AssertNumberOfCalls(t, "Get", 1)
}

// ETag завязан на время обновления: после появления миниатюр тот же
// If-None-Match больше не совпадает и клиент получает свежее содержимое.
func TestETagChangesAfterProcessing(t *testing.T) {
	api, d := newAPI(t)

	id := uuid.New()
	before := storedAvatar(id)
	before.ProcessingStatus = domain.ProcessingStatusPending
	before.UpdatedAt = time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)

	after := storedAvatar(id)
	after.UpdatedAt = time.Date(2026, 8, 27, 10, 5, 0, 0, time.UTC)

	d.repo.EXPECT().GetByID(mock.Anything, id).Return(before, nil).Once()
	d.storage.EXPECT().Get(mock.Anything, before.S3Key).Return(&domain.Object{
		Body: io.NopCloser(strings.NewReader("оригинал")), ContentType: pngContentType, Size: 8,
	}, nil).Once()

	first := httptest.NewRecorder()
	api.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/api/v1/avatars/"+id.String(), nil))
	oldETag := first.Header().Get("ETag")

	d.repo.EXPECT().GetByID(mock.Anything, id).Return(after, nil).Once()
	d.storage.EXPECT().Get(mock.Anything, after.S3Key).Return(&domain.Object{
		Body: io.NopCloser(strings.NewReader("обновлён")), ContentType: pngContentType, Size: 8,
	}, nil).Once()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/avatars/"+id.String(), nil)
	req.Header.Set("If-None-Match", oldETag)

	second := httptest.NewRecorder()
	api.ServeHTTP(second, req)

	assert.Equal(t, http.StatusOK, second.Code)
	assert.NotEqual(t, oldETag, second.Header().Get("ETag"))
	assert.Equal(t, "обновлён", second.Body.String())
}

func TestGetServesThumbnail(t *testing.T) {
	api, d := newAPI(t)

	id := uuid.New()
	avatar := storedAvatar(id)
	thumbKey := avatar.ThumbnailS3Keys[domain.ThumbnailSmall]

	d.repo.EXPECT().GetByID(mock.Anything, id).Return(avatar, nil).Once()
	d.storage.EXPECT().Get(mock.Anything, thumbKey).Return(&domain.Object{
		Body: io.NopCloser(strings.NewReader("миниатюра")), ContentType: "image/jpeg", Size: 9,
	}, nil).Once()

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/avatars/"+id.String()+"?size=100x100", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "image/jpeg", rec.Header().Get("Content-Type"))
}

func TestGetRejectsBadRequests(t *testing.T) {
	id := uuid.New()

	tests := []struct {
		name      string
		path      string
		wantError string
	}{
		{"идентификатор не UUID", "/api/v1/avatars/не-uuid", "Invalid avatar id"},
		{"неизвестный размер", "/api/v1/avatars/" + id.String() + "?size=500x500", "Invalid size"},
		{"конвертация не поддерживается", "/api/v1/avatars/" + id.String() + "?format=webp", "Unsupported parameter"},
		{"format=jpeg тоже отвергается", "/api/v1/avatars/" + id.String() + "?format=jpeg", "Unsupported parameter"},
		{"format=png тоже отвергается", "/api/v1/avatars/" + id.String() + "?format=png", "Unsupported parameter"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api, _ := newAPI(t)

			rec := httptest.NewRecorder()
			api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))

			require.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, rec.Body.String(), tt.wantError)
		})
	}
}

func TestGetDeletedAvatarReturnsNotFound(t *testing.T) {
	api, d := newAPI(t)

	id := uuid.New()
	d.repo.EXPECT().GetByID(mock.Anything, id).Return(nil, domain.ErrAvatarNotFound).Once()

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/avatars/"+id.String(), nil))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "Avatar not found")
}

func TestGetCurrentFallsBackToDefaultImage(t *testing.T) {
	tests := []struct {
		name    string
		userID  string
		arrange func(apiDeps, string)
	}{
		{
			name:   "у пользователя нет аватарки",
			userID: testUserID,
			arrange: func(d apiDeps, userID string) {
				d.repo.EXPECT().GetCurrentByUserID(mock.Anything, userID).
					Return(nil, domain.ErrAvatarNotFound).Once()
			},
		},
		{
			name:    "недопустимый идентификатор",
			userID:  "user%20bad",
			arrange: func(apiDeps, string) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api, d := newAPI(t)
			tt.arrange(d, tt.userID)

			rec := httptest.NewRecorder()
			api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/users/"+tt.userID+"/avatar", nil))

			require.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, "image/png", rec.Header().Get("Content-Type"))
			assert.Equal(t, "true", rec.Header().Get("X-Avatar-Fallback"))
			assert.Equal(t, "no-cache", rec.Header().Get("Cache-Control"),
				"заглушка не кешируется: иначе клиент не увидит появившуюся аватарку")
			assert.NotEmpty(t, rec.Body.Bytes())
		})
	}
}

func TestGetCurrentStreamsAvatar(t *testing.T) {
	api, d := newAPI(t)

	id := uuid.New()
	avatar := storedAvatar(id)

	d.repo.EXPECT().GetCurrentByUserID(mock.Anything, testUserID).Return(avatar, nil).Once()
	d.storage.EXPECT().Get(mock.Anything, avatar.S3Key).Return(&domain.Object{
		Body: io.NopCloser(strings.NewReader("аватарка")), ContentType: pngContentType, Size: 8,
	}, nil).Once()

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/users/"+testUserID+"/avatar", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Header().Get("X-Avatar-Fallback"))
}

func TestGetCurrentStorageFailure(t *testing.T) {
	api, d := newAPI(t)

	id := uuid.New()
	avatar := storedAvatar(id)

	d.repo.EXPECT().GetCurrentByUserID(mock.Anything, testUserID).Return(avatar, nil).Once()
	d.storage.EXPECT().Get(mock.Anything, avatar.S3Key).Return(nil, assert.AnError).Once()

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/users/"+testUserID+"/avatar", nil))

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestMetadata(t *testing.T) {
	api, d := newAPI(t)

	id := uuid.New()
	avatar := storedAvatar(id)

	d.repo.EXPECT().GetByID(mock.Anything, id).Return(avatar, nil).Once()

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/avatars/"+id.String()+"/metadata", nil))

	require.Equal(t, http.StatusOK, rec.Code)

	body := decodeJSON[map[string]any](t, rec)
	assert.Equal(t, id.String(), body["id"])
	assert.Equal(t, testUserID, body["user_id"])
	assert.Equal(t, "avatar.png", body["file_name"])
	assert.Equal(t, float64(2048), body["size"])
	assert.Equal(t, "completed", body["processing_status"])

	dims, ok := body["dimensions"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(400), dims["width"])
	assert.Equal(t, float64(300), dims["height"])

	thumbs, ok := body["thumbnails"].([]any)
	require.True(t, ok)
	assert.Len(t, thumbs, 2)
}

func TestMetadataWithoutThumbnails(t *testing.T) {
	api, d := newAPI(t)

	id := uuid.New()
	avatar := &domain.Avatar{
		ID: id, UserID: testUserID, ThumbnailS3Keys: map[string]string{},
		ProcessingStatus: domain.ProcessingStatusPending,
	}

	d.repo.EXPECT().GetByID(mock.Anything, id).Return(avatar, nil).Once()

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/avatars/"+id.String()+"/metadata", nil))

	require.Equal(t, http.StatusOK, rec.Code)

	body := decodeJSON[map[string]any](t, rec)
	assert.Empty(t, body["thumbnails"])
	assert.Nil(t, body["dimensions"], "размеры появляются только после обработки")
}

func TestList(t *testing.T) {
	api, d := newAPI(t)

	first, second := storedAvatar(uuid.New()), storedAvatar(uuid.New())
	d.repo.EXPECT().ListByUserID(mock.Anything, testUserID).
		Return([]domain.Avatar{*first, *second}, nil).Once()

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/users/"+testUserID+"/avatars", nil))

	require.Equal(t, http.StatusOK, rec.Code)

	body := decodeJSON[map[string]any](t, rec)
	assert.Equal(t, testUserID, body["user_id"])
	assert.Equal(t, float64(2), body["count"])
	assert.Len(t, body["avatars"], 2)
}

func TestListEmpty(t *testing.T) {
	api, d := newAPI(t)

	d.repo.EXPECT().ListByUserID(mock.Anything, testUserID).Return(nil, nil).Once()

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/users/"+testUserID+"/avatars", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"avatars":[]`, "пустой список — массив, а не null")
}

func TestListInvalidUserID(t *testing.T) {
	api, _ := newAPI(t)

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/users/bad%20user/avatars", nil))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "Invalid user id")
}

func TestDelete(t *testing.T) {
	api, d := newAPI(t)

	id := uuid.New()
	d.repo.EXPECT().SoftDelete(mock.Anything, id, testUserID).Return([]string{"original"}, nil).Once()
	d.publisher.EXPECT().PublishDelete(mock.Anything, mock.Anything).Return(nil).Once()

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/avatars/"+id.String(), nil)
	req.Header.Set(headerUserID, testUserID)

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, rec.Body.String())
}

func TestDeleteForeignAvatar(t *testing.T) {
	api, d := newAPI(t)

	id := uuid.New()
	d.repo.EXPECT().SoftDelete(mock.Anything, id, testUserID).Return(nil, domain.ErrForbidden).Once()

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/avatars/"+id.String(), nil)
	req.Header.Set(headerUserID, testUserID)

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "Forbidden")
}

func TestDeleteRejectsBadRequests(t *testing.T) {
	id := uuid.New()

	tests := []struct {
		name       string
		userID     string
		path       string
		wantStatus int
	}{
		{"без заголовка", "", "/api/v1/avatars/" + id.String(), http.StatusBadRequest},
		{"идентификатор не UUID", testUserID, "/api/v1/avatars/не-uuid", http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api, _ := newAPI(t)

			req := httptest.NewRequest(http.MethodDelete, tt.path, nil)
			if tt.userID != "" {
				req.Header.Set(headerUserID, tt.userID)
			}

			rec := httptest.NewRecorder()
			api.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}

func TestDeleteAlreadyDeleted(t *testing.T) {
	api, d := newAPI(t)

	id := uuid.New()
	d.repo.EXPECT().SoftDelete(mock.Anything, id, testUserID).Return(nil, domain.ErrAvatarNotFound).Once()

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/avatars/"+id.String(), nil)
	req.Header.Set(headerUserID, testUserID)

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestDeleteCurrent(t *testing.T) {
	t.Run("удаляет свою аватарку", func(t *testing.T) {
		api, d := newAPI(t)

		id := uuid.New()
		d.repo.EXPECT().GetCurrentByUserID(mock.Anything, testUserID).
			Return(&domain.Avatar{ID: id, UserID: testUserID}, nil).Once()
		d.repo.EXPECT().SoftDelete(mock.Anything, id, testUserID).Return([]string{"original"}, nil).Once()
		d.publisher.EXPECT().PublishDelete(mock.Anything, mock.Anything).Return(nil).Once()

		req := httptest.NewRequest(http.MethodDelete, "/api/v1/users/"+testUserID+"/avatar", nil)
		req.Header.Set(headerUserID, testUserID)

		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusNoContent, rec.Code)
	})

	t.Run("чужого пользователя удалить нельзя", func(t *testing.T) {
		api, _ := newAPI(t)

		req := httptest.NewRequest(http.MethodDelete, "/api/v1/users/user-2/avatar", nil)
		req.Header.Set(headerUserID, testUserID)

		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("без заголовка", func(t *testing.T) {
		api, _ := newAPI(t)

		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/users/"+testUserID+"/avatar", nil))

		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("аватарки нет", func(t *testing.T) {
		api, d := newAPI(t)

		d.repo.EXPECT().GetCurrentByUserID(mock.Anything, testUserID).
			Return(nil, domain.ErrAvatarNotFound).Once()

		req := httptest.NewRequest(http.MethodDelete, "/api/v1/users/"+testUserID+"/avatar", nil)
		req.Header.Set(headerUserID, testUserID)

		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusNotFound, rec.Code)
	})
}
