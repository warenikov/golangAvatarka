package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"go-avatar-service/internal/config"
	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/handlers/form"
	"go-avatar-service/internal/services"
)

const (
	formFieldUserID = "user_id"

	multipartMemory = 4 << 20
	bytesInMB       = 1 << 20
	createdLayout   = "02.01.2006 15:04"
)

type Handler struct {
	svc      *services.AvatarService
	cfg      *config.Config
	log      *slog.Logger
	uploadMW []func(http.Handler) http.Handler
}

// NewHandler создаёт обработчики страниц веб-интерфейса.
// Необязательные uploadMW навешиваются только на приём формы загрузки:
// она дороже остальных страниц и ограничивается отдельно от них.
func NewHandler(
	svc *services.AvatarService, cfg *config.Config, log *slog.Logger,
	uploadMW ...func(http.Handler) http.Handler,
) *Handler {
	return &Handler{svc: svc, cfg: cfg, log: log, uploadMW: uploadMW}
}

// Routes регистрирует страницы веб-интерфейса на переданном роутере.
func (h *Handler) Routes(r chi.Router) {
	r.Get("/upload", h.UploadForm)
	r.With(h.uploadMW...).Post("/upload", h.Upload)
	r.Get("/gallery", h.GalleryLookup)
	r.Get("/gallery/{user_id}", h.Gallery)
	r.Post("/avatars/{avatar_id}/delete", h.Delete)
}

// UploadForm обрабатывает GET /web/upload.
func (h *Handler) UploadForm(w http.ResponseWriter, r *http.Request) {
	h.render(r.Context(), w, uploadPage, http.StatusOK, h.uploadData(r.URL.Query().Get(formFieldUserID), ""))
}

// Upload обрабатывает POST /web/upload и уводит на галерею пользователя.
func (h *Handler) Upload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	r.Body = http.MaxBytesReader(w, r.Body, h.cfg.App.MaxUploadBytes)

	// security(G120): разбор формы ограничен — MaxBytesReader строкой выше
	// режет тело на APP_MAX_UPLOAD_BYTES, а multipartMemory задаёт порог,
	// после которого части уходят во временные файлы (их удаляет net/http
	// по завершении запроса).
	// #nosec G120 -- тело запроса ограничено MaxBytesReader выше
	if err := r.ParseMultipartForm(multipartMemory); err != nil {
		h.renderUploadError(ctx, w, "", err)

		return
	}

	userID := r.PostFormValue(formFieldUserID)
	if err := domain.ValidateUserID(userID); err != nil {
		h.renderUploadError(ctx, w, userID, err)

		return
	}

	image, err := form.ReadImage(r, h.cfg.App.AllowedMIME)
	if err != nil {
		h.renderUploadError(ctx, w, userID, err)

		return
	}
	defer func() { _ = image.Close() }()

	_, err = h.svc.Upload(ctx, services.UploadInput{
		UserID:   userID,
		FileName: image.Name,
		MimeType: image.MIME,
		Size:     image.Size,
		Body:     image.Body,
	})
	if err != nil {
		h.renderUploadError(ctx, w, userID, err)

		return
	}

	// #nosec G710 -- userID прошёл domain.ValidateUserID, см. galleryPath
	http.Redirect(w, r, galleryPath(userID), http.StatusSeeOther)
}

// GalleryLookup обрабатывает GET /web/gallery: без параметра показывает форму
// поиска, с корректным user_id — уводит на галерею пользователя.
func (h *Handler) GalleryLookup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userID := r.URL.Query().Get(formFieldUserID)
	if userID == "" {
		h.render(ctx, w, galleryPage, http.StatusOK, galleryPageData{})

		return
	}

	if err := domain.ValidateUserID(userID); err != nil {
		h.render(ctx, w, galleryPage, http.StatusBadRequest, galleryPageData{Error: err.Error()})

		return
	}

	// #nosec G710 -- userID прошёл domain.ValidateUserID, см. galleryPath
	http.Redirect(w, r, galleryPath(userID), http.StatusSeeOther)
}

// Gallery обрабатывает GET /web/gallery/{user_id}.
func (h *Handler) Gallery(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userID := chi.URLParam(r, "user_id")

	avatars, err := h.svc.List(ctx, userID)
	if err != nil {
		h.renderListError(ctx, w, err)

		return
	}

	items := make([]galleryItem, 0, len(avatars))
	for i := range avatars {
		items = append(items, toGalleryItem(&avatars[i]))
	}

	h.render(ctx, w, galleryPage, http.StatusOK, galleryPageData{
		UserID:  userID,
		Count:   len(items),
		Avatars: items,
	})
}

