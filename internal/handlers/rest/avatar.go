package rest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"go-avatar-service/internal/config"
	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/services"
	"go-avatar-service/internal/services/imageproc"
	"go-avatar-service/web"
)

const (
	headerUserID = "X-User-ID"
	cacheMaxAge  = 86400

	formFieldFile  = "file"
	formFieldImage = "image"
)

var allowedSizes = []string{domain.SizeOriginal, domain.ThumbnailSmall, domain.ThumbnailLarge}

type AvatarHandler struct {
	responder
	svc *services.AvatarService
	cfg *config.Config
}

// NewAvatarHandler создаёт обработчики публичного API аватарок.
func NewAvatarHandler(svc *services.AvatarService, cfg *config.Config, log *slog.Logger) *AvatarHandler {
	return &AvatarHandler{responder: responder{log: log}, svc: svc, cfg: cfg}
}

type uploadResponse struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	URL       string    `json:"url"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type dimensions struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type thumbnailInfo struct {
	Size string `json:"size"`
	URL  string `json:"url"`
}

type metadataResponse struct {
	ID               string          `json:"id"`
	UserID           string          `json:"user_id"`
	FileName         string          `json:"file_name"`
	MimeType         string          `json:"mime_type"`
	Size             int64           `json:"size"`
	Dimensions       *dimensions     `json:"dimensions,omitempty"`
	Thumbnails       []thumbnailInfo `json:"thumbnails"`
	ProcessingStatus string          `json:"processing_status"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

type listResponse struct {
	UserID  string             `json:"user_id"`
	Count   int                `json:"count"`
	Avatars []metadataResponse `json:"avatars"`
}

// Upload обрабатывает POST /api/v1/avatars.
func (h *AvatarHandler) Upload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userID := r.Header.Get(headerUserID)
	if err := domain.ValidateUserID(userID); err != nil {
		h.Error(ctx, w, http.StatusBadRequest, "Invalid user id", err.Error())

		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, h.cfg.App.MaxUploadBytes)

	file, header, err := openUploadedFile(r)
	if err != nil {
		h.writeUploadError(ctx, w, err)

		return
	}
	defer func() { _ = file.Close() }()

	mime, body, err := imageproc.Detect(file)
	if err != nil {
		h.writeUploadError(ctx, w, err)

		return
	}

	if err = imageproc.ValidateMIME(mime, h.cfg.App.AllowedMIME); err != nil {
		h.Error(ctx, w, http.StatusBadRequest, "Invalid file format",
			fmt.Sprintf("Supported formats: %v", h.cfg.App.AllowedMIME))

		return
	}

	avatar, err := h.svc.Upload(ctx, services.UploadInput{
		UserID:   userID,
		FileName: header.Filename,
		MimeType: mime,
		Size:     header.Size,
		Body:     body,
	})
	if err != nil {
		h.writeUploadError(ctx, w, err)

		return
	}

	h.JSON(ctx, w, http.StatusCreated, uploadResponse{
		ID:        avatar.ID.String(),
		UserID:    avatar.UserID,
		URL:       "/api/v1/avatars/" + avatar.ID.String(),
		Status:    "processing",
		CreatedAt: avatar.CreatedAt,
	})
}

func openUploadedFile(r *http.Request) (io.ReadCloser, *multipartHeader, error) {
	file, header, err := r.FormFile(formFieldFile)
	if errors.Is(err, http.ErrMissingFile) {
		file, header, err = r.FormFile(formFieldImage)
	}
	if err != nil {
		return nil, nil, err
	}

	return file, &multipartHeader{Filename: header.Filename, Size: header.Size}, nil
}

type multipartHeader struct {
	Filename string
	Size     int64
}

func (h *AvatarHandler) writeUploadError(ctx context.Context, w http.ResponseWriter, err error) {
	var maxBytes *http.MaxBytesError

	switch {
	case errors.As(err, &maxBytes), errors.Is(err, domain.ErrTooLarge):
		h.JSON(ctx, w, http.StatusRequestEntityTooLarge, map[string]any{
			"error":    "File too large",
			"max_size": h.cfg.App.MaxUploadBytes,
		})
	case errors.Is(err, http.ErrMissingFile):
		h.Error(ctx, w, http.StatusBadRequest, "File is required",
			fmt.Sprintf("Send the image in form field %q or %q", formFieldFile, formFieldImage))
	case errors.Is(err, domain.ErrInvalidUserID), errors.Is(err, domain.ErrInvalidFormat):
		h.Error(ctx, w, http.StatusBadRequest, "Invalid request", err.Error())
	default:
		h.log.ErrorContext(ctx, "загрузка аватарки", "err", err)
		h.Error(ctx, w, http.StatusInternalServerError, "Internal error", "")
	}
}

