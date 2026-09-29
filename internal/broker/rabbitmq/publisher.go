package rabbitmq

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"go-avatar-service/internal/breaker"
	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/observability"
)

type Publisher struct {
	conn    *Connection
	metrics *observability.Business
	breaker *breaker.Breaker
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

// WithBreaker пропускает публикации загрузок через выключатель: пока брокер недоступен,
// событие не ждёт подтверждения, а сразу уходит в ошибку и достаётся реконсилятору.
// Удаления идут в обход: их реконсилятор не переопубликует, и отклонённое
// выключателем событие оставило бы файлы в хранилище навсегда.
func (p *Publisher) WithBreaker(b *breaker.Breaker) *Publisher {
	p.breaker = b

	return p
}

// PublishUpload отправляет событие о загруженной аватарке.
func (p *Publisher) PublishUpload(ctx context.Context, event domain.AvatarUploadEvent) error {
	return p.publishTracked(ctx, p.breaker, RoutingUploaded, observability.EventUpload, event.AvatarID, event)
}

// PublishDelete отправляет событие об удалённой аватарке.
func (p *Publisher) PublishDelete(ctx context.Context, event domain.AvatarDeleteEvent) error {
	return p.publishTracked(ctx, nil, RoutingDeleted, observability.EventDelete, event.AvatarID, event)
}

// publishTracked публикует событие через выключатель b (nil — напрямую) и учитывает результат в метриках.
func (p *Publisher) publishTracked(
	ctx context.Context, b *breaker.Breaker, routingKey, kind, messageID string, payload any,
) error {
	err := b.Do(func() error { return p.publish(ctx, routingKey, messageID, payload) })

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
