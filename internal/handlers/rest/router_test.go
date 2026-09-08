package rest_test

import (
	"bytes"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"go-avatar-service/internal/config"
	"go-avatar-service/internal/handlers/rest"
	webui "go-avatar-service/internal/handlers/web"
	"go-avatar-service/internal/observability"
	"go-avatar-service/internal/services"
)

const testOrigin = "https://app.example.com"

type routerOpts struct {
	rateLimitRPM    int
	rateLimitUpload int
	corsOrigins     []string
	checkers        []rest.Checker
}

// newRouter собирает боевой роутер целиком: middleware, лимиты, CORS и таблицу
// маршрутов — то, что в тестах отдельных обработчиков не проверяется.
func newRouter(t *testing.T, opts routerOpts) (http.Handler, apiDeps) {
	t.Helper()

	d := apiDeps{
		repo:      NewMockAvatarRepository(t),
		storage:   NewMockObjectStorage(t),
		publisher: NewMockEventPublisher(t),
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	cfg := &config.Config{}
	cfg.App.Env = config.EnvDev
	cfg.App.Version = "test"
	cfg.App.MaxUploadBytes = testMaxUpload
	cfg.App.AllowedMIME = []string{"image/jpeg", "image/png", "image/webp"}
	cfg.App.RequestTimeout = 0
	cfg.App.RateLimitRPM = opts.rateLimitRPM
	cfg.App.RateLimitUpload = opts.rateLimitUpload
	cfg.App.CORSOrigins = opts.corsOrigins

	if cfg.App.RateLimitRPM == 0 {
		cfg.App.RateLimitRPM = 1000
	}
	if cfg.App.RateLimitUpload == 0 {
		cfg.App.RateLimitUpload = 1000
	}
	if cfg.App.CORSOrigins == nil {
		cfg.App.CORSOrigins = []string{testOrigin}
	}

	svc := services.NewAvatarService(d.repo, d.storage, d.publisher, log)
	uploadLimiter := rest.UploadRateLimiter(log, cfg.App.RateLimitUpload)
	registry := prometheus.NewRegistry()

	router := rest.NewRouter(rest.RouterDeps{
		Config:        cfg,
		Log:           log,
		Metrics:       observability.NewHTTP(registry),
		Registry:      registry,
		Avatars:       rest.NewAvatarHandler(svc, cfg, log),
		Web:           webui.NewHandler(svc, cfg, log, uploadLimiter),
		UploadLimiter: uploadLimiter,
		Checkers:      opts.checkers,
	})

	return router, d
}

func TestRouterRoutes(t *testing.T) {
	router, _ := newRouter(t, routerOpts{})

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{"корень уводит на загрузку", http.MethodGet, "/", http.StatusFound},
		{"health", http.MethodGet, "/health", http.StatusOK},
		{"metrics", http.MethodGet, "/metrics", http.StatusOK},
		{"страница загрузки", http.MethodGet, "/web/upload", http.StatusOK},
		{"поиск галереи", http.MethodGet, "/web/gallery", http.StatusOK},
		{"статика", http.MethodGet, "/static/", http.StatusOK},
		{"index.html уводит на каталог", http.MethodGet, "/static/index.html", http.StatusMovedPermanently},
		{"несуществующий путь", http.MethodGet, "/нет-такого", http.StatusNotFound},
		{"неизвестный метод", http.MethodPut, "/api/v1/avatars", http.StatusMethodNotAllowed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))

			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}

// Маршруты API проверяются по реальной таблице: опечатка в шаблоне пути
// в router.go тестами отдельных обработчиков не ловится.
func TestRouterAPIPathsReachHandlers(t *testing.T) {
	router, _ := newRouter(t, routerOpts{})

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"получение по id", http.MethodGet, "/api/v1/avatars/не-uuid"},
		{"метаданные", http.MethodGet, "/api/v1/avatars/не-uuid/metadata"},
		{"удаление по id", http.MethodDelete, "/api/v1/avatars/не-uuid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req.Header.Set(headerUserID, testUserID)

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			// 400 «Invalid avatar id» означает, что запрос дошёл до обработчика
			// и тот прочитал параметр пути под ожидаемым именем.
			require.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, rec.Body.String(), "Invalid avatar id")
		})
	}
}

