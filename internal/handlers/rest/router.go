package rest

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"go-avatar-service/internal/config"
	webui "go-avatar-service/internal/handlers/web"
	"go-avatar-service/internal/observability"
)

type RouterDeps struct {
	Config   *config.Config
	Log      *slog.Logger
	Metrics  *observability.HTTP
	Registry *prometheus.Registry
	Avatars  *AvatarHandler
	Web      *webui.Handler
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

	health := NewHealthHandler(deps.Log, deps.Config.App.Version, healthTimeout, !deps.Config.IsProd(), deps.Checkers...)
	r.Get("/health", health.Handle)
	r.Method(http.MethodGet, "/metrics", promhttp.HandlerFor(deps.Registry, promhttp.HandlerOpts{}))

	r.Route("/api/v1", func(api chi.Router) {
		api.Post("/avatars", deps.Avatars.Upload)
		api.Get("/avatars/{avatar_id}", deps.Avatars.Get)
		api.Get("/avatars/{avatar_id}/metadata", deps.Avatars.Metadata)
		api.Delete("/avatars/{avatar_id}", deps.Avatars.Delete)

		api.Get("/users/{user_id}/avatar", deps.Avatars.GetCurrent)
		api.Get("/users/{user_id}/avatars", deps.Avatars.List)
		api.Delete("/users/{user_id}/avatar", deps.Avatars.DeleteCurrent)
	})

	r.Route("/web", deps.Web.Routes)
	r.Handle("/static/*", webui.StaticHandler())

	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/web/upload", http.StatusFound)
	})

	return r
}
