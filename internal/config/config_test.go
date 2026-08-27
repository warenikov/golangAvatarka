package config_test

import (
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-avatar-service/internal/config"
)

// validConfig — конфигурация со значениями по умолчанию, от которой отталкиваются кейсы.
func validConfig() *config.Config {
	cfg := &config.Config{}
	cfg.App.Env = config.EnvDev
	cfg.App.LogLevel = "info"
	cfg.App.HTTPAddr = ":8080"
	cfg.App.MaxUploadBytes = 10 << 20
	cfg.App.MaxImagePixels = 50_000_000
	cfg.App.AllowedMIME = []string{"image/jpeg", "image/png"}
	cfg.App.CORSOrigins = []string{"http://localhost:8080"}
	cfg.App.RateLimitRPM = 300
	cfg.App.RateLimitUpload = 10
	cfg.DB.Port = 5432
	cfg.DB.MinConns = 2
	cfg.DB.MaxConns = 10
	cfg.DB.Password = "s3cret"
	cfg.S3.Bucket = "avatars"
	cfg.S3.SecretKey = "s3cret"
	cfg.RabbitMQ.URL = "amqp://guest:guest@localhost:5672/"
	cfg.RabbitMQ.RetryTTL = 30 * time.Second
	cfg.RabbitMQ.MaxRetries = 5
	cfg.RabbitMQ.Prefetch = 4

	return cfg
}

// isolateEnv убирает из окружения все переменные сервиса на время теста.
//
// config.Load читает настоящее окружение, а docker-compose задаёт APP_ENV,
// DB_PORT и другие переменные для контейнеров приложения: без изоляции
// `go test ./...` внутри образа падал на несовпадении с ожидаемыми значениями.
func isolateEnv(t *testing.T) {
	t.Helper()

	prefixes := []string{"APP_", "DB_", "S3_", "RABBITMQ_", "WORKER_"}

	for _, entry := range os.Environ() {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}

		if !slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(name, p) }) {
			continue
		}

		require.NoError(t, os.Unsetenv(name))

		t.Cleanup(func() { _ = os.Setenv(name, value) })
	}
}

func TestLoadDefaults(t *testing.T) {
	isolateEnv(t)

	cfg, err := config.Load()
	require.NoError(t, err)

	assert.Equal(t, config.EnvDev, cfg.App.Env)
	assert.Equal(t, ":8080", cfg.App.HTTPAddr)
	assert.Equal(t, int64(10485760), cfg.App.MaxUploadBytes)
	assert.Equal(t, []string{"image/jpeg", "image/png", "image/webp"}, cfg.App.AllowedMIME)
	assert.Equal(t, 5432, cfg.DB.Port)
	assert.Equal(t, 300, cfg.App.RateLimitRPM)
	assert.Equal(t, 10, cfg.App.RateLimitUpload,
		"загрузка дороже чтения и ограничивается строже")
	assert.Equal(t, []string{"http://localhost:8080"}, cfg.App.CORSOrigins,
		"по умолчанию — явный origin, не wildcard")
	assert.Equal(t, "avatars", cfg.S3.Bucket)
	assert.Equal(t, 30*time.Second, cfg.RabbitMQ.RetryTTL)
	assert.Equal(t, time.Minute, cfg.Worker.ReconcileInterval)
	assert.False(t, cfg.IsProd())
}

func TestLoadFromEnv(t *testing.T) {
	isolateEnv(t)

	t.Setenv("APP_HTTP_ADDR", ":9999")
	t.Setenv("APP_LOG_LEVEL", "debug")
	t.Setenv("APP_ALLOWED_MIME", "image/png,image/webp")
	t.Setenv("DB_PORT", "5433")
	t.Setenv("RABBITMQ_PREFETCH", "16")

	cfg, err := config.Load()
	require.NoError(t, err)

	assert.Equal(t, ":9999", cfg.App.HTTPAddr)
	assert.Equal(t, "debug", cfg.App.LogLevel)
	assert.Equal(t, []string{"image/png", "image/webp"}, cfg.App.AllowedMIME)
	assert.Equal(t, 5433, cfg.DB.Port)
	assert.Equal(t, 16, cfg.RabbitMQ.Prefetch)
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	isolateEnv(t)

	t.Setenv("APP_ENV", "staging")

	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "APP_ENV")
}

