package observability

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// InstrumentationName — имя библиотеки инструментирования в спанах.
const InstrumentationName = "go-avatar-service"

const exporterTimeout = 5 * time.Second

// TracingConfig описывает подключение к коллектору трейсов.
type TracingConfig struct {
	Enabled     bool
	Endpoint    string
	ServiceName string
	Version     string
	Environment string
	SampleRatio float64
}

// ShutdownFunc завершает работу экспортёра, досылая накопленные спаны.
type ShutdownFunc func(context.Context) error

// SetupTracing настраивает глобального поставщика трейсов и распространение
// контекста между сервисами.
//
// Пропагатор ставится всегда, даже при выключенном трейсинге: заголовок
// traceparent должен проходить сквозь сервис и не теряться по дороге,
// иначе соседние сервисы получат разорванный трейс.
//
// Возвращённый ShutdownFunc обязателен к вызову: батчер копит спаны в памяти
// и без него теряет всё, что не успел отправить.
func SetupTracing(ctx context.Context, cfg TracingConfig) (ShutdownFunc, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	if !cfg.Enabled {
		return func(context.Context) error { return nil }, nil
	}

	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(cfg.Endpoint),
		// Коллектор находится внутри доверенного контура compose,
		// TLS между ним и сервисом не настраивается.
		otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithTimeout(exporterTimeout),
	)
	if err != nil {
		return nil, fmt.Errorf("otlp exporter: %w", err)
	}

	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.Version),
			semconv.DeploymentEnvironmentNameKey.String(cfg.Environment),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("build resource: %w", err)
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		// ParentBased: решение о сэмплировании принимает тот, кто начал трейс.
		// Иначе воркер мог бы отбросить продолжение уже записанного запроса,
		// и в Jaeger осталась бы половина картины.
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
	)

	otel.SetTracerProvider(provider)

	return func(shutdownCtx context.Context) error {
		if err := provider.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown tracer provider: %w", err)
		}

		return nil
	}, nil
}

// Tracer возвращает трассировщик сервиса.
func Tracer() trace.Tracer {
	return otel.Tracer(InstrumentationName)
}

// EndSpan закрывает спан, отметив ошибку, если она была.
//
// Без явной пометки спан остаётся зелёным даже у отказавшей операции,
// и в Jaeger сбой выглядит успехом.
func EndSpan(span trace.Span, err error) {
	if err != nil && !errors.Is(err, context.Canceled) {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}

	span.End()
}

// TraceIDFromContext возвращает идентификатор трейса для записи в лог.
// Пустая строка означает, что запрос не трассируется.
func TraceIDFromContext(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return ""
	}

	return sc.TraceID().String()
}

// SpanIDFromContext возвращает идентификатор текущего спана для записи в лог.
func SpanIDFromContext(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return ""
	}

	return sc.SpanID().String()
}

// Attr собирает строковый атрибут спана.
func Attr(key, value string) attribute.KeyValue {
	return attribute.String(key, value)
}
