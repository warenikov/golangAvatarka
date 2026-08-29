// Package postgres реализует хранение метаданных аватарок в PostgreSQL.
package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"go-avatar-service/internal/config"
	"go-avatar-service/migrations"
)

// NewPool создаёт пул соединений и проверяет доступность базы.
func NewPool(ctx context.Context, cfg config.DB) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}

	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MinConns = cfg.MinConns

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	if err = pool.Ping(ctx); err != nil {
		pool.Close()

		return nil, fmt.Errorf("ping: %w", err)
	}

	return pool, nil
}

// Migrate накатывает миграции, вшитые в бинарь.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	return withGoose(ctx, pool, goose.UpContext)
}

// MigrateDown откатывает последнюю применённую миграцию.
func MigrateDown(ctx context.Context, pool *pgxpool.Pool) error {
	return withGoose(ctx, pool, goose.DownContext)
}

func withGoose(ctx context.Context, pool *pgxpool.Pool, action func(context.Context, *sql.DB, string, ...goose.OptionsFunc) error) error {
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()

	goose.SetBaseFS(migrations.FS)
	defer goose.SetBaseFS(nil)

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set dialect: %w", err)
	}

	if err := action(ctx, db, "."); err != nil {
		return fmt.Errorf("goose: %w", err)
	}

	return nil
}