// Get обрабатывает GET /api/v1/avatars/{avatar_id}.
func (h *AvatarHandler) Get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	id, ok := h.parseID(ctx, w, r)
	if !ok {
		return
	}

	size, ok := h.parseSize(ctx, w, r)
	if !ok {
		return
	}

	avatar, obj, err := h.svc.Get(ctx, id, size)
	if err != nil {
		h.writeReadError(ctx, w, err)

		return
	}
	defer func() { _ = obj.Body.Close() }()

	h.streamContent(ctx, w, r, avatar, obj, size, true)
}

// GetCurrent обрабатывает GET /api/v1/users/{user_id}/avatar.
// Пользователю без аватарки отдаётся стандартное изображение-заглушка.
func (h *AvatarHandler) GetCurrent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userID := chi.URLParam(r, "user_id")
	size, ok := h.parseSize(ctx, w, r)
	if !ok {
		return
	}

	avatar, obj, err := h.svc.GetCurrent(ctx, userID, size)
	if errors.Is(err, domain.ErrAvatarNotFound) || errors.Is(err, domain.ErrInvalidUserID) {
		h.writeFallback(ctx, w)

		return
	}
	if err != nil {
		h.writeReadError(ctx, w, err)

		return
	}
	defer func() { _ = obj.Body.Close() }()

	h.streamContent(ctx, w, r, avatar, obj, size, true)
}

// Metadata обрабатывает GET /api/v1/avatars/{avatar_id}/metadata.
func (h *AvatarHandler) Metadata(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	id, ok := h.parseID(ctx, w, r)
	if !ok {
		return
	}

	avatar, err := h.svc.Metadata(ctx, id)
	if err != nil {
		h.writeReadError(ctx, w, err)

		return
	}

	h.JSON(ctx, w, http.StatusOK, toMetadata(avatar))
}

// List обрабатывает GET /api/v1/users/{user_id}/avatars.
func (h *AvatarHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userID := chi.URLParam(r, "user_id")

	avatars, err := h.svc.List(ctx, userID)
	if err != nil {
		h.writeReadError(ctx, w, err)

		return
	}

	items := make([]metadataResponse, 0, len(avatars))
	for i := range avatars {
		items = append(items, toMetadata(&avatars[i]))
	}

	h.JSON(ctx, w, http.StatusOK, listResponse{UserID: userID, Count: len(items), Avatars: items})
}

// Delete обрабатывает DELETE /api/v1/avatars/{avatar_id}.
func (h *AvatarHandler) Delete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userID := r.Header.Get(headerUserID)
	if err := domain.ValidateUserID(userID); err != nil {
		h.Error(ctx, w, http.StatusBadRequest, "Invalid user id", err.Error())

		return
	}

	id, ok := h.parseID(ctx, w, r)
	if !ok {
		return
	}

	if err := h.svc.Delete(ctx, id, userID); err != nil {
		h.writeReadError(ctx, w, err)

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// DeleteCurrent обрабатывает DELETE /api/v1/users/{user_id}/avatar.
func (h *AvatarHandler) DeleteCurrent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userID := r.Header.Get(headerUserID)
	if err := domain.ValidateUserID(userID); err != nil {
		h.Error(ctx, w, http.StatusBadRequest, "Invalid user id", err.Error())

		return
	}

	if pathUser := chi.URLParam(r, "user_id"); pathUser != userID {
		h.Error(ctx, w, http.StatusForbidden, "Forbidden", "You can only delete your own avatars")

		return
	}

	if err := h.svc.DeleteCurrent(ctx, userID); err != nil {
		h.writeReadError(ctx, w, err)

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *AvatarHandler) streamContent(
	ctx context.Context, w http.ResponseWriter, r *http.Request,
	avatar *domain.Avatar, obj *domain.Object, size string, cacheable bool,
) {
	etag := contentETag(avatar, size)

	if match := r.Header.Get("If-None-Match"); match == etag {
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusNotModified)

		return
	}

	contentType := obj.ContentType
	if contentType == "" {
		contentType = avatar.MimeType
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", obj.Size))
	w.Header().Set("ETag", etag)

	if cacheable {
		w.Header().Set("Cache-Control", fmt.Sprintf("max-age=%d", cacheMaxAge))
	}

	if _, err := io.Copy(w, obj.Body); err != nil {
		h.log.ErrorContext(ctx, "отдача содержимого аватарки", "avatar_id", avatar.ID, "err", err)
	}
}

// writeFallback отдаёт изображение-заглушку. Оно намеренно не кешируется:
// иначе клиент не увидит аватарку, появившуюся позже.
func (h *AvatarHandler) writeFallback(ctx context.Context, w http.ResponseWriter) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(web.DefaultAvatar)))
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Avatar-Fallback", "true")

	if _, err := w.Write(web.DefaultAvatar); err != nil {
		h.log.ErrorContext(ctx, "отдача заглушки", "err", err)
	}
}

