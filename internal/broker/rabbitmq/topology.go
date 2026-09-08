// Package rabbitmq реализует транспорт событий обработки аватарок поверх RabbitMQ.
package rabbitmq

import (
	"fmt"
	"math"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"go-avatar-service/internal/config"
)

const (
	RoutingUploaded = "avatar.uploaded"
	RoutingDeleted  = "avatar.deleted"
	RoutingDead     = "avatar.dead"

	routingRetryProcess = "avatar.retry.process"
	routingRetryDelete  = "avatar.retry.delete"

	argDeadLetterExchange   = "x-dead-letter-exchange"
	argDeadLetterRoutingKey = "x-dead-letter-routing-key"
	argMessageTTL           = "x-message-ttl"
)

type topology struct {
	exchange     string
	queueProcess string
	queueDelete  string
	retryProcess string
	retryDelete  string
	queueDead    string
	retryTTL     int32
}

// retryTTLMillis переводит задержку ретрая в миллисекунды для аргумента x-message-ttl,
// который RabbitMQ принимает 32-битным числом.
func retryTTLMillis(d time.Duration) int32 {
	ms := d.Milliseconds()
	if ms > math.MaxInt32 {
		return math.MaxInt32
	}
	if ms < 0 {
		return 0
	}

	return int32(ms)
}

func newTopology(cfg config.RabbitMQ) topology {
	return topology{
		exchange:     cfg.Exchange,
		queueProcess: cfg.QueueProcess,
		queueDelete:  cfg.QueueDelete,
		retryProcess: cfg.QueueRetry + ".process",
		retryDelete:  cfg.QueueRetry + ".delete",
		queueDead:    cfg.QueueDead,
		retryTTL:     retryTTLMillis(cfg.RetryTTL),
	}
}

// declare создаёт обмен и очереди. Повторный вызов безопасен: объявления идемпотентны.
//
// Каждой рабочей очереди соответствует своя очередь ретраев: одна общая не подошла бы,
// потому что по истечении TTL сообщение надо вернуть именно в ту очередь, откуда оно
// пришло, а маршрутный ключ у обеих был бы одинаковый.
func (t topology) declare(ch *amqp.Channel) error {
	if err := ch.ExchangeDeclare(t.exchange, amqp.ExchangeTopic, true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare exchange %s: %w", t.exchange, err)
	}

	pairs := []struct {
		work       string
		workKey    string
		retry      string
		retryKey   string
		returnsVia string
	}{
		{t.queueProcess, RoutingUploaded, t.retryProcess, routingRetryProcess, RoutingUploaded},
		{t.queueDelete, RoutingDeleted, t.retryDelete, routingRetryDelete, RoutingDeleted},
	}

	for _, p := range pairs {
		workArgs := amqp.Table{
			argDeadLetterExchange:   t.exchange,
			argDeadLetterRoutingKey: p.retryKey,
		}
		if err := t.declareAndBind(ch, p.work, p.workKey, workArgs); err != nil {
			return err
		}

		retryArgs := amqp.Table{
			argDeadLetterExchange:   t.exchange,
			argDeadLetterRoutingKey: p.returnsVia,
			argMessageTTL:           t.retryTTL,
		}
		if err := t.declareAndBind(ch, p.retry, p.retryKey, retryArgs); err != nil {
			return err
		}
	}

	return t.declareAndBind(ch, t.queueDead, RoutingDead, nil)
}

func (t topology) declareAndBind(ch *amqp.Channel, queue, key string, args amqp.Table) error {
	if _, err := ch.QueueDeclare(queue, true, false, false, false, args); err != nil {
		return fmt.Errorf("declare queue %s: %w", queue, err)
	}

	if err := ch.QueueBind(queue, key, t.exchange, false, nil); err != nil {
		return fmt.Errorf("bind queue %s to %s: %w", queue, key, err)
	}

	return nil
}
