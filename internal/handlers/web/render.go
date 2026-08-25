// Package web отдаёт страницы веб-интерфейса, отрендеренные на сервере.
package web

import (
	"bytes"
	"context"
	"html/template"
	"io/fs"
	"net/http"

	assets "go-avatar-service/web"
)

const layoutTemplate = "layout"

var (
	uploadPage  = mustParsePage("upload.gohtml")
	galleryPage = mustParsePage("gallery.gohtml")
	errorPage   = mustParsePage("error.gohtml")

	staticFiles = http.FileServer(http.FS(mustSubFS(assets.StaticFS, "static")))
)

type uploadPageData struct {
	UserID      string
	MaxUploadMB int64
	Error       string
}

type galleryPageData struct {
	UserID  string
	Count   int
	Avatars []galleryItem
	Error   string
}

type galleryItem struct {
	ID           string
	FileName     string
	ThumbnailURL string
	OriginalURL  string
	Created      string
	StatusLabel  string
	StatusClass  string
}

type errorPageData struct {
	Status  int
	Message string
	Details string
}

// StaticHandler отдаёт файлы из web/static, вшитые в бинарь.
func StaticHandler() http.Handler {
	return http.StripPrefix("/static/", staticFiles)
}

// render собирает страницу в буфер и только затем отправляет её клиенту.
func (h *Handler) render(
	ctx context.Context, w http.ResponseWriter, page *template.Template, status int, data any,
) {
	var buf bytes.Buffer

	if err := page.ExecuteTemplate(&buf, layoutTemplate, data); err != nil {
		h.log.ErrorContext(ctx, "рендеринг страницы", "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)

	if _, err := buf.WriteTo(w); err != nil {
		h.log.ErrorContext(ctx, "отправка страницы", "err", err)
	}
}

func (h *Handler) renderError(ctx context.Context, w http.ResponseWriter, status int, message, details string) {
	h.render(ctx, w, errorPage, status, errorPageData{Status: status, Message: message, Details: details})
}

func mustParsePage(name string) *template.Template {
	return template.Must(template.ParseFS(assets.TemplatesFS,
		"templates/layout.gohtml", "templates/"+name))
}

func mustSubFS(source fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(source, dir)
	if err != nil {
		panic(err)
	}

	return sub
}
