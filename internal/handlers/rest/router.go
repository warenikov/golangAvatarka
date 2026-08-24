package rest

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"go-avatar-service/internal/config"
	"go-avatar-service/internal/observability"
)

type RouterDeps struct {
	Config   *config.Config
	Log      *slog.Logger
	Metrics  *observability.HTTP
	Registry *prometheus.Registry
	Checkers []Checker
}

// NewRouter собирает middleware и маршруты HTTP-сервера.
func NewRouter(deps RouterDeps) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(Recoverer(deps.Log))
	r.Use(RequestLogger(deps.Log))
	r.Use(Metrics(deps.Metrics))
	r.Use(middleware.Timeout(deps.Config.App.RequestTimeout))

	health := NewHealthHandler(deps.Log, deps.Config.App.Version, healthTimeout, deps.Checkers...)
	r.Get("/health", health.Handle)
	r.Method(http.MethodGet, "/metrics", promhttp.HandlerFor(deps.Registry, promhttp.HandlerOpts{}))

	return r
}
