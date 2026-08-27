package s3

import (
	"context"
	"fmt"
)

type HealthChecker struct {
	storage *Storage
}

// NewHealthChecker создаёт проверку доступности хранилища для /health.
func NewHealthChecker(storage *Storage) *HealthChecker {
	return &HealthChecker{storage: storage}
}

// Name возвращает имя компонента в ответе /health.
func (h *HealthChecker) Name() string { return "s3" }

// Check проверяет, что бакет доступен.
func (h *HealthChecker) Check(ctx context.Context) error {
	if _, err := h.storage.client.BucketExists(ctx, h.storage.bucket); err != nil {
		return fmt.Errorf("bucket %s: %w", h.storage.bucket, err)
	}

	return nil
}
