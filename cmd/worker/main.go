package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"go-avatar-service/internal/config"
	"go-avatar-service/internal/logger"
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

	log.InfoContext(ctx, "воркер запущен", "env", cfg.App.Env)

	<-ctx.Done()
	stop()

	log.Info("получен сигнал, воркер остановлен")

	return nil
}
