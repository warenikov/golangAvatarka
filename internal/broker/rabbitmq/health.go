package rabbitmq

import (
	"context"
	"errors"
)

type HealthChecker struct {
	conn *Connection
}

// NewHealthChecker создаёт проверку доступности брокера для /health.
func NewHealthChecker(conn *Connection) *HealthChecker {
	return &HealthChecker{conn: conn}
}

// Name возвращает имя компонента в ответе /health.
func (h *HealthChecker) Name() string { return "rabbitmq" }

// Check проверяет, что соединение с брокером живо.
func (h *HealthChecker) Check(_ context.Context) error {
	if h.conn.IsClosed() {
		return errors.New("соединение с брокером разорвано")
	}

	return nil
}
