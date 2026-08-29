package web

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	"go-avatar-service/internal/handlers/form"
	"go-avatar-service/internal/services"
)

const webUserID = "web-user"

type webDeps struct {
	repo      *MockAvatarRepository
	storage   *MockObjectStorage
	publisher *MockEventPublisher
}

// serviceRouter собирает веб-интерфейс поверх настоящего сервиса с мокнутыми портами.
func serviceRouter(t *testing.T) (http.Handler, webDeps) {
	t.Helper()

	d := webDeps{
		repo:      NewMockAvatarRepository(t),
		storage:   NewMockObjectStorage(t),
		publisher: NewMockEventPublisher(t),
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	cfg := &config.Config{}
	cfg.App.MaxUploadBytes = testMaxUpload
	cfg.App.AllowedMIME = []string{"image/jpeg", "image/png", "image/webp"}

	h := NewHandler(services.NewAvatarService(d.repo, d.storage, d.publisher, log), cfg, log)

	r := chi.NewRouter()
	r.Route("/web", h.Routes)

	return r, d
}

func webPNG(t *testing.T) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 50, 30))
	for y := range 30 {
		for x := range 50 {
			img.Set(x, y, color.RGBA{R: uint8(x), G: 40, B: uint8(y), A: 255})
		}
	}

	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))

	return buf.Bytes()
}

func galleryAvatar(id uuid.UUID, status domain.ProcessingStatus) domain.Avatar {
	return domain.Avatar{
		ID: id, UserID: webUserID, FileName: "avatar.png",
		S3Key:            domain.OriginalObjectKey(webUserID, id),
		ThumbnailS3Keys:  domain.ThumbnailKeys(webUserID, id),
		ProcessingStatus: status,
		CreatedAt:        time.Date(2026, 8, 27, 12, 30, 0, 0, time.UTC),
	}
}

func TestUploadRedirectsToGallery(t *testing.T) {
	router, d := serviceRouter(t)

	d.storage.EXPECT().Put(mock.Anything, mock.Anything, mock.Anything, mock.Anything, "image/png").
		Return(nil).Once()
	d.repo.EXPECT().Create(mock.Anything, mock.Anything).Return(nil).Once()
	d.publisher.EXPECT().PublishUpload(mock.Anything, mock.Anything).Return(nil).Once()

	contentType, body := multipartUpload(t, webUserID, form.FieldFile, "avatar.png", webPNG(t))

	req := httptest.NewRequest(http.MethodPost, "/web/upload", body)
	req.Header.Set("Content-Type", contentType)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	assert.Equal(t, "/web/gallery/"+webUserID, rec.Header().Get("Location"))
}

// Идентификатор пользователя приходит из формы и попадает в путь галереи.
// От подстановки в путь защищает не экранирование, а ValidateUserID:
// url.PathEscape не меняет ни одного символа из разрешённого набора
// (A-Za-z0-9._@+-), проверено перебором. Поэтому здесь ожидается точный
// литерал, а не результат того же PathEscape, что вызывает обработчик.
func TestUploadRedirectPreservesAllowedUserID(t *testing.T) {
	router, d := serviceRouter(t)

	userID := "user+tag@example.com"

	d.storage.EXPECT().Put(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil).Once()
	d.repo.EXPECT().Create(mock.Anything, mock.Anything).Return(nil).Once()
	d.publisher.EXPECT().PublishUpload(mock.Anything, mock.Anything).Return(nil).Once()

	contentType, body := multipartUpload(t, userID, form.FieldFile, "avatar.png", webPNG(t))

	req := httptest.NewRequest(http.MethodPost, "/web/upload", body)
	req.Header.Set("Content-Type", contentType)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, "/web/gallery/user+tag@example.com", rec.Header().Get("Location"))
}

// Настоящая защита пути: идентификатор, способный увести редирект в чужой
// каталог, отбивается валидацией и до galleryPath не доходит.
func TestUploadRejectsPathBreakingUserID(t *testing.T) {
	tests := []string{"../other", "user/../root", "user/nested", "bad user"}

	for _, userID := range tests {
		t.Run(userID, func(t *testing.T) {
			router, _ := serviceRouter(t)

			contentType, body := multipartUpload(t, userID, form.FieldFile, "avatar.png", webPNG(t))

			req := httptest.NewRequest(http.MethodPost, "/web/upload", body)
			req.Header.Set("Content-Type", contentType)

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Empty(t, rec.Header().Get("Location"), "редиректа быть не должно")
			assert.Contains(t, rec.Body.String(), "Некорректный User ID")
		})
	}
}

