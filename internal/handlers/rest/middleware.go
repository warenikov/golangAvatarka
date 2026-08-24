package rest

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

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