func TestLoadRejectsUnparsableValues(t *testing.T) {
	isolateEnv(t)

	t.Setenv("DB_PORT", "не число")

	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse environment")
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*config.Config)
		wantErr string
	}{
		{"валидная конфигурация", func(*config.Config) {}, ""},
		{"неизвестное окружение", func(c *config.Config) { c.App.Env = "staging" }, "APP_ENV"},
		{"неизвестный уровень логов", func(c *config.Config) { c.App.LogLevel = "trace" }, "APP_LOG_LEVEL"},
		{"пустой адрес", func(c *config.Config) { c.App.HTTPAddr = "" }, "APP_HTTP_ADDR"},
		{"нулевой лимит загрузки", func(c *config.Config) { c.App.MaxUploadBytes = 0 }, "APP_MAX_UPLOAD_BYTES"},
		{"отрицательный лимит пикселей", func(c *config.Config) { c.App.MaxImagePixels = -1 }, "APP_MAX_IMAGE_PIXELS"},
		{"пустой список MIME", func(c *config.Config) { c.App.AllowedMIME = nil }, "APP_ALLOWED_MIME"},
		{"нулевой лимит запросов", func(c *config.Config) { c.App.RateLimitRPM = 0 }, "APP_RATE_LIMIT_RPM"},
		{"нулевой лимит загрузок", func(c *config.Config) { c.App.RateLimitUpload = 0 }, "APP_RATE_LIMIT_UPLOAD_RPM"},
		{
			name:    "лимит загрузок выше общего",
			mutate:  func(c *config.Config) { c.App.RateLimitUpload = 500 },
			wantErr: "больше общего лимита",
		},
		{"пустой список CORS", func(c *config.Config) { c.App.CORSOrigins = nil }, "APP_CORS_ORIGINS"},
		{"порт вне диапазона", func(c *config.Config) { c.DB.Port = 70000 }, "DB_PORT"},
		{"min больше max", func(c *config.Config) { c.DB.MinConns = 20 }, "DB_MIN_CONNS"},
		{"пустой бакет", func(c *config.Config) { c.S3.Bucket = "" }, "S3_BUCKET"},
		{"нулевой TTL ретраев", func(c *config.Config) { c.RabbitMQ.RetryTTL = 0 }, "RABBITMQ_RETRY_TTL"},
		{"слишком длинный TTL", func(c *config.Config) { c.RabbitMQ.RetryTTL = 48 * time.Hour }, "RABBITMQ_RETRY_TTL"},
		{"нет ретраев", func(c *config.Config) { c.RabbitMQ.MaxRetries = 0 }, "RABBITMQ_MAX_RETRIES"},
		{"нулевой prefetch", func(c *config.Config) { c.RabbitMQ.Prefetch = 0 }, "RABBITMQ_PREFETCH"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.mutate(cfg)

			err := cfg.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestValidateProdHardening(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*config.Config)
		wantErr string
	}{
		{"пароль БД по умолчанию", func(c *config.Config) { c.DB.Password = "avatars" }, "DB_PASSWORD"},
		{"ключ S3 по умолчанию", func(c *config.Config) { c.S3.SecretKey = "minioadmin" }, "S3_SECRET_KEY"},
		{"wildcard в CORS", func(c *config.Config) { c.App.CORSOrigins = []string{"*"} }, "APP_CORS_ORIGINS"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.App.Env = config.EnvProd
			tt.mutate(cfg)

			err := cfg.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}

	t.Run("prod с заданными секретами проходит", func(t *testing.T) {
		cfg := validConfig()
		cfg.App.Env = config.EnvProd

		require.NoError(t, cfg.Validate())
		assert.True(t, cfg.IsProd())
	})
}

func TestValidateCollectsAllErrors(t *testing.T) {
	cfg := validConfig()
	cfg.App.Env = "staging"
	cfg.App.LogLevel = "trace"
	cfg.S3.Bucket = ""

	err := cfg.Validate()
	require.Error(t, err)

	for _, want := range []string{"APP_ENV", "APP_LOG_LEVEL", "S3_BUCKET"} {
		assert.Contains(t, err.Error(), want, "валидация должна сообщать обо всех проблемах разом")
	}
}

func TestDBDSN(t *testing.T) {
	db := config.DB{
		Host: "db.internal", Port: 5432, User: "avatars",
		Password: "p@ss word/1", Name: "avatars", SSLMode: "require",
	}

	assert.Equal(t, "postgres://avatars:p%40ss%20word%2F1@db.internal:5432/avatars?sslmode=require", db.DSN())
}

// Главное свойство DSN — пароль должен дойти до драйвера ровно таким, каким
// его задали. Раньше пробел кодировался как "+", и в userinfo это литеральный
// плюс: pgx отвечал "password authentication failed" без единой подсказки.
func TestDBDSNRoundTrip(t *testing.T) {
	tests := []struct {
		name     string
		password string
	}{
		{"обычный", "avatars"},
		{"с пробелом", "p@ss word/1"},
		{"со спецсимволами", "p:a?s#s&w=o/rd"},
		{"кириллица", "па$$ворд"},
		{"пустой", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := config.DB{
				Host: "db.internal", Port: 5432, User: "avatars",
				Password: tt.password, Name: "avatars", SSLMode: "require",
			}

			u, err := url.Parse(db.DSN())
			require.NoError(t, err)

			assert.Equal(t, "avatars", u.User.Username())

			got, _ := u.User.Password()
			assert.Equal(t, tt.password, got, "пароль должен переживать разбор адреса обратно")

			assert.Equal(t, "db.internal:5432", u.Host)
			assert.Equal(t, "/avatars", u.Path)
			assert.Equal(t, "require", u.Query().Get("sslmode"))
		})
	}
}

