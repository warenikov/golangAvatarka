package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"go-avatar-service/internal/breaker"
	"go-avatar-service/internal/broker/rabbitmq"
	"go-avatar-service/internal/config"
	"go-avatar-service/internal/handlers/rest"
	"go-avatar-service/internal/logger"
	"go-avatar-service/internal/observability"
	"go-avatar-service/internal/repository/postgres"
	"go-avatar-service/internal/repository/s3"
	"go-avatar-service/internal/worker"
)

const healthTimeout = 2 * time.Second

func main() {
	if err := run(); err != nil {
		// Логгером, а не в stderr: сборщик логов индексирует только JSON-строки
		// с полем service, и обычный Fprintf не попал бы в OpenSearch —
		// то есть причина падения терялась бы ровно тогда, когда нужна.
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).
			With("service", "worker").
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
	log = log.With("service", "worker", "version", cfg.App.Version)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := observability.SetupTracing(ctx, observability.TracingConfig{
		Enabled:     cfg.Tracing.Enabled,
		Endpoint:    cfg.Tracing.Endpoint,
		ServiceName: "gophprofile-worker",
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

	storage, err := s3.NewStorage(ctx, cfg.S3)
	if err != nil {
		return fmt.Errorf("s3: %w", err)
	}

	conn, err := rabbitmq.Connect(ctx, cfg.RabbitMQ, log)
	if err != nil {
		return fmt.Errorf("rabbitmq: %w", err)
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			log.Error("не удалось закрыть соединение с брокером", "err", closeErr)
		}
	}()

	registry, err := observability.NewRegistry()
	if err != nil {
		return fmt.Errorf("metrics registry: %w", err)
	}

	metrics, err := observability.NewBusiness(registry)
	if err != nil {
		return fmt.Errorf("business metrics: %w", err)
	}

	breakerMetrics, err := observability.NewBreakers(registry)
	if err != nil {
		return fmt.Errorf("breaker metrics: %w", err)
	}

	storage = storage.WithBreaker(breakerMetrics.NewBreaker("s3", cfg.Breaker))

	publisher, err := rabbitmq.NewPublisher(conn)
	if err != nil {
		return fmt.Errorf("rabbitmq publisher: %w", err)
	}
	publisher = publisher.WithMetrics(metrics).
		WithBreaker(breakerMetrics.NewBreaker("rabbitmq", cfg.Breaker))

	repo := postgres.NewAvatarRepository(pool).
		WithBreaker(breakerMetrics.NewBreaker("postgres", cfg.Breaker,
			breaker.WithHealthyErrors(postgres.IsDataError)))
	processor := worker.NewProcessor(repo, storage, cfg.App.MaxImagePixels, log,
		worker.WithMetrics(metrics))
	reconciler := worker.NewReconciler(repo, publisher,
		cfg.Worker.ReconcileInterval, cfg.Worker.ReconcileAge, log).WithMetrics(metrics)

	group, groupCtx := errgroup.WithContext(ctx)

	consumers := []struct {
		queue   string
		handler rabbitmq.Handler
	}{
		{cfg.RabbitMQ.QueueProcess, processor.HandleUpload},
		{cfg.RabbitMQ.QueueDelete, processor.HandleDelete},
	}

	for _, c := range consumers {
		consumer, consumerErr := rabbitmq.NewConsumer(conn, cfg.RabbitMQ.Prefetch, cfg.RabbitMQ.MaxRetries, log)
		if consumerErr != nil {
			return fmt.Errorf("rabbitmq consumer %s: %w", c.queue, consumerErr)
		}

		consumer = consumer.WithMetrics(metrics)

		group.Go(func() error {
			defer func() { _ = consumer.Close() }()

			return consumer.Consume(groupCtx, c.queue, c.handler)
		})
	}

	group.Go(func() error { return reconciler.Run(groupCtx) })

	// Служебный сервер: без него метрики воркера снять неоткуда.
	health := workerHealth(cfg, log.With("component", "health"), pool, storage, conn)
	admin := observability.NewServer(cfg.Worker.AdminAddr, registry,
		observability.AdminRoutes{Live: health.Live, Ready: health.Ready}, log)
	group.Go(func() error {
		// Отказ намеренно не уводит группу: воркер без метрик продолжает
		// разбирать очередь, и ронять его из-за служебного порта было бы
		// хуже, чем остаться без графиков.
		if adminErr := admin.Run(groupCtx); adminErr != nil {
			log.ErrorContext(groupCtx, "служебный сервер воркера остановлен", "err", adminErr)
		}

		return nil
	})

	log.InfoContext(ctx, "воркер запущен", "env", cfg.App.Env)

	if err = group.Wait(); err != nil {
		return fmt.Errorf("worker: %w", err)
	}

	log.Info("воркер остановлен")

	return nil
}

// workerHealth собирает обработчик проверок состояния воркера.
//
// Воркер не принимает трафик, поэтому проверка нужна не балансировщику,
// а оркестратору: без неё зависший на мёртвом соединении процесс выглядит
// живым и очередь молча копится. Отсюда и разделение проверок — готовность
// смотрит на зависимости, живость только на сам процесс: перезапуск воркера
// не поднимет ни упавшую базу, ни недоступный брокер.
func workerHealth(
	cfg *config.Config, log *slog.Logger,
	pool *pgxpool.Pool, storage *s3.Storage, conn *rabbitmq.Connection,
) *rest.HealthHandler {
	checkers := []rest.Checker{
		postgres.NewHealthChecker(pool),
		s3.NewHealthChecker(storage),
		rabbitmq.NewHealthChecker(conn),
	}

	// Логгер именно наш, а не slog.Default(): стандартный пишет текстом в stderr,
	// без уровня из конфига и без trace_id, и такие строки не проходят разбор
	// в конвейере логов — диагностика воркера просто не доезжала бы до OpenSearch.
	// Причина отказа раскрывается по тому же правилу, что и у сервера.
	return rest.NewHealthHandler(log, cfg.App.Version, healthTimeout, !cfg.IsProd(), checkers...)
}
