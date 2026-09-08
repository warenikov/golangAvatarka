package rabbitmq

import (
	"context"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"go-avatar-service/internal/observability"
)

// headerCarrier переносит контекст трассировки в заголовках сообщения AMQP.
//
// Без него трейс обрывается на границе брокера: HTTP-запрос заканчивается
// публикацией, а работа воркера начинается новым, ни с чем не связанным
// трейсом. Формат — W3C traceparent, тот же, что ходит по HTTP,
// поэтому обе половины склеиваются в одну картину запроса.
type headerCarrier amqp.Table

// Get возвращает значение заголовка. Заголовки AMQP типизированы, поэтому
// нестроковые значения игнорируются — подставить их в traceparent нельзя.
func (c headerCarrier) Get(key string) string {
	value, ok := c[key]
	if !ok {
		return ""
	}

	str, ok := value.(string)
	if !ok {
		return ""
	}

	return str
}

func (c headerCarrier) Set(key, value string) {
	c[key] = value
}

func (c headerCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for key := range c {
		keys = append(keys, key)
	}

	return keys
}

// injectTrace кладёт контекст трассировки в заголовки исходящего сообщения.
func injectTrace(ctx context.Context, headers amqp.Table) amqp.Table {
	if headers == nil {
		headers = amqp.Table{}
	}

	otel.GetTextMapPropagator().Inject(ctx, headerCarrier(headers))

	return headers
}

// startPublishSpan открывает спан публикации.
func startPublishSpan(ctx context.Context, exchange, routingKey, messageID string) (context.Context, trace.Span) {
	ctx, span := observability.Tracer().Start(ctx, "publish "+routingKey,
		trace.WithSpanKind(trace.SpanKindProducer),
	)

	span.SetAttributes(
		semconv.MessagingSystemRabbitMQ,
		semconv.MessagingDestinationName(exchange),
		semconv.MessagingMessageID(messageID),
		attribute.String("messaging.rabbitmq.routing_key", routingKey),
	)

	return ctx, span
}

// startConsumeSpan привязывает обработку к трейсу, породившему событие.
//
// Первая доставка становится потомком спана публикации: HTTP-запрос и работа
// воркера оказываются одним трейсом, и в Jaeger видна полная картина запроса.
//
// Повторная доставка — другое дело. Сообщение возвращается через очередь
// ретраев с TTL, то есть спустя десятки секунд или минуты, но заголовок
// traceparent в нём прежний. Сделать такую обработку потомком значило бы
// пририсовать к давно закрытому спану многоминутный хвост и растить один
// трейс с каждой попыткой. Поэтому повтор начинает свой трейс и связывается
// с исходным ссылкой: связь сохраняется, а длительности остаются честными.
//
// Сообщение вовсе без контекста — например, переизданное реконсилятором —
// тоже начинает трейс заново, связывать его не с чем.
func startConsumeSpan(ctx context.Context, queue string, d amqp.Delivery) (context.Context, trace.Span) {
	attempt := deathCount(d) + 1

	opts := make([]trace.SpanStartOption, 0, 3)
	opts = append(opts,
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			semconv.MessagingSystemRabbitMQ,
			semconv.MessagingDestinationName(queue),
			semconv.MessagingMessageID(d.MessageId),
			attribute.Int64("messaging.rabbitmq.delivery_attempt", attempt),
		),
	)

	parentCtx := otel.GetTextMapPropagator().Extract(ctx, headerCarrier(d.Headers))

	origin := trace.SpanContextFromContext(parentCtx)
	if !origin.IsValid() {
		return observability.Tracer().Start(ctx, "consume "+queue, opts...)
	}

	if attempt == 1 {
		return observability.Tracer().Start(parentCtx, "consume "+queue, opts...)
	}

	opts = append(opts, trace.WithLinks(trace.Link{SpanContext: origin}))

	return observability.Tracer().Start(ctx, "consume "+queue, opts...)
}
