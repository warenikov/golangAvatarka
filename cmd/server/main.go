package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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

	healthTimeout = 2 * time.Second
)

func main() {
	if err := run(); err != nil {
		// Логгером, а не в stderr: сборщик логов индексирует только JSON-строки
		// с полем service, и обычный Fprintf не попал бы в OpenSearch —
		// то есть причина падения терялась бы ровно тогда, когда нужна.
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).
			With("service", "server").
			Error("процесс остановлен с ошибкой", "err", err)
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

	shutdownTracing, err := observability.SetupTracing(ctx, observability.TracingConfig{
		Enabled:     cfg.Tracing.Enabled,
		Endpoint:    cfg.Tracing.Endpoint,
		ServiceName: "gophprofile-server",
		Version:     cfg.App.Version,
		Environment: cfg.App.Env,
		SampleRatio: cfg.Tracing.SampleRatio,
	})
	if err != nil {
		return fmt.Errorf("tracing: %w", err)
	}

	// Контекст отдельный: основной к моменту остановки уже отменён сигналом,
	// а накопленные спаны нужно успеть дослать.
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.App.ShutdownTimeout)
		defer cancel()

		if flushErr := shutdownTracing(flushCtx); flushErr != nil {
			log.Error("трейсы не досланы", "err", flushErr)
		}
	}()

	log.InfoContext(ctx, "трейсинг настроен",
		"enabled", cfg.Tracing.Enabled, "endpoint", cfg.Tracing.Endpoint)

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

	registry, err := observability.NewRegistry()
	if err != nil {
		return fmt.Errorf("metrics registry: %w", err)
	}

	businessMetrics, err := observability.NewBusiness(registry)
	if err != nil {
		return fmt.Errorf("business metrics: %w", err)
	}

	httpMetrics, err := observability.NewHTTP(registry)
	if err != nil {
		return fmt.Errorf("http metrics: %w", err)
	}

	publisher = publisher.WithMetrics(businessMetrics)

	repo := postgres.NewAvatarRepository(pool)
	avatarSvc := services.NewAvatarService(repo, storage, publisher, log,
		services.WithMetrics(businessMetrics))

	// Один ограничитель на обе точки входа загрузки — REST и веб-форму.
	uploadLimiter := rest.UploadRateLimiter(log.With("component", "ratelimit"), cfg.App.RateLimitUpload)
	webHandler := webui.NewHandler(avatarSvc, cfg, log.With("component", "web"), uploadLimiter)

	// Причина отказа компонента раскрывается только вне прода: текст ошибки
	// подключения выдаёт адреса и учётные записи инфраструктуры.
	health := rest.NewHealthHandler(log.With("component", "health"), cfg.App.Version,
		healthTimeout, !cfg.IsProd(),
		postgres.NewHealthChecker(pool),
		s3.NewHealthChecker(storage),
		rabbitmq.NewHealthChecker(conn),
	)

	router := rest.NewRouter(rest.RouterDeps{
		Config:        cfg,
		Log:           log,
		Metrics:       httpMetrics,
		Avatars:       rest.NewAvatarHandler(avatarSvc, cfg, log.With("component", "http")),
		Web:           webHandler,
		Health:        health,
		UploadLimiter: uploadLimiter,
	})

	// Служебный слушатель переживает остановку основного: пока под сливает
	// соединения, оркестратор продолжает опрашивать готовность, и ответ
	// «сливаюсь» должен доходить. Контекст без отмены родителем — сигнал
	// гасит основной сервер, а не этот.
	adminCtx, stopAdmin := context.WithCancel(context.WithoutCancel(ctx))
	defer stopAdmin()

	admin := observability.NewServer(cfg.App.AdminAddr, registry,
		observability.AdminRoutes{Live: health.Live, Ready: health.Ready},
		log.With("component", "admin"))
	go admin.Run(adminCtx)

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

	// Сначала отказ готовности, и только потом остановка приёма соединений.
	// В Kubernetes удаление пода из endpoints идёт параллельно с доставкой
	// сигнала, и без паузы часть запросов успевает прийти в процесс, который
	// уже закрыл слушатель, — клиент получает разрыв вместо ответа.
	health.Drain()

	if cfg.App.DrainDelay > 0 {
		log.Info("готовность отключена, ждём вывода из балансировки",
			"delay", cfg.App.DrainDelay.String())
		time.Sleep(cfg.App.DrainDelay)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.App.ShutdownTimeout)
	defer cancel()

	if err = srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	stopAdmin()

	log.Info("сервер остановлен")

	return nil
}
