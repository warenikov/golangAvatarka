package rest

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"go-avatar-service/internal/config"
	webui "go-avatar-service/internal/handlers/web"
	"go-avatar-service/internal/observability"
)

// corsMaxAge — срок кеширования preflight-ответа в секундах.
const corsMaxAge = 300

type RouterDeps struct {
	Config   *config.Config
	Log      *slog.Logger
	Metrics  *observability.HTTP
	Registry *prometheus.Registry
	Avatars  *AvatarHandler
	Web      *webui.Handler
	Checkers []Checker

	// UploadLimiter — общий ограничитель загрузок для REST и веб-формы.
	// Если не задан, отдельного лимита на загрузку нет.
	UploadLimiter func(http.Handler) http.Handler
}

// NewRouter собирает middleware и маршруты HTTP-сервера.
func NewRouter(deps RouterDeps) http.Handler {
	r := chi.NewRouter()

	app := deps.Config.App

	r.Use(middleware.RequestID)
	r.Use(Recoverer(deps.Log))
	r.Use(RequestLogger(deps.Log))
	r.Use(Metrics(deps.Metrics))
	r.Use(SecurityHeaders)

	// Порядок важен. Адрес клиента резолвится до лимитеров, иначе ключ пуст
	// и все запросы делят одно ведро на всех. Лимит по адресу идёт раньше
	// лимита по пользователю: он ограничивает и число ключей, которыми можно
	// набить таблицу счётчиков подставным X-User-ID.
	r.Use(middleware.ClientIPFromRemoteAddr)
	r.Use(rateLimiter(deps.Log, app.RateLimitRPM, clientIPKey))
	r.Use(rateLimiter(deps.Log, app.RateLimitRPM, userKey))

	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: app.CORSOrigins,
		AllowedMethods: []string{
			http.MethodGet, http.MethodHead, http.MethodPost, http.MethodDelete, http.MethodOptions,
		},
		AllowedHeaders: []string{"Accept", "Content-Type", "If-None-Match", headerUserID},
		// ETag не входит в список заголовков, видимых кросс-доменному JS
		// по умолчанию: без этого условные запросы с фронта не соберутся.
		ExposedHeaders:   []string{"ETag", "X-Avatar-Fallback"},
		AllowCredentials: false,
		MaxAge:           corsMaxAge,
	}))

	r.Use(middleware.Timeout(app.RequestTimeout))

	// Загрузка дороже чтения: 10 МБ тела, запись в хранилище и работа воркера.
	// Для неё отдельный, более строгий лимит.
	uploadLimit := deps.UploadLimiter
	if uploadLimit == nil {
		uploadLimit = func(next http.Handler) http.Handler { return next }
	}

	health := NewHealthHandler(deps.Log, app.Version, healthTimeout, !deps.Config.IsProd(), deps.Checkers...)
	r.Get("/health", health.Handle)
	r.Method(http.MethodGet, "/metrics", promhttp.HandlerFor(deps.Registry, promhttp.HandlerOpts{}))

	r.Route("/api/v1", func(api chi.Router) {
		api.With(uploadLimit).Post("/avatars", deps.Avatars.Upload)
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
