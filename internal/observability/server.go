package observability

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	serverReadHeaderTimeout = 5 * time.Second
	serverReadTimeout       = 15 * time.Second
	serverWriteTimeout      = 30 * time.Second
)

// Server — служебный HTTP-сервер для процессов без собственного API.
//
// Воркер до сих пор не отдавал ни метрик, ни состояния: половина показателей
// сервиса — обработка миниатюр, ретраи, отставание очереди — живёт именно
// в нём, и снять их было неоткуда.
type Server struct {
	srv *http.Server
	log *slog.Logger
}

// NewServer собирает сервер с /metrics и /health.
func NewServer(addr string, reg *prometheus.Registry, health http.HandlerFunc, log *slog.Logger) *Server {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	if health != nil {
		mux.HandleFunc("GET /health", health)
	}

	return &Server{
		srv: &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: serverReadHeaderTimeout,
			ReadTimeout:       serverReadTimeout,
			WriteTimeout:      serverWriteTimeout,
		},
		log: log,
	}
}

// Run держит сервер до отмены контекста.
//
// Отказ служебного сервера не должен ронять процесс: без метрик воркер
// работает хуже наблюдаемым, но продолжает обрабатывать очередь.
func (s *Server) Run(ctx context.Context) {
	go func() {
		<-ctx.Done()

		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), serverWriteTimeout)
		defer cancel()

		if err := s.srv.Shutdown(shutdownCtx); err != nil {
			s.log.Error("служебный сервер остановлен с ошибкой", "err", err)
		}
	}()

	s.log.InfoContext(ctx, "служебный сервер запущен", "addr", s.srv.Addr)

	if err := s.srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		s.log.ErrorContext(ctx, "служебный сервер недоступен", "err", err)
	}
}

// Addr возвращает адрес прослушивания — пригодно для логов и тестов.
func (s *Server) Addr() string {
	return s.srv.Addr
}
