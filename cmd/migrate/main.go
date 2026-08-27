// Команда migrate накатывает или откатывает миграции схемы и завершается.
//
// Отдельный бинарь нужен, чтобы схему меняла ровно одна задача, а не каждый
// экземпляр сервера при старте: при нескольких репликах это гонка, а сам факт
// изменения схемы прикладным процессом мешает откатывать релиз.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"go-avatar-service/internal/config"
	"go-avatar-service/internal/logger"
	"go-avatar-service/internal/repository/postgres"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "миграции не применены: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	direction := "up"
	if len(os.Args) > 1 {
		direction = os.Args[1]
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log, err := logger.New(cfg.App.LogLevel, os.Stdout)
	if err != nil {
		return err
	}
	log = log.With("service", "migrate")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.NewPool(ctx, cfg.DB)
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer pool.Close()

	log.InfoContext(ctx, "подключение к базе установлено", "dsn", cfg.DB.Redacted())

	switch direction {
	case "up":
		err = postgres.Migrate(ctx, pool)
	case "down":
		err = postgres.MigrateDown(ctx, pool)
	default:
		return fmt.Errorf("неизвестное направление %q: ожидается up или down", direction)
	}

	if err != nil {
		return fmt.Errorf("%s: %w", direction, err)
	}

	log.InfoContext(ctx, "миграции применены", "direction", direction)

	return nil
}
