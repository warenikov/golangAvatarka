package web

import (
	"bytes"
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
	"github.com/stretchr/testify/require"

	"go-avatar-service/internal/config"
	"go-avatar-service/internal/domain"
)

const testMaxUpload = 1 << 20

func testRouter(t *testing.T) http.Handler {
	t.Helper()

	cfg := &config.Config{}
	cfg.App.MaxUploadBytes = testMaxUpload
	cfg.App.AllowedMIME = []string{"image/jpeg", "image/png", "image/webp"}

	h := NewHandler(nil, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))

	r := chi.NewRouter()
	r.Route("/web", h.Routes)
	r.Handle("/static/*", StaticHandler())

	return r
}

func multipartUpload(t *testing.T, userID, fileField, fileName string, content []byte) (string, *bytes.Buffer) {
	t.Helper()

	var body bytes.Buffer
	form := multipart.NewWriter(&body)

	if userID != "" {
		require.NoError(t, form.WriteField("user_id", userID))
	}

	if fileField != "" {
		part, err := form.CreateFormFile(fileField, fileName)
		require.NoError(t, err)
		_, err = part.Write(content)
		require.NoError(t, err)
	}

	require.NoError(t, form.Close())

	return form.FormDataContentType(), &body
}

func TestUploadForm(t *testing.T) {
	rec := httptest.NewRecorder()
	testRouter(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/web/upload?user_id=user-42", nil))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/html")

	body := rec.Body.String()
	assert.Contains(t, body, `action="/web/upload"`)
	assert.Contains(t, body, `enctype="multipart/form-data"`)
	assert.Contains(t, body, `name="user_id"`)
	assert.Contains(t, body, `value="user-42"`)
	assert.Contains(t, body, "до 1 МБ")
}

func TestUploadRejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name       string
		userID     string
		fileField  string
		content    []byte
		wantStatus int
		wantText   string
	}{
		{
			name:       "нет user_id",
			fileField:  "file",
			content:    []byte("\xff\xd8\xff\xe0jpeg"),
			wantStatus: http.StatusBadRequest,
			wantText:   "Некорректный User ID",
		},
		{
			name:       "user_id с недопустимыми символами",
			userID:     "../../etc/passwd",
			fileField:  "file",
			content:    []byte("\xff\xd8\xff\xe0jpeg"),
			wantStatus: http.StatusBadRequest,
			wantText:   "Некорректный User ID",
		},
		{
			name:       "файл не приложен",
			userID:     "user-42",
			wantStatus: http.StatusBadRequest,
			wantText:   "Выберите файл",
		},
		{
			name:       "файл не изображение",
			userID:     "user-42",
			fileField:  "file",
			content:    []byte("это обычный текст, а не картинка"),
			wantStatus: http.StatusBadRequest,
			wantText:   "Неподдерживаемый формат",
		},
		{
			name:       "файл больше лимита",
			userID:     "user-42",
			fileField:  "file",
			content:    bytes.Repeat([]byte("a"), testMaxUpload+1),
			wantStatus: http.StatusRequestEntityTooLarge,
			wantText:   "Файл больше 1 МБ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contentType, body := multipartUpload(t, tt.userID, tt.fileField, "avatar.jpg", tt.content)

			req := httptest.NewRequest(http.MethodPost, "/web/upload", body)
			req.Header.Set("Content-Type", contentType)

			rec := httptest.NewRecorder()
			testRouter(t).ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Contains(t, rec.Body.String(), tt.wantText)
		})
	}
}

func TestUploadAcceptsImageFieldName(t *testing.T) {
	contentType, body := multipartUpload(t, "user-42", "image", "avatar.txt", []byte("не картинка"))

	req := httptest.NewRequest(http.MethodPost, "/web/upload", body)
	req.Header.Set("Content-Type", contentType)

	rec := httptest.NewRecorder()
	testRouter(t).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "Неподдерживаемый формат")
}

func TestGalleryLookup(t *testing.T) {
	tests := []struct {
		name         string
		query        string
		wantStatus   int
		wantLocation string
		wantText     string
	}{
		{
			name:       "без параметра показывает форму поиска",
			wantStatus: http.StatusOK,
			wantText:   `action="/web/gallery"`,
		},
		{
			name:       "недопустимый user_id",
			query:      "?user_id=" + "%D1%8E%D0%B7%D0%B5%D1%80",
			wantStatus: http.StatusBadRequest,
			wantText:   "invalid user id",
		},
		{
			name:         "корректный user_id ведёт на галерею",
			query:        "?user_id=user-42",
			wantStatus:   http.StatusSeeOther,
			wantLocation: "/web/gallery/user-42",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			testRouter(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/web/gallery"+tt.query, nil))

			assert.Equal(t, tt.wantStatus, rec.Code)

			if tt.wantLocation != "" {
				assert.Equal(t, tt.wantLocation, rec.Header().Get("Location"))
			}
			if tt.wantText != "" {
				assert.Contains(t, rec.Body.String(), tt.wantText)
			}
		})
	}
}

func TestDeleteRejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name     string
		avatarID string
		userID   string
		wantText string
	}{
		{
			name:     "пустой user_id",
			avatarID: uuid.NewString(),
			wantText: "Некорректный User ID",
		},
		{
			name:     "идентификатор аватарки не UUID",
			avatarID: "not-a-uuid",
			userID:   "user-42",
			wantText: "Некорректный идентификатор аватарки",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/web/avatars/"+tt.avatarID+"/delete",
				strings.NewReader("user_id="+tt.userID))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

			rec := httptest.NewRecorder()
			testRouter(t).ServeHTTP(rec, req)

			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, rec.Body.String(), tt.wantText)
		})
	}
}

func TestStaticHandlerServesBundledFiles(t *testing.T) {
	router := testRouter(t)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/", nil))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Avatar Upload Service")

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/default-avatar.png", nil))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "image/png", rec.Header().Get("Content-Type"))
}

func TestToGalleryItem(t *testing.T) {
	id := uuid.New()
	created := time.Date(2026, time.August, 25, 14, 30, 0, 0, time.Local)

	item := toGalleryItem(&domain.Avatar{
		ID:               id,
		UserID:           "user-42",
		FileName:         "",
		ProcessingStatus: domain.ProcessingStatusCompleted,
		CreatedAt:        created,
	})

	assert.Equal(t, id.String(), item.ID)
	assert.Equal(t, "без имени", item.FileName)
	assert.Equal(t, "/api/v1/avatars/"+id.String()+"?size=100x100", item.ThumbnailURL)
	assert.Equal(t, "/api/v1/avatars/"+id.String(), item.OriginalURL)
	assert.Equal(t, "25.08.2026 14:30", item.Created)
	assert.Equal(t, "миниатюры готовы", item.StatusLabel)
}

func TestProcessingBadge(t *testing.T) {
	tests := []struct {
		status    domain.ProcessingStatus
		wantLabel string
	}{
		{domain.ProcessingStatusPending, "в очереди"},
		{domain.ProcessingStatusProcessing, "обрабатывается"},
		{domain.ProcessingStatusCompleted, "миниатюры готовы"},
		{domain.ProcessingStatusFailed, "ошибка обработки"},
		{domain.ProcessingStatus("unknown"), "unknown"},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			label, class := processingBadge(tt.status)

			assert.Equal(t, tt.wantLabel, label)
			assert.NotEmpty(t, class)
		})
	}
}

func TestGalleryPath(t *testing.T) {
	assert.Equal(t, "/web/gallery/user-42", galleryPath("user-42"))
	assert.Equal(t, "/web/gallery/user@example.com", galleryPath("user@example.com"))
}