// Delete обрабатывает POST /web/avatars/{avatar_id}/delete и возвращает на галерею.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userID := r.PostFormValue(formFieldUserID)
	if err := domain.ValidateUserID(userID); err != nil {
		h.renderError(ctx, w, http.StatusBadRequest, "Некорректный User ID", err.Error())

		return
	}

	id, err := uuid.Parse(chi.URLParam(r, "avatar_id"))
	if err != nil {
		h.renderError(ctx, w, http.StatusBadRequest, "Некорректный идентификатор аватарки", "Ожидается UUID")

		return
	}

	if err = h.svc.Delete(ctx, id, userID); err != nil {
		h.renderDeleteError(ctx, w, err)

		return
	}

	// #nosec G710 -- userID прошёл domain.ValidateUserID, см. galleryPath
	http.Redirect(w, r, galleryPath(userID), http.StatusSeeOther)
}

func (h *Handler) uploadData(userID, message string) uploadPageData {
	return uploadPageData{
		UserID:      userID,
		MaxUploadMB: h.cfg.App.MaxUploadBytes / bytesInMB,
		Error:       message,
	}
}

func (h *Handler) renderUploadError(ctx context.Context, w http.ResponseWriter, userID string, err error) {
	var maxBytes *http.MaxBytesError

	var message string

	status := http.StatusBadRequest

	switch {
	case errors.As(err, &maxBytes), errors.Is(err, domain.ErrTooLarge):
		status = http.StatusRequestEntityTooLarge
		message = fmt.Sprintf("Файл больше %d МБ", h.cfg.App.MaxUploadBytes/bytesInMB)
	case errors.Is(err, http.ErrMissingFile):
		message = "Выберите файл изображения"
	case errors.Is(err, domain.ErrInvalidUserID):
		message = "Некорректный User ID: " + err.Error()
	case errors.Is(err, domain.ErrInvalidFormat):
		message = fmt.Sprintf("Неподдерживаемый формат. Разрешены: %v", h.cfg.App.AllowedMIME)
	default:
		h.log.ErrorContext(ctx, "загрузка аватарки через веб-форму", "err", err)

		status = http.StatusInternalServerError
		message = "Не удалось загрузить аватарку, попробуйте ещё раз"
	}

	h.render(ctx, w, uploadPage, status, h.uploadData(userID, message))
}

func (h *Handler) renderListError(ctx context.Context, w http.ResponseWriter, err error) {
	if errors.Is(err, domain.ErrInvalidUserID) {
		h.render(ctx, w, galleryPage, http.StatusBadRequest, galleryPageData{Error: err.Error()})

		return
	}

	h.log.ErrorContext(ctx, "список аватарок для галереи", "err", err)
	h.renderError(ctx, w, http.StatusInternalServerError, "Не удалось загрузить галерею", "")
}

func (h *Handler) renderDeleteError(ctx context.Context, w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrAvatarNotFound):
		h.renderError(ctx, w, http.StatusNotFound, "Аватарка не найдена", "")
	case errors.Is(err, domain.ErrForbidden):
		h.renderError(ctx, w, http.StatusForbidden, "Чужую аватарку удалить нельзя", "")
	default:
		h.log.ErrorContext(ctx, "удаление аватарки через веб-форму", "err", err)
		h.renderError(ctx, w, http.StatusInternalServerError, "Не удалось удалить аватарку", "")
	}
}

// galleryPath собирает путь галереи пользователя.
//
// security(G710): открытого редиректа здесь нет, хотя анализатор помечает
// userID как значение из запроса. Все вызывающие сначала прогоняют его через
// domain.ValidateUserID, который допускает только [A-Za-z0-9._@+-]: ни "/",
// ни ":", ни "\\" в значение не попадут, поэтому результат всегда остаётся
// относительным путём внутри своего origin и не может стать "//host" или
// "https://host". Уберут валидацию — редирект станет уводимым наружу.
func galleryPath(userID string) string {
	return "/web/gallery/" + url.PathEscape(userID)
}

func toGalleryItem(a *domain.Avatar) galleryItem {
	fileName := a.FileName
	if fileName == "" {
		fileName = "без имени"
	}

	label, class := processingBadge(a.ProcessingStatus)

	return galleryItem{
		ID:           a.ID.String(),
		FileName:     fileName,
		ThumbnailURL: fmt.Sprintf("/api/v1/avatars/%s?size=%s", a.ID, domain.ThumbnailSmall),
		OriginalURL:  "/api/v1/avatars/" + a.ID.String(),
		Created:      a.CreatedAt.Local().Format(createdLayout),
		StatusLabel:  label,
		StatusClass:  class,
	}
}

func processingBadge(status domain.ProcessingStatus) (label, class string) {
	switch status {
	case domain.ProcessingStatusCompleted:
		return "миниатюры готовы", "bg-green-500/20 text-green-200"
	case domain.ProcessingStatusFailed:
		return "ошибка обработки", "bg-red-500/20 text-red-200"
	case domain.ProcessingStatusProcessing:
		return "обрабатывается", "bg-blue-500/20 text-blue-200"
	case domain.ProcessingStatusPending:
		return "в очереди", "bg-yellow-500/20 text-yellow-200"
	default:
		return string(status), "bg-white/10 text-white/70"
	}
}
