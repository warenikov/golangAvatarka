package rest

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"go-avatar-service/internal/config"
	webui "go-avatar-service/internal/handlers/web"
	"go-avatar-service/internal/observability"
)

// corsMaxAge — срок кеширования preflight-ответа в секундах.
const corsMaxAge = 300

// serverSpanName — запасное имя операции. Используется, только если
// форматтер почему-то не отработал: у otelhttp это обязательный аргумент.
const serverSpanName = "http.server"

type RouterDeps struct {
	Config  *config.Config
	Log     *slog.Logger
	Metrics *observability.HTTP
	Avatars *AvatarHandler
	Web     *webui.Handler

	// Health обслуживает /health, /livez и /readyz. Обработчик приходит
	// снаружи, а не собирается здесь: тот же экземпляр слушает служебный порт
	// и получает команду на слив при остановке.
	Health *HealthHandler

	// UploadLimiter — общий ограничитель загрузок для REST и веб-формы.
	// Если не задан, отдельного лимита на загрузку нет.
	UploadLimiter func(http.Handler) http.Handler
}

// NewRouter собирает middleware и маршруты HTTP-сервера.
func NewRouter(deps RouterDeps) http.Handler {
	r := chi.NewRouter()

	app := deps.Config.App

	r.Use(middleware.RequestID)
	// Трейсинг стоит выше логов и метрик: тогда trace_id попадает и в запись
	// лога о запросе, и спан покрывает всю обработку целиком.
	//
	// Имя спана до маршрутизации — только метод: шаблон пути в этот момент
	// ещё неизвестен, а по соглашениям OTel имя серверного спана без маршрута
	// и есть метод. Дальше TraceRoute доуточняет его до "METHOD /шаблон".
	r.Use(otelhttp.NewMiddleware(serverSpanName,
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return SpanMethodName(r.Method)
		}),
	))
	r.Use(TraceRoute)
	r.Use(Recoverer(deps.Log))
	r.Use(RequestLogger(deps.Log))
	r.Use(Metrics(deps.Metrics))
	r.Use(SecurityHeaders)
	r.Use(ClientIP(app.TrustedProxyCIDRs))

	// Живость — единственная проверка вне ограничителей. Она не трогает
	// зависимостей и стоит одного сравнения в памяти, поэтому лимит на ней
	// бесполезен, а отсутствие лимита ничем не грозит.
	if deps.Health != nil {
		r.Get("/livez", deps.Health.Live)
	}

	r.Group(func(pub chi.Router) {
		// Порядок важен. Адрес клиента резолвится выше по цепочке, иначе ключ
		// пуст и все запросы делят одно ведро на всех. Лимит по адресу идёт
		// раньше лимита по пользователю: он ограничивает и число ключей,
		// которыми можно набить таблицу счётчиков подставным X-User-ID.
		pub.Use(rateLimiter(deps.Log, app.RateLimitRPM, clientIPKey))
		pub.Use(rateLimiter(deps.Log, app.RateLimitRPM, userKey))

		pub.Use(cors.Handler(cors.Options{
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

		pub.Use(middleware.Timeout(app.RequestTimeout))

		// Готовность опрашивает базу, хранилище и брокер — три обращения
		// к инфраструктуре на запрос. Ingress маршрутизирует весь префикс,
		// так что без лимита этот путь стал бы самым дешёвым способом
		// нагрузить зависимости снаружи. Пробы оркестратора сюда не ходят:
		// у них служебный порт, где ограничителей нет вовсе.
		if deps.Health != nil {
			pub.Get("/health", deps.Health.Ready)
			pub.Get("/readyz", deps.Health.Ready)
		}

		// Загрузка дороже чтения: 10 МБ тела, запись в хранилище и работа воркера.
		// Для неё отдельный, более строгий лимит.
		uploadLimit := deps.UploadLimiter
		if uploadLimit == nil {
			uploadLimit = func(next http.Handler) http.Handler { return next }
		}

		pub.Route("/api/v1", func(api chi.Router) {
			api.With(uploadLimit).Post("/avatars", deps.Avatars.Upload)
			api.Get("/avatars/{avatar_id}", deps.Avatars.Get)
			api.Get("/avatars/{avatar_id}/metadata", deps.Avatars.Metadata)
			api.Delete("/avatars/{avatar_id}", deps.Avatars.Delete)

			api.Get("/users/{user_id}/avatar", deps.Avatars.GetCurrent)
			api.Get("/users/{user_id}/avatars", deps.Avatars.List)
			api.Delete("/users/{user_id}/avatar", deps.Avatars.DeleteCurrent)
		})

		pub.Route("/web", deps.Web.Routes)
		pub.Handle("/static/*", webui.StaticHandler())

		// Спецификация и страница её просмотра. Внутри группы, а не рядом
		// с пробами: это обычный публичный контент, и лимит частоты на нём
		// уместен ровно так же, как на остальных страницах.
		pub.Get("/openapi.yaml", OpenAPIHandler(deps.Log))
		pub.Get("/docs", DocsHandler(deps.Log))

		pub.Get("/", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/web/upload", http.StatusFound)
		})
	})

	return r
}
