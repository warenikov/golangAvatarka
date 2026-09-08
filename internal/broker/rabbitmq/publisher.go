package rabbitmq

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/observability"
)

type Publisher struct {
	conn    *Connection
	metrics *observability.Business
	mu      sync.Mutex
}

// NewPublisher включает подтверждения публикации и возвращает публикатор событий.
func NewPublisher(conn *Connection) (*Publisher, error) {
	if err := conn.channel.Confirm(false); err != nil {
		return nil, fmt.Errorf("enable publisher confirms: %w", err)
	}

	return &Publisher{conn: conn}, nil
}

// WithMetrics подключает бизнес-метрики к издателю.
func (p *Publisher) WithMetrics(m *observability.Business) *Publisher {
	p.metrics = m

	return p
}

// PublishUpload отправляет событие о загруженной аватарке.
func (p *Publisher) PublishUpload(ctx context.Context, event domain.AvatarUploadEvent) error {
	return p.publishTracked(ctx, RoutingUploaded, observability.EventUpload, event.AvatarID, event)
}

// PublishDelete отправляет событие об удалённой аватарке.
func (p *Publisher) PublishDelete(ctx context.Context, event domain.AvatarDeleteEvent) error {
	return p.publishTracked(ctx, RoutingDeleted, observability.EventDelete, event.AvatarID, event)
}

// publishTracked публикует событие и учитывает результат в метриках.
func (p *Publisher) publishTracked(ctx context.Context, routingKey, kind, messageID string, payload any) error {
	err := p.publish(ctx, routingKey, messageID, payload)

	result := observability.ResultOK
	if err != nil {
		result = observability.ResultError
	}

	p.metrics.EventPublished(kind, result)

	return err
}

// publish отправляет сообщение и дожидается подтверждения брокера.
// MessageID равен идентификатору аватарки — по нему потребитель узнаёт повтор.
func (p *Publisher) publish(ctx context.Context, routingKey, messageID string, payload any) (err error) {
	ctx, span := startPublishSpan(ctx, p.conn.topology.exchange, routingKey, messageID)
	defer func() { observability.EndSpan(span, err) }()

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
			// Контекст трассировки едет вместе с сообщением: иначе работа
			// воркера окажется отдельным трейсом, не связанным с запросом.
			Headers: injectTrace(ctx, nil),
			Body:    body,
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
