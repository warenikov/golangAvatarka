package rest

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"

	"go-avatar-service/internal/observability"
)

// RequestLogger пишет в лог результат каждого обработанного запроса.
func RequestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			started := time.Now()

			defer func() {
				log.InfoContext(r.Context(), "запрос обработан",
					"method", r.Method,
					"path", r.URL.Path,
					"status", ww.Status(),
					"bytes", ww.BytesWritten(),
					"duration_ms", time.Since(started).Milliseconds(),
					"request_id", middleware.GetReqID(r.Context()),
					"remote_addr", r.RemoteAddr,
				)
			}()

			next.ServeHTTP(ww, r)
		})
	}
}

// Recoverer перехватывает панику в обработчике и отвечает кодом 500.
func Recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	rs := responder{log: log}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				if recErr, ok := rec.(error); ok && errors.Is(recErr, http.ErrAbortHandler) {
					panic(rec)
				}

				log.ErrorContext(r.Context(), "паника в обработчике",
					"panic", rec,
					"method", r.Method,
					"path", r.URL.Path,
					"request_id", middleware.GetReqID(r.Context()),
				)
				rs.Error(r.Context(), w, http.StatusInternalServerError, "Internal error", "")
			}()

			next.ServeHTTP(w, r)
		})
	}
}

// Metrics учитывает количество, длительность и параллелизм запросов.
func Metrics(m *observability.HTTP) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			started := time.Now()

			m.InFlight.Inc()
			defer m.InFlight.Dec()

			next.ServeHTTP(ww, r)

			route := chi.RouteContext(r.Context()).RoutePattern()
			if route == "" {
				route = "unmatched"
			}

			m.Requests.WithLabelValues(r.Method, route, strconv.Itoa(ww.Status())).Inc()
			m.Duration.WithLabelValues(r.Method, route).Observe(time.Since(started).Seconds())
		})
	}
}

// SecurityHeaders проставляет заголовки, ограничивающие поведение браузера.
//
// nosniff здесь не формальность: сервис отдаёт файлы, загруженные кем угодно,
// а Content-Type определяется по сигнатуре. Файл, у которого начало проходит
// как PNG, а дальше лежит разметка, при сниффинге мог бы быть отрисован как
// HTML с нашего же origin — это хранимая XSS. nosniff обязывает браузер
// доверять заявленному типу.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")

		next.ServeHTTP(w, r)
	})
}

// clientIPKey возвращает ключ лимита по адресу, с которого открыто соединение.
//
// Заголовки X-Forwarded-For и подобные намеренно не читаются: доверенного
// прокси перед сервисом нет, а любой клиент может проставить их сам и получать
// новое ведро лимита на каждый запрос. Адрес берётся из RemoteAddr через
// middleware.ClientIPFromRemoteAddr, CanonicalizeIP сводит IPv6 к /64 —
// иначе клиент ротирует адреса внутри своей подсети и обходит лимит.
func clientIPKey(r *http.Request) (string, error) {
	return httprate.CanonicalizeIP(middleware.GetClientIP(r.Context())), nil
}

// userKey возвращает ключ лимита по идентификатору пользователя.
//
// X-User-ID никем не подтверждён, поэтому лимит по нему обходится подстановкой
// чужого значения — это ограничение честно принимается. Он нужен не как защита
// от злоумышленника, а как справедливость между пользователями за одним NAT;
// связывающим остаётся лимит по адресу, который стоит в цепочке раньше и не
// даёт раздуть таблицу счётчиков произвольными идентификаторами.
// Без заголовка ключом становится адрес, иначе все анонимные запросы делили бы
// одно ведро на всех.
func userKey(r *http.Request) (string, error) {
	if userID := r.Header.Get(headerUserID); userID != "" {
		return "user:" + userID, nil
	}

	return clientIPKey(r)
}

// rateLimiter собирает middleware ограничения частоты с ответом в формате API.
func rateLimiter(log *slog.Logger, requestsPerMinute int, keyFn httprate.KeyFunc) func(http.Handler) http.Handler {
	rs := responder{log: log}

	return httprate.LimitBy(requestsPerMinute, time.Minute, keyFn,
		httprate.WithLimitHandler(func(w http.ResponseWriter, r *http.Request) {
			rs.Error(r.Context(), w, http.StatusTooManyRequests, "Too many requests",
				fmt.Sprintf("Limit is %d requests per minute", requestsPerMinute))
		}),
	)
}

// UploadRateLimiter возвращает ограничитель для операций загрузки.
// Собирается один раз и вешается и на REST, и на веб-форму: это одна и та же
// дорогая операция, и общий счётчик не даёт обойти лимит сменой точки входа.
func UploadRateLimiter(log *slog.Logger, requestsPerMinute int) func(http.Handler) http.Handler {
	return rateLimiter(log, requestsPerMinute, clientIPKey)
}
