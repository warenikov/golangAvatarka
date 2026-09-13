package rabbitmq

import (
	"context"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// withTracing включает провайдер и пропагатор на время теста: без пропагатора
// Inject и Extract молча ничего не делают.
func withTracing(t *testing.T) {
	t.Helper()

	previousProvider := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()

	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample())))
	otel.SetTextMapPropagator(propagation.TraceContext{})

	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previousPropagator)
	})
}

func TestHeaderCarrier(t *testing.T) {
	carrier := headerCarrier(amqp.Table{
		"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"x-death":     []any{amqp.Table{"count": int64(1)}},
		"retry-count": int32(3),
	})

	assert.Equal(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", carrier.Get("traceparent"))
	assert.Empty(t, carrier.Get("отсутствует"))
	assert.Empty(t, carrier.Get("x-death"), "нестроковые заголовки в traceparent не годятся")
	assert.Empty(t, carrier.Get("retry-count"))
	assert.Len(t, carrier.Keys(), 3)

	carrier.Set("tracestate", "vendor=value")
	assert.Equal(t, "vendor=value", carrier.Get("tracestate"))
}

// Главное свойство спринта: работа воркера обязана попасть в тот же трейс,
// что и HTTP-запрос, породивший событие.
func TestTraceSurvivesTheBroker(t *testing.T) {
	withTracing(t)

	// Сторона публикации: спан запроса.
	producerCtx, producerSpan := otel.Tracer("test").Start(t.Context(), "http request")
	defer producerSpan.End()

	requestTraceID := producerSpan.SpanContext().TraceID()

	headers := injectTrace(producerCtx, nil)
	require.Contains(t, headers, "traceparent", "контекст должен уехать в заголовках сообщения")

	// Сторона потребления: другой процесс, пустой контекст.
	_, consumerSpan := startConsumeSpan(context.Background(), "avatars.process", amqp.Delivery{
		Headers:   headers,
		MessageId: "avatar-1",
	})
	defer consumerSpan.End()

	assert.Equal(t, requestTraceID, consumerSpan.SpanContext().TraceID(),
		"трейс обязан пережить границу брокера")
	assert.NotEqual(t, producerSpan.SpanContext().SpanID(), consumerSpan.SpanContext().SpanID(),
		"это отдельный спан, а не тот же самый")
}

// Реконсилятор переиздаёт события без исходного контекста — трейс начинается
// заново, но обработка всё равно должна трассироваться.
func TestConsumeWithoutTraceStartsOwnTrace(t *testing.T) {
	withTracing(t)

	_, span := startConsumeSpan(context.Background(), "avatars.process", amqp.Delivery{
		MessageId: "avatar-2",
	})
	defer span.End()

	assert.True(t, span.SpanContext().IsValid(), "спан должен быть создан и без заголовков")
}

func TestInjectTraceCreatesTableWhenMissing(t *testing.T) {
	withTracing(t)

	ctx, span := otel.Tracer("test").Start(t.Context(), "publish")
	defer span.End()

	headers := injectTrace(ctx, nil)
	require.NotNil(t, headers, "заголовки должны создаваться, если их не передали")
	assert.Contains(t, headers, "traceparent")
}

// Публикация не должна затирать уже проставленные заголовки — там едет x-death.
func TestInjectTraceKeepsExistingHeaders(t *testing.T) {
	withTracing(t)

	ctx, span := otel.Tracer("test").Start(t.Context(), "publish")
	defer span.End()

	headers := injectTrace(ctx, amqp.Table{"x-death": "исходное значение"})

	assert.Equal(t, "исходное значение", headers["x-death"])
	assert.Contains(t, headers, "traceparent")
}

// Повторная доставка приходит через очередь ретраев с TTL, спустя минуты,
// но с прежним traceparent. Потомком её делать нельзя: спан-родитель давно
// закрыт, и трейс распух бы на каждую попытку.
func TestRetryLinksInsteadOfNesting(t *testing.T) {
	withTracing(t)

	producerCtx, producerSpan := otel.Tracer("test").Start(t.Context(), "http request")
	defer producerSpan.End()

	originTraceID := producerSpan.SpanContext().TraceID()
	headers := injectTrace(producerCtx, nil)

	// Вторая попытка: RabbitMQ проставил x-death со счётчиком отказов.
	headers["x-death"] = []any{amqp.Table{"reason": deathReasonRejected, "count": int64(1)}}

	_, retrySpan := startConsumeSpan(context.Background(), "avatars.process", amqp.Delivery{
		Headers:   headers,
		MessageId: "avatar-retry",
	})
	defer retrySpan.End()

	assert.NotEqual(t, originTraceID, retrySpan.SpanContext().TraceID(),
		"повтор должен начинать свой трейс, а не продолжать давно закрытый")
	assert.True(t, retrySpan.SpanContext().IsValid())
}

// Первая доставка — наоборот, обязана быть частью исходного трейса.
func TestFirstDeliveryContinuesTrace(t *testing.T) {
	withTracing(t)

	producerCtx, producerSpan := otel.Tracer("test").Start(t.Context(), "http request")
	defer producerSpan.End()

	_, consumerSpan := startConsumeSpan(context.Background(), "avatars.process", amqp.Delivery{
		Headers:   injectTrace(producerCtx, nil),
		MessageId: "avatar-first",
	})
	defer consumerSpan.End()

	assert.Equal(t, producerSpan.SpanContext().TraceID(), consumerSpan.SpanContext().TraceID())
}

func TestConsumeSpanCarriesAttempt(t *testing.T) {
	withTracing(t)

	delivery := amqp.Delivery{
		MessageId: "avatar-3",
		Headers: amqp.Table{
			"x-death": []any{amqp.Table{"reason": deathReasonRejected, "count": int64(2)}},
		},
	}

	ctx, span := startConsumeSpan(context.Background(), "avatars.process", delivery)
	defer span.End()

	assert.True(t, trace.SpanContextFromContext(ctx).IsValid())
}
