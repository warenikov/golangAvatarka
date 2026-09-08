package rabbitmq

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"go-avatar-service/internal/config"
)

const (
	connectAttempts   = 10
	connectBaseDelay  = 500 * time.Millisecond
	connectMaxDelay   = 10 * time.Second
	publishConfirmTTL = 5 * time.Second
)

type Connection struct {
	conn     *amqp.Connection
	channel  *amqp.Channel
	topology topology
	log      *slog.Logger
}

// Connect подключается к брокеру и объявляет топологию, повторяя попытки
// с растущей задержкой: брокер в docker-compose поднимается дольше приложения.
func Connect(ctx context.Context, cfg config.RabbitMQ, log *slog.Logger) (*Connection, error) {
	var (
		conn *amqp.Connection
		err  error
	)

	delay := connectBaseDelay
	for attempt := 1; attempt <= connectAttempts; attempt++ {
		conn, err = amqp.Dial(cfg.URL)
		if err == nil {
			break
		}

		log.WarnContext(ctx, "брокер недоступен, повтор",
			"attempt", attempt, "of", connectAttempts, "delay", delay.String(), "err", err)

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}

		delay = min(delay*2, connectMaxDelay)
	}

	if err != nil {
		return nil, fmt.Errorf("connect after %d attempts: %w", connectAttempts, err)
	}

	channel, err := conn.Channel()
	if err != nil {
		_ = conn.Close()

		return nil, fmt.Errorf("open channel: %w", err)
	}

	t := newTopology(cfg)
	if err = t.declare(channel); err != nil {
		_ = channel.Close()
		_ = conn.Close()

		return nil, err
	}

	return &Connection{conn: conn, channel: channel, topology: t, log: log}, nil
}

// Close закрывает канал и соединение с брокером.
func (c *Connection) Close() error {
	if err := c.channel.Close(); err != nil && !c.conn.IsClosed() {
		c.log.Warn("не удалось закрыть канал", "err", err)
	}

	if err := c.conn.Close(); err != nil {
		return fmt.Errorf("close connection: %w", err)
	}

	return nil
}

// OpenChannel открывает отдельный канал. Канал AMQP не рассчитан на параллельное
// использование, поэтому каждому потребителю нужен свой.
func (c *Connection) OpenChannel() (*amqp.Channel, error) {
	ch, err := c.conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("open channel: %w", err)
	}

	return ch, nil
}

// IsClosed сообщает, разорвано ли соединение с брокером.
func (c *Connection) IsClosed() bool { return c.conn.IsClosed() }