// Хост из конфига может оказаться IPv6-адресом; без скобок адрес разобрать нельзя.
func TestDBDSNIPv6Host(t *testing.T) {
	db := config.DB{Host: "::1", Port: 5432, User: "avatars", Password: "x", Name: "avatars", SSLMode: "disable"}

	u, err := url.Parse(db.DSN())
	require.NoError(t, err)

	assert.Equal(t, "[::1]:5432", u.Host)
	assert.Equal(t, "::1", u.Hostname())
	assert.Equal(t, "5432", u.Port())
}

func TestDBRedacted(t *testing.T) {
	db := config.DB{
		Host: "db.internal", Port: 5432, User: "avatars",
		Password: "p@ss word/1", Name: "avatars", SSLMode: "require",
	}

	redacted := db.Redacted()

	assert.NotContains(t, redacted, "p@ss", "пароль не должен попадать в логи")
	assert.NotContains(t, redacted, "word", "пароль не должен попадать в логи")
	assert.Equal(t, "postgres://avatars:xxxxx@db.internal:5432/avatars?sslmode=require", redacted,
		"маскировка — штатная url.URL.Redacted из stdlib")
}

func TestAllowedMIMESet(t *testing.T) {
	app := config.App{AllowedMIME: []string{"image/jpeg", " image/png ", "IMAGE/WEBP"}}

	tests := []struct {
		mime string
		want bool
	}{
		{"image/jpeg", true},
		{"image/png", true},
		{"image/webp", true},
		{"image/gif", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.mime, func(t *testing.T) {
			assert.Equal(t, tt.want, app.AllowedMIMESet(tt.mime))
		})
	}
}

func TestRedactedNeverLeaksLongPassword(t *testing.T) {
	db := config.DB{User: "avatars", Password: strings.Repeat("s3cret", 12)}

	assert.NotContains(t, db.Redacted(), "s3cret")
}
