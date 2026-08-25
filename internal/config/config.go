// Package config загружает конфигурацию сервиса из переменных окружения.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

const (
	EnvDev  = "dev"
	EnvProd = "prod"

	devDBPassword  = "avatars"
	devS3SecretKey = "minioadmin"

	maxRetryTTL = 24 * time.Hour
)

type Config struct {
	App      App
	DB       DB
	S3       S3
	RabbitMQ RabbitMQ
	Worker   Worker
}

type App struct {
	Env             string        `env:"APP_ENV" envDefault:"dev"`
	LogLevel        string        `env:"APP_LOG_LEVEL" envDefault:"info"`
	HTTPAddr        string        `env:"APP_HTTP_ADDR" envDefault:":8080"`
	ShutdownTimeout time.Duration `env:"APP_SHUTDOWN_TIMEOUT" envDefault:"10s"`
	RequestTimeout  time.Duration `env:"APP_REQUEST_TIMEOUT" envDefault:"60s"`
	Version         string        `env:"APP_VERSION" envDefault:"dev"`
	MaxUploadBytes  int64         `env:"APP_MAX_UPLOAD_BYTES" envDefault:"10485760"`
	MaxImagePixels  int64         `env:"APP_MAX_IMAGE_PIXELS" envDefault:"50000000"`
	AllowedMIME     []string      `env:"APP_ALLOWED_MIME" envSeparator:"," envDefault:"image/jpeg,image/png,image/webp"`
	CORSOrigins     []string      `env:"APP_CORS_ORIGINS" envSeparator:"," envDefault:"http://localhost:8080"`
	RateLimitRPM    int           `env:"APP_RATE_LIMIT_RPM" envDefault:"60"`
}

type DB struct {
	Host     string `env:"DB_HOST" envDefault:"localhost"`
	Port     int    `env:"DB_PORT" envDefault:"5432"`
	User     string `env:"DB_USER" envDefault:"avatars"`
	Password string `env:"DB_PASSWORD" envDefault:"avatars" json:"-"`
	Name     string `env:"DB_NAME" envDefault:"avatars"`
	SSLMode  string `env:"DB_SSLMODE" envDefault:"disable"`
	MaxConns int32  `env:"DB_MAX_CONNS" envDefault:"10"`
	MinConns int32  `env:"DB_MIN_CONNS" envDefault:"2"`
}

type S3 struct {
	Endpoint  string `env:"S3_ENDPOINT" envDefault:"localhost:9000"`
	AccessKey string `env:"S3_ACCESS_KEY" envDefault:"minioadmin" json:"-"`
	SecretKey string `env:"S3_SECRET_KEY" envDefault:"minioadmin" json:"-"`
	Bucket    string `env:"S3_BUCKET" envDefault:"avatars"`
	Region    string `env:"S3_REGION" envDefault:"us-east-1"`
	UseSSL    bool   `env:"S3_USE_SSL" envDefault:"false"`
}

type RabbitMQ struct {
	URL          string        `env:"RABBITMQ_URL" envDefault:"amqp://guest:guest@localhost:5672/" json:"-"`
	Exchange     string        `env:"RABBITMQ_EXCHANGE" envDefault:"avatars.exchange"`
	QueueProcess string        `env:"RABBITMQ_QUEUE_PROCESS" envDefault:"avatars.process"`
	QueueDelete  string        `env:"RABBITMQ_QUEUE_DELETE" envDefault:"avatars.delete"`
	QueueRetry   string        `env:"RABBITMQ_QUEUE_RETRY" envDefault:"avatars.retry"`
	QueueDead    string        `env:"RABBITMQ_QUEUE_DEAD" envDefault:"avatars.dead"`
	RetryTTL     time.Duration `env:"RABBITMQ_RETRY_TTL" envDefault:"30s"`
	MaxRetries   int           `env:"RABBITMQ_MAX_RETRIES" envDefault:"5"`
	Prefetch     int           `env:"RABBITMQ_PREFETCH" envDefault:"4"`
}

type Worker struct {
	ReconcileInterval time.Duration `env:"WORKER_RECONCILE_INTERVAL" envDefault:"1m"`
	ReconcileAge      time.Duration `env:"WORKER_RECONCILE_AGE" envDefault:"5m"`
}

