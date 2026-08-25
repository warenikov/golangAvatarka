package rabbitmq

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const deathReasonRejected = "rejected"

// Handler обрабатывает одно сообщение. Возврат ошибки отправляет его в ретрай.
type Handler func(ctx context.Context, body []byte) error

type Consumer struct {
	channel    *amqp.Channel
	exchange   string
	prefetch   int
	maxRetries int
	log        *slog.Logger
}

// NewConsumer открывает отдельный канал и создаёт потребителя с ограничением
// на число одновременно обрабатываемых сообщений.
func NewConsumer(conn *Connection, prefetch, maxRetries int, log *slog.Logger) (*Consumer, error) {
	ch, err := conn.OpenChannel()
	if err != nil {
		return nil, err
	}

	if err = ch.Qos(prefetch, 0, false); err != nil {
		_ = ch.Close()

		return nil, fmt.Errorf("set qos: %w", err)
	}

	return &Consumer{
		channel:    ch,
		exchange:   conn.topology.exchange,
		prefetch:   prefetch,
		maxRetries: maxRetries,
		log:        log,
	}, nil
}

// Close закрывает канал потребителя.
func (c *Consumer) Close() error {
	if err := c.channel.Close(); err != nil {
		return fmt.Errorf("close consumer channel: %w", err)
	}

	return nil
}

// Consume читает очередь до отмены контекста, дожидаясь завершения начатых обработок.
func (c *Consumer) Consume(ctx context.Context, queue string, handle Handler) error {
	tag := fmt.Sprintf("%s-%d", queue, time.Now().UnixNano())

	deliveries, err := c.channel.ConsumeWithContext(ctx, queue, tag, false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("consume %s: %w", queue, err)
	}

	log := c.log.With("queue", queue)
	log.InfoContext(ctx, "потребитель запущен", "prefetch", c.prefetch)

	for {
		select {
		case <-ctx.Done():
			if cancelErr := c.channel.Cancel(tag, false); cancelErr != nil {
				log.Warn("не удалось отменить подписку", "err", cancelErr)
			}

			log.Info("потребитель остановлен")

			return nil

		case delivery, ok := <-deliveries:
			if !ok {
				log.Warn("канал доставки закрыт брокером")

				return nil
			}

			c.handleDelivery(ctx, log, delivery, handle)
		}
	}
}

func (c *Consumer) handleDelivery(ctx context.Context, log *slog.Logger, d amqp.Delivery, handle Handler) {
	log = log.With("message_id", d.MessageId, "attempt", deathCount(d)+1)

	err := handle(ctx, d.Body)
	if err == nil {
		if ackErr := d.Ack(false); ackErr != nil {
			log.ErrorContext(ctx, "не удалось подтвердить сообщение", "err", ackErr)
		}

		return
	}

	if deathCount(d) >= int64(c.maxRetries) {
		log.ErrorContext(ctx, "исчерпаны попытки, сообщение уходит в очередь разбора", "err", err)
		c.toDeadLetter(ctx, log, d)

		return
	}

	log.WarnContext(ctx, "обработка не удалась, сообщение уйдёт в ретрай", "err", err)

	if nackErr := d.Nack(false, false); nackErr != nil {
		log.ErrorContext(ctx, "не удалось отклонить сообщение", "err", nackErr)
	}
}

// toDeadLetter перекладывает сообщение в очередь разбора и подтверждает исходное,
// иначе оно вернулось бы в цикл ретраев уже без счётчика попыток.
func (c *Consumer) toDeadLetter(ctx context.Context, log *slog.Logger, d amqp.Delivery) {
	err := c.channel.PublishWithContext(ctx,
		c.exchange, RoutingDead, false, false,
		amqp.Publishing{
			ContentType:  d.ContentType,
			DeliveryMode: amqp.Persistent,
			MessageId:    d.MessageId,
			Timestamp:    time.Now(),
			Headers:      d.Headers,
			Body:         d.Body,
		})
	if err != nil {
		log.ErrorContext(ctx, "не удалось переложить сообщение в очередь разбора", "err", err)

		if nackErr := d.Nack(false, false); nackErr != nil {
			log.ErrorContext(ctx, "не удалось отклонить сообщение", "err", nackErr)
		}

		return
	}

	if ackErr := d.Ack(false); ackErr != nil {
		log.ErrorContext(ctx, "не удалось подтвердить сообщение", "err", ackErr)
	}
}

// deathCount читает число неудачных обработок из заголовка x-death, который
// RabbitMQ ведёт сам при перекладывании сообщения через dead-letter exchange.
//
// Учитываются только записи с причиной rejected — это отказы обработчика.
// Записи с причиной expired приходят из очереди ретраев по истечении TTL
// и означают возврат в работу, а не новую неудачу: суммирование всех записей
// расходовало бы бюджет попыток вдвое быстрее заявленного.
func deathCount(d amqp.Delivery) int64 {
	deaths, ok := d.Headers["x-death"].([]any)
	if !ok || len(deaths) == 0 {
		return 0
	}

	var total int64
	for _, entry := range deaths {
		death, isTable := entry.(amqp.Table)
		if !isTable {
			continue
		}

		reason, isStr := death["reason"].(string)
		if !isStr || reason != deathReasonRejected {
			continue
		}

		if count, isInt := death["count"].(int64); isInt {
			total += count
		}
	}

	return total
}

// Decode разбирает тело сообщения в указанную структуру.
func Decode[T any](body []byte) (T, error) {
	var payload T

	if err := json.Unmarshal(body, &payload); err != nil {
		return payload, fmt.Errorf("unmarshal event: %w", err)
	}

	return payload, nil
}
