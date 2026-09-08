package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sync/errgroup"

	"go-avatar-service/internal/broker/rabbitmq"
	"go-avatar-service/internal/config"
	"go-avatar-service/internal/logger"
	"go-avatar-service/internal/repository/postgres"
	"go-avatar-service/internal/repository/s3"
	"go-avatar-service/internal/worker"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "воркер остановлен с ошибкой: %v\n", err)
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

	publisher, err := rabbitmq.NewPublisher(conn)
	if err != nil {
		return fmt.Errorf("rabbitmq publisher: %w", err)
	}

	repo := postgres.NewAvatarRepository(pool)
	processor := worker.NewProcessor(repo, storage, cfg.App.MaxImagePixels, log)
	reconciler := worker.NewReconciler(repo, publisher,
		cfg.Worker.ReconcileInterval, cfg.Worker.ReconcileAge, log)

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

		group.Go(func() error {
			defer func() { _ = consumer.Close() }()

			return consumer.Consume(groupCtx, c.queue, c.handler)
		})
	}

	group.Go(func() error { return reconciler.Run(groupCtx) })

	log.InfoContext(ctx, "воркер запущен", "env", cfg.App.Env)

	if err = group.Wait(); err != nil {
		return fmt.Errorf("worker: %w", err)
	}

	log.Info("воркер остановлен")

	return nil
}
