package observability

import (
	"context"
	"errors"
	"fmt"
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

// AdminRoutes — обработчики проверок состояния для служебного сервера.
// Пустое поле означает, что маршрут не регистрируется.
type AdminRoutes struct {
	Live  http.HandlerFunc
	Ready http.HandlerFunc
}

// Server — служебный HTTP-сервер: метрики и проверки состояния.
//
// Его слушают оба процесса, но по разным причинам. Воркер не имеет своего API,
// и снять с него метрики было бы неоткуда. У сервера API есть, но держать
// на нём /metrics нельзя: публичный порт смотрит наружу через Ingress,
// а в метках метрик лежит внутреннее устройство сервиса.
type Server struct {
	srv *http.Server
	log *slog.Logger
}

// NewServer собирает сервер с /metrics, /livez и /readyz.
//
// /health остаётся синонимом готовности: эндпоинт описан в API с первого
// спринта, и ломать его ради переименования незачем.
func NewServer(addr string, reg *prometheus.Registry, routes AdminRoutes, log *slog.Logger) *Server {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	if routes.Live != nil {
		mux.HandleFunc("GET /livez", routes.Live)
	}

	if routes.Ready != nil {
		mux.HandleFunc("GET /readyz", routes.Ready)
		mux.HandleFunc("GET /health", routes.Ready)
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

// Run держит сервер до отмены контекста и возвращает ошибку прослушивания.
//
// Как поступить с отказом, решает вызывающий, и решение у процессов разное.
// Воркер без метрик продолжает разбирать очередь — он просто хуже наблюдаем.
// Сервер без служебного порта теряет пробы: оркестратор не может выяснить,
// жив ли он, и убивает под по стартовой проверке, хотя API работает.
func (s *Server) Run(ctx context.Context) error {
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
		return fmt.Errorf("служебный сервер недоступен: %w", err)
	}

	return nil
}

// Addr возвращает адрес прослушивания — пригодно для логов и тестов.
func (s *Server) Addr() string {
	return s.srv.Addr
}