// Load читает файл .env, если он существует, разбирает переменные окружения и проверяет значения.
func Load() (*Config, error) {
	if err := godotenv.Load(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("load .env: %w", err)
	}

	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return nil, fmt.Errorf("parse environment: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}
	return &cfg, nil
}

// Validate проверяет ограничения, которые нельзя выразить тегами структуры.
func (c *Config) Validate() error {
	var errs []error

	if !slices.Contains([]string{EnvDev, EnvProd}, c.App.Env) {
		errs = append(errs, fmt.Errorf("APP_ENV: ожидается %q или %q, получено %q", EnvDev, EnvProd, c.App.Env))
	}
	if !slices.Contains([]string{"debug", "info", "warn", "error"}, c.App.LogLevel) {
		errs = append(errs, fmt.Errorf("APP_LOG_LEVEL: недопустимый уровень %q", c.App.LogLevel))
	}
	if c.App.HTTPAddr == "" {
		errs = append(errs, errors.New("APP_HTTP_ADDR: пустой адрес"))
	}
	if c.App.MaxUploadBytes <= 0 {
		errs = append(errs, fmt.Errorf("APP_MAX_UPLOAD_BYTES: ожидается положительное число, получено %d", c.App.MaxUploadBytes))
	}
	if c.App.MaxImagePixels <= 0 {
		errs = append(errs, fmt.Errorf("APP_MAX_IMAGE_PIXELS: ожидается положительное число, получено %d", c.App.MaxImagePixels))
	}
	if len(c.App.AllowedMIME) == 0 {
		errs = append(errs, errors.New("APP_ALLOWED_MIME: список пуст"))
	}
	if c.DB.Port < 1 || c.DB.Port > 65535 {
		errs = append(errs, fmt.Errorf("DB_PORT: вне диапазона 1-65535: %d", c.DB.Port))
	}
	if c.DB.MinConns > c.DB.MaxConns {
		errs = append(errs, fmt.Errorf("DB_MIN_CONNS (%d) больше DB_MAX_CONNS (%d)", c.DB.MinConns, c.DB.MaxConns))
	}
	if c.S3.Bucket == "" {
		errs = append(errs, errors.New("S3_BUCKET: пустое имя бакета"))
	}
	if _, err := url.Parse(c.RabbitMQ.URL); err != nil {
		errs = append(errs, fmt.Errorf("RABBITMQ_URL: %w", err))
	}
	if c.RabbitMQ.RetryTTL <= 0 || c.RabbitMQ.RetryTTL > maxRetryTTL {
		errs = append(errs, fmt.Errorf("RABBITMQ_RETRY_TTL: ожидается от 1s до %s, получено %s",
			maxRetryTTL, c.RabbitMQ.RetryTTL))
	}
	if c.RabbitMQ.MaxRetries < 1 {
		errs = append(errs, fmt.Errorf("RABBITMQ_MAX_RETRIES: ожидается положительное число, получено %d", c.RabbitMQ.MaxRetries))
	}
	if c.RabbitMQ.Prefetch < 1 {
		errs = append(errs, fmt.Errorf("RABBITMQ_PREFETCH: ожидается положительное число, получено %d", c.RabbitMQ.Prefetch))
	}

	if c.App.Env == EnvProd {
		if c.DB.Password == devDBPassword {
			errs = append(errs, errors.New("DB_PASSWORD: в prod нельзя оставлять пароль по умолчанию"))
		}
		if c.S3.SecretKey == devS3SecretKey {
			errs = append(errs, errors.New("S3_SECRET_KEY: в prod нельзя оставлять ключ по умолчанию"))
		}
		if slices.Contains(c.App.CORSOrigins, "*") {
			errs = append(errs, errors.New("APP_CORS_ORIGINS: в prod запрещён wildcard"))
		}
	}

	return errors.Join(errs...)
}

// IsProd сообщает, запущен ли сервис в производственном окружении.
func (c *Config) IsProd() bool { return c.App.Env == EnvProd }

// DSN собирает строку подключения к PostgreSQL.
func (d DB) DSN() string {
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
		url.QueryEscape(d.User), url.QueryEscape(d.Password),
		d.Host, d.Port, d.Name, d.SSLMode)
}

// Redacted возвращает DSN с замаскированным паролем — пригоден для логов.
func (d DB) Redacted() string {
	return fmt.Sprintf("postgres://%s:***@%s:%d/%s?sslmode=%s",
		d.User, d.Host, d.Port, d.Name, d.SSLMode)
}

// AllowedMIMESet проверяет, разрешён ли MIME-тип к загрузке.
func (a App) AllowedMIMESet(mime string) bool {
	return slices.ContainsFunc(a.AllowedMIME, func(m string) bool {
		return strings.EqualFold(strings.TrimSpace(m), mime)
	})
}
