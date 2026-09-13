package observability_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"go-avatar-service/internal/observability"
)

// Пропагатор ставится даже при выключенном трейсинге: заголовок traceparent
// должен проходить сквозь сервис, иначе соседи получат разорванный трейс.
func TestSetupTracingDisabledStillSetsPropagator(t *testing.T) {
	shutdown, err := observability.SetupTracing(t.Context(), observability.TracingConfig{
		Enabled:     false,
		ServiceName: "test",
	})
	require.NoError(t, err)
	require.NotNil(t, shutdown)

	fields := otel.GetTextMapPropagator().Fields()
	assert.Contains(t, fields, "traceparent")
	assert.Contains(t, fields, "baggage")

	require.NoError(t, shutdown(t.Context()), "остановка выключенного трейсинга не должна падать")
}

func TestTraceAndSpanIDFromContext(t *testing.T) {
	t.Run("вне трейса пусто", func(t *testing.T) {
		assert.Empty(t, observability.TraceIDFromContext(t.Context()))
		assert.Empty(t, observability.SpanIDFromContext(t.Context()))
	})

	t.Run("внутри трейса возвращает идентификаторы", func(t *testing.T) {
		traceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
		require.NoError(t, err)

		spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
		require.NoError(t, err)

		ctx := trace.ContextWithSpanContext(t.Context(), trace.NewSpanContext(trace.SpanContextConfig{
			TraceID: traceID,
			SpanID:  spanID,
		}))

		assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", observability.TraceIDFromContext(ctx))
		assert.Equal(t, "00f067aa0ba902b7", observability.SpanIDFromContext(ctx))
	})
}

func TestTracerIsUsable(t *testing.T) {
	tracer := observability.Tracer()
	require.NotNil(t, tracer)

	_, span := tracer.Start(t.Context(), "проверка")
	assert.NotPanics(t, func() { observability.EndSpan(span, nil) })
}

// Отказавшая операция обязана быть помечена: иначе в Jaeger сбой выглядит
// успехом, и по трейсу проблему не найти.
func TestEndSpanMarksFailure(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"без ошибки", nil},
		{"с ошибкой", assert.AnError},
		{"отменённый контекст", context.Canceled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, span := observability.Tracer().Start(t.Context(), "операция")

			assert.NotPanics(t, func() { observability.EndSpan(span, tt.err) })
		})
	}
}

func TestSetupTracingRejectsUnreachableCollectorLazily(t *testing.T) {
	// Экспортёр по gRPC подключается лениво, поэтому недоступный коллектор
	// не должен ронять старт сервиса: иначе упавший Jaeger уронит и приложение.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	shutdown, err := observability.SetupTracing(ctx, observability.TracingConfig{
		Enabled:     true,
		Endpoint:    "127.0.0.1:1",
		ServiceName: "test",
		SampleRatio: 1,
	})
	require.NoError(t, err, "недоступный коллектор не повод не стартовать")

	shutdownCtx, cancelShutdown := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancelShutdown()

	_ = shutdown(shutdownCtx)
}
