package rabbitmq

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"go-avatar-service/internal/domain"
)

type Publisher struct {
	conn *Connection
	mu   sync.Mutex
}

// NewPublisher включает подтверждения публикации и возвращает публикатор событий.
func NewPublisher(conn *Connection) (*Publisher, error) {
	if err := conn.channel.Confirm(false); err != nil {
		return nil, fmt.Errorf("enable publisher confirms: %w", err)
	}

	return &Publisher{conn: conn}, nil
}

// PublishUpload отправляет событие о загруженной аватарке.
func (p *Publisher) PublishUpload(ctx context.Context, event domain.AvatarUploadEvent) error {
	return p.publish(ctx, RoutingUploaded, event.AvatarID, event)
}

// PublishDelete отправляет событие об удалённой аватарке.
func (p *Publisher) PublishDelete(ctx context.Context, event domain.AvatarDeleteEvent) error {
	return p.publish(ctx, RoutingDeleted, event.AvatarID, event)
}

// publish отправляет сообщение и дожидается подтверждения брокера.
// MessageID равен идентификатору аватарки — по нему потребитель узнаёт повтор.
func (p *Publisher) publish(ctx context.Context, routingKey, messageID string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	confirm, err := p.conn.channel.PublishWithDeferredConfirmWithContext(ctx,
		p.conn.topology.exchange, routingKey, false, false,
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			MessageId:    messageID,
			Timestamp:    time.Now(),
			Body:         body,
		})
	if err != nil {
		return fmt.Errorf("publish %s: %w", routingKey, err)
	}

	confirmCtx, cancel := context.WithTimeout(ctx, publishConfirmTTL)
	defer cancel()

	acked, err := confirm.WaitContext(confirmCtx)
	if err != nil {
		return fmt.Errorf("wait confirm %s: %w", routingKey, err)
	}
	if !acked {
		return fmt.Errorf("брокер отклонил сообщение %s", messageID)
	}

	return nil
}