func TestSecurityHeaders(t *testing.T) {
	router, _ := newRouter(t, routerOpts{})

	tests := []struct {
		name string
		path string
	}{
		{"страница", "/web/upload"},
		{"health", "/health"},
		{"статика", "/static/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))

			assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"),
				"сервис отдаёт загруженные пользователями файлы — сниффинг типа недопустим")
			assert.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
			assert.Equal(t, "no-referrer", rec.Header().Get("Referrer-Policy"))
		})
	}
}

func TestCORSAllowsConfiguredOrigin(t *testing.T) {
	router, _ := newRouter(t, routerOpts{corsOrigins: []string{testOrigin}})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", testOrigin)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Equal(t, testOrigin, rec.Header().Get("Access-Control-Allow-Origin"))
	assert.Contains(t, rec.Header().Values("Vary"), "Origin",
		"ответы кешируются, и без Vary общий кеш отдал бы чужой Allow-Origin")

	// Без этого браузерный JS не увидит ETag и не сможет слать If-None-Match.
	// Имя канонизируется библиотекой в "Etag"; браузеры сравнивают без учёта регистра.
	assert.Contains(t, strings.ToLower(rec.Header().Get("Access-Control-Expose-Headers")), "etag")
}

func TestCORSRejectsUnknownOrigin(t *testing.T) {
	router, _ := newRouter(t, routerOpts{corsOrigins: []string{testOrigin}})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "https://evil.example.com")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"),
		"origin вне белого списка не получает разрешения")
}

// Preflight приходит на путь, для которого OPTIONS-маршрута нет.
// CORS-middleware стоит выше роутинга и обязан ответить сам.
func TestCORSPreflight(t *testing.T) {
	router, _ := newRouter(t, routerOpts{corsOrigins: []string{testOrigin}})

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/avatars", nil)
	req.Header.Set("Origin", testOrigin)
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "X-User-ID, Content-Type")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Contains(t, []int{http.StatusOK, http.StatusNoContent}, rec.Code)
	assert.Equal(t, testOrigin, rec.Header().Get("Access-Control-Allow-Origin"))
	assert.Contains(t, rec.Header().Get("Access-Control-Allow-Methods"), http.MethodPost)
	assert.Contains(t, rec.Header().Get("Access-Control-Allow-Headers"), "X-User-Id")
	assert.NotEmpty(t, rec.Header().Get("Access-Control-Max-Age"))
	assert.Empty(t, rec.Header().Get("Access-Control-Allow-Credentials"),
		"учётные данные не передаются, значит и разрешать их не нужно")
}

func TestRateLimitByIP(t *testing.T) {
	const limit = 3

	router, _ := newRouter(t, routerOpts{rateLimitRPM: limit})

	for i := range limit {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		require.Equal(t, http.StatusOK, rec.Code, "запрос %d должен пройти", i+1)
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Contains(t, rec.Body.String(), "Too many requests")
	assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	assert.Equal(t, strconv.Itoa(limit), rec.Header().Get("X-RateLimit-Limit"))
	assert.NotEmpty(t, rec.Header().Get("Retry-After"))
}

// Ключевое свойство: лимит нельзя обойти подделкой заголовков.
// Именно из-за этого в проекте отказались от middleware.RealIP.
func TestRateLimitIgnoresForwardedHeaders(t *testing.T) {
	const limit = 2

	router, _ := newRouter(t, routerOpts{rateLimitRPM: limit})

	spoofHeaders := []string{"X-Forwarded-For", "X-Real-IP", "True-Client-IP"}

	for i := range limit {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		require.Equal(t, http.StatusOK, rec.Code, "запрос %d", i+1)
	}

	for _, header := range spoofHeaders {
		t.Run(header, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/health", nil)
			req.Header.Set(header, "203.0.113.99")

			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusTooManyRequests, rec.Code,
				"подстановка %s не должна давать новое ведро лимита", header)
		})
	}
}

