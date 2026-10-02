package rest

import (
	"context"
	"log/slog"
	"net/http"

	openapi "go-avatar-service/api"
	"go-avatar-service/web"
)

// docsCacheControl — час. Спецификация меняется вместе с выкатом, а не чаще,
// но кешировать её на сутки значило бы показывать вчерашний контракт
// после обновления сервиса.
const docsCacheControl = "public, max-age=3600"

// OpenAPIHandler отдаёт спецификацию OpenAPI.
//
// Отдельный эндпоинт, а не файл в статике: спецификация — часть контракта,
// и её адрес должен быть таким же стабильным, как адреса самих методов.
// Генераторы клиентов и линтеры контрактов ходят именно сюда.
func OpenAPIHandler(log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "application/yaml; charset=utf-8")
		h.Set("Cache-Control", docsCacheControl)

		writeBody(r.Context(), w, log, openapi.Spec)
	}
}

// DocsHandler отдаёт страницу просмотра спецификации.
func DocsHandler(log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Cache-Control", docsCacheControl)

		writeBody(r.Context(), w, log, web.SwaggerPage)
	}
}

// writeBody отправляет готовое тело и сообщает о разрыве соединения.
//
// Ошибку записи здесь уже не превратить в ответ — заголовки ушли клиенту, —
// но и глотать её нельзя: череда обрывов на отдаче означает проблему в сети
// или в прокси, и без записи в лог она ничем себя не проявит.
func writeBody(ctx context.Context, w http.ResponseWriter, log *slog.Logger, body []byte) {
	if _, err := w.Write(body); err != nil {
		log.ErrorContext(ctx, "тело ответа не отправлено", "err", err)
	}
}