func (h *AvatarHandler) parseID(ctx context.Context, w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "avatar_id"))
	if err != nil {
		h.Error(ctx, w, http.StatusBadRequest, "Invalid avatar id", "Expected UUID")

		return uuid.Nil, false
	}

	return id, true
}

func (h *AvatarHandler) parseSize(ctx context.Context, w http.ResponseWriter, r *http.Request) (string, bool) {
	if format := r.URL.Query().Get("format"); format != "" && format != "jpeg" && format != "png" {
		h.Error(ctx, w, http.StatusBadRequest, "Unsupported format",
			"Only jpeg and png are supported; webp encoding is not available")

		return "", false
	}

	size := r.URL.Query().Get("size")
	if size == "" {
		return domain.SizeOriginal, true
	}

	if !slices.Contains(allowedSizes, size) {
		h.Error(ctx, w, http.StatusBadRequest, "Invalid size",
			fmt.Sprintf("Supported sizes: %v", allowedSizes))

		return "", false
	}

	return size, true
}

func (h *AvatarHandler) writeReadError(ctx context.Context, w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrAvatarNotFound), errors.Is(err, domain.ErrObjectNotFound):
		h.Error(ctx, w, http.StatusNotFound, "Avatar not found", "")
	case errors.Is(err, domain.ErrForbidden):
		h.Error(ctx, w, http.StatusForbidden, "Forbidden", "You can only delete your own avatars")
	case errors.Is(err, domain.ErrInvalidUserID):
		h.Error(ctx, w, http.StatusBadRequest, "Invalid user id", err.Error())
	default:
		h.log.ErrorContext(ctx, "обработка запроса аватарки", "err", err)
		h.Error(ctx, w, http.StatusInternalServerError, "Internal error", "")
	}
}

func toMetadata(a *domain.Avatar) metadataResponse {
	resp := metadataResponse{
		ID:               a.ID.String(),
		UserID:           a.UserID,
		FileName:         a.FileName,
		MimeType:         a.MimeType,
		Size:             a.SizeBytes,
		Thumbnails:       make([]thumbnailInfo, 0, len(a.ThumbnailS3Keys)),
		ProcessingStatus: string(a.ProcessingStatus),
		CreatedAt:        a.CreatedAt,
		UpdatedAt:        a.UpdatedAt,
	}

	if a.Width != nil && a.Height != nil {
		resp.Dimensions = &dimensions{Width: *a.Width, Height: *a.Height}
	}

	for _, size := range []string{domain.ThumbnailSmall, domain.ThumbnailLarge} {
		if _, ok := a.ThumbnailS3Keys[size]; ok {
			resp.Thumbnails = append(resp.Thumbnails, thumbnailInfo{
				Size: size,
				URL:  fmt.Sprintf("/api/v1/avatars/%s?size=%s", a.ID, size),
			})
		}
	}

	return resp
}

func contentETag(a *domain.Avatar, size string) string {
	sum := sha256.Sum256([]byte(a.ID.String() + "|" + size + "|" + a.UpdatedAt.UTC().String()))

	return `"` + hex.EncodeToString(sum[:])[:16] + `"`
}
