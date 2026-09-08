package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go-avatar-service/internal/broker/rabbitmq"
	"go-avatar-service/internal/config"
	"go-avatar-service/internal/handlers/rest"
	webui "go-avatar-service/internal/handlers/web"
	"go-avatar-service/internal/logger"
	"go-avatar-service/internal/observability"
	"go-avatar-service/internal/repository/postgres"
	"go-avatar-service/internal/repository/s3"
	"go-avatar-service/internal/services"
)

const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 60 * time.Second
	writeTimeout      = 60 * time.Second
	idleTimeout       = 120 * time.Second
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "сервер остановлен с ошибкой: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log, err := logger.New(cfg.App.LogLevel, os.Stdout)
	if err != nil {
		return err
	}
	log = log.With("service", "server", "version", cfg.App.Version)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.NewPool(ctx, cfg.DB)
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer pool.Close()

	log.InfoContext(ctx, "подключение к базе установлено", "dsn", cfg.DB.Redacted())

	// В compose схему накатывает отдельный сервис migrate, и сервер стартует
	// только после его успешного завершения. Локальный `make run-server`
	// по-прежнему поднимает схему сам — иначе каждый запуск требовал бы
	// отдельной команды.
	if cfg.App.AutoMigrate {
		if err = postgres.Migrate(ctx, pool); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}

		log.InfoContext(ctx, "схема актуальна")
	}

	storage, err := s3.NewStorage(ctx, cfg.S3)
	if err != nil {
		return fmt.Errorf("s3: %w", err)
	}

	log.InfoContext(ctx, "хранилище готово", "endpoint", cfg.S3.Endpoint, "bucket", cfg.S3.Bucket)

	conn, err := rabbitmq.Connect(ctx, cfg.RabbitMQ, log)
	if err != nil {
		return fmt.Errorf("rabbitmq: %w", err)
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			log.Error("не удалось закрыть соединение с брокером", "err", closeErr)
		}
	}()

	publisher, err := rabbitmq.NewPublisher(conn)
	if err != nil {
		return fmt.Errorf("rabbitmq publisher: %w", err)
	}

	log.InfoContext(ctx, "брокер подключён", "exchange", cfg.RabbitMQ.Exchange)

	repo := postgres.NewAvatarRepository(pool)
	avatarSvc := services.NewAvatarService(repo, storage, publisher, log)

	// Один ограничитель на обе точки входа загрузки — REST и веб-форму.
	uploadLimiter := rest.UploadRateLimiter(log.With("component", "ratelimit"), cfg.App.RateLimitUpload)
	webHandler := webui.NewHandler(avatarSvc, cfg, log.With("component", "web"), uploadLimiter)

	registry := observability.NewRegistry()
	router := rest.NewRouter(rest.RouterDeps{
		Config:        cfg,
		Log:           log,
		Metrics:       observability.NewHTTP(registry),
		Registry:      registry,
		Avatars:       rest.NewAvatarHandler(avatarSvc, cfg, log.With("component", "http")),
		Web:           webHandler,
		UploadLimiter: uploadLimiter,
		Checkers: []rest.Checker{
			postgres.NewHealthChecker(pool),
			s3.NewHealthChecker(storage),
			rabbitmq.NewHealthChecker(conn),
		},
	})

	srv := &http.Server{
		Addr:              cfg.App.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	serverErr := make(chan error, 1)
	go func() {
		log.InfoContext(ctx, "сервер запускается", "addr", cfg.App.HTTPAddr, "env", cfg.App.Env)
		if listenErr := srv.ListenAndServe(); listenErr != nil && !errors.Is(listenErr, http.ErrServerClosed) {
			serverErr <- listenErr
		}
	}()

	select {
	case listenErr := <-serverErr:
		return fmt.Errorf("listen: %w", listenErr)
	case <-ctx.Done():
		stop()
		log.Info("получен сигнал, останавливаем сервер", "timeout", cfg.App.ShutdownTimeout.String())
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.App.ShutdownTimeout)
	defer cancel()

	if err = srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	log.Info("сервер остановлен")

	return nil
}
