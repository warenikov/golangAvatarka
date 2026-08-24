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

	"go-avatar-service/internal/config"
	"go-avatar-service/internal/handlers/rest"
	"go-avatar-service/internal/logger"
	"go-avatar-service/internal/observability"
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

	registry := observability.NewRegistry()
	router := rest.NewRouter(rest.RouterDeps{
		Config:   cfg,
		Log:      log,
		Metrics:  observability.NewHTTP(registry),
		Registry: registry,
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