func TestRateLimitSeparatesClients(t *testing.T) {
	const limit = 1

	router, _ := newRouter(t, routerOpts{rateLimitRPM: limit})

	first := httptest.NewRequest(http.MethodGet, "/health", nil)
	first.RemoteAddr = "198.51.100.1:5000"

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, first)
	require.Equal(t, http.StatusOK, rec.Code)

	// Тот же адрес — лимит исчерпан.
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, first)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)

	// Другой адрес — своё ведро.
	second := httptest.NewRequest(http.MethodGet, "/health", nil)
	second.RemoteAddr = "198.51.100.2:5000"

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, second)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// Клиент с IPv6 управляет целой /64 и мог бы менять адрес на каждый запрос.
func TestRateLimitBucketsIPv6BySubnet(t *testing.T) {
	const limit = 1

	router, _ := newRouter(t, routerOpts{rateLimitRPM: limit})

	first := httptest.NewRequest(http.MethodGet, "/health", nil)
	first.RemoteAddr = "[2001:db8:1:2::1]:5000"

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, first)
	require.Equal(t, http.StatusOK, rec.Code)

	// Другой адрес той же /64 — то же ведро.
	second := httptest.NewRequest(http.MethodGet, "/health", nil)
	second.RemoteAddr = "[2001:db8:1:2::dead]:5000"

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, second)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code,
		"смена адреса внутри своей /64 не должна давать новый лимит")
}

func TestUploadRateLimitIsStricter(t *testing.T) {
	router, d := newRouter(t, routerOpts{rateLimitRPM: 100, rateLimitUpload: 1})

	d.storage.EXPECT().Put(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil).Once()
	d.repo.EXPECT().Create(mock.Anything, mock.Anything).Return(nil).Once()
	d.publisher.EXPECT().PublishUpload(mock.Anything, mock.Anything).Return(nil).Once()

	first := httptest.NewRecorder()
	router.ServeHTTP(first, uploadRequest(t, testUserID, "file", pngBytes(t, 20, 20)))
	require.Equal(t, http.StatusCreated, first.Code, first.Body.String())

	second := httptest.NewRecorder()
	router.ServeHTTP(second, uploadRequest(t, testUserID, "file", pngBytes(t, 20, 20)))
	require.Equal(t, http.StatusTooManyRequests, second.Code)

	// Чтение при этом ещё разрешено: лимиты раздельные.
	read := httptest.NewRecorder()
	router.ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/health", nil))
	assert.Equal(t, http.StatusOK, read.Code)
}

// Веб-форма и REST — одна и та же дорогая операция, лимит у них общий,
// иначе его обходят сменой точки входа.
func TestUploadRateLimitSharedBetweenAPIAndWeb(t *testing.T) {
	router, d := newRouter(t, routerOpts{rateLimitRPM: 100, rateLimitUpload: 1})

	d.storage.EXPECT().Put(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(nil).Once()
	d.repo.EXPECT().Create(mock.Anything, mock.Anything).Return(nil).Once()
	d.publisher.EXPECT().PublishUpload(mock.Anything, mock.Anything).Return(nil).Once()

	api := httptest.NewRecorder()
	router.ServeHTTP(api, uploadRequest(t, testUserID, "file", pngBytes(t, 20, 20)))
	require.Equal(t, http.StatusCreated, api.Code, api.Body.String())

	contentType, body := webMultipart(t, testUserID, pngBytes(t, 20, 20))

	req := httptest.NewRequest(http.MethodPost, "/web/upload", body)
	req.Header.Set("Content-Type", contentType)

	web := httptest.NewRecorder()
	router.ServeHTTP(web, req)

	assert.Equal(t, http.StatusTooManyRequests, web.Code)
}

// webMultipart собирает тело формы веб-интерфейса: там user_id — поле формы,
// а не заголовок.
func webMultipart(t *testing.T, userID string, content []byte) (string, *bytes.Buffer) {
	t.Helper()

	var body bytes.Buffer
	form := multipart.NewWriter(&body)

	require.NoError(t, form.WriteField("user_id", userID))

	part, err := form.CreateFormFile("file", "avatar.png")
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)

	require.NoError(t, form.Close())

	return form.FormDataContentType(), &body
}