func TestUploadShowsServiceFailure(t *testing.T) {
	router, d := serviceRouter(t)

	d.storage.EXPECT().Put(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(assert.AnError).Once()

	contentType, body := multipartUpload(t, webUserID, form.FieldFile, "avatar.png", webPNG(t))

	req := httptest.NewRequest(http.MethodPost, "/web/upload", body)
	req.Header.Set("Content-Type", contentType)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "Не удалось загрузить аватарку")
	assert.NotContains(t, rec.Body.String(), assert.AnError.Error())
	assert.Contains(t, rec.Body.String(), `value="`+webUserID+`"`, "форма должна сохранить введённый User ID")
}

func TestGalleryShowsAvatars(t *testing.T) {
	router, d := serviceRouter(t)

	done := galleryAvatar(uuid.New(), domain.ProcessingStatusCompleted)
	pending := galleryAvatar(uuid.New(), domain.ProcessingStatusPending)

	d.repo.EXPECT().ListByUserID(mock.Anything, webUserID).
		Return([]domain.Avatar{done, pending}, nil).Once()

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/web/gallery/"+webUserID, nil))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/html")

	page := rec.Body.String()
	assert.Contains(t, page, "/api/v1/avatars/"+done.ID.String()+"?size=100x100")
	assert.Contains(t, page, "/api/v1/avatars/"+pending.ID.String())
	assert.Contains(t, page, "миниатюры готовы")
	assert.Contains(t, page, "в очереди")
	assert.Contains(t, page, `action="/web/avatars/`+done.ID.String()+`/delete"`)
}

func TestGalleryEmpty(t *testing.T) {
	router, d := serviceRouter(t)

	d.repo.EXPECT().ListByUserID(mock.Anything, webUserID).Return(nil, nil).Once()

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/web/gallery/"+webUserID, nil))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "/api/v1/avatars/")
}

func TestGalleryInvalidUserID(t *testing.T) {
	router, _ := serviceRouter(t)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/web/gallery/bad%20user", nil))

	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid user id")
}

func TestGalleryRepositoryFailure(t *testing.T) {
	router, d := serviceRouter(t)

	d.repo.EXPECT().ListByUserID(mock.Anything, webUserID).Return(nil, assert.AnError).Once()

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/web/gallery/"+webUserID, nil))

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "Не удалось загрузить галерею")
}

func TestDeleteFromGallery(t *testing.T) {
	id := uuid.New()

	tests := []struct {
		name       string
		arrange    func(webDeps)
		wantStatus int
		wantBody   string
	}{
		{
			name: "своя аватарка удаляется",
			arrange: func(d webDeps) {
				d.repo.EXPECT().SoftDelete(mock.Anything, id, webUserID).Return([]string{"original"}, nil).Once()
				d.publisher.EXPECT().PublishDelete(mock.Anything, mock.Anything).Return(nil).Once()
			},
			wantStatus: http.StatusSeeOther,
		},
		{
			name: "чужая аватарка",
			arrange: func(d webDeps) {
				d.repo.EXPECT().SoftDelete(mock.Anything, id, webUserID).Return(nil, domain.ErrForbidden).Once()
			},
			wantStatus: http.StatusForbidden,
			wantBody:   "Чужую аватарку удалить нельзя",
		},
		{
			name: "аватарки уже нет",
			arrange: func(d webDeps) {
				d.repo.EXPECT().SoftDelete(mock.Anything, id, webUserID).Return(nil, domain.ErrAvatarNotFound).Once()
			},
			wantStatus: http.StatusNotFound,
			wantBody:   "Аватарка не найдена",
		},
		{
			name: "база недоступна",
			arrange: func(d webDeps) {
				d.repo.EXPECT().SoftDelete(mock.Anything, id, webUserID).Return(nil, assert.AnError).Once()
			},
			wantStatus: http.StatusInternalServerError,
			wantBody:   "Не удалось удалить аватарку",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router, d := serviceRouter(t)
			tt.arrange(d)

			form := url.Values{formFieldUserID: {webUserID}}
			req := httptest.NewRequest(http.MethodPost, "/web/avatars/"+id.String()+"/delete",
				strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			require.Equal(t, tt.wantStatus, rec.Code, rec.Body.String())

			if tt.wantStatus == http.StatusSeeOther {
				assert.Equal(t, "/web/gallery/"+webUserID, rec.Header().Get("Location"))

				return
			}

			assert.Contains(t, rec.Body.String(), tt.wantBody)
		})
	}
}
