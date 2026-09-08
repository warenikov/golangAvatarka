package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

type HealthChecker struct {
	pool *pgxpool.Pool
}

// NewHealthChecker создаёт проверку доступности PostgreSQL для /health.
func NewHealthChecker(pool *pgxpool.Pool) *HealthChecker {
	return &HealthChecker{pool: pool}
}

// Name возвращает имя компонента в ответе /health.
func (h *HealthChecker) Name() string { return "postgres" }

// Check пингует базу данных.
func (h *HealthChecker) Check(ctx context.Context) error {
	if err := h.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping: %w", err)
	}

	return nil
}
