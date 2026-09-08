package logger

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// Имена полей выбраны так, чтобы совпадать с тем, что ищут в OpenSearch
// и по чему связывают запись лога с трейсом в Jaeger.
const (
	fieldTraceID = "trace_id"
	fieldSpanID  = "span_id"
)

// traceHandler добавляет в каждую запись идентификаторы трейса из контекста.
//
// Это то, ради чего по всему коду вызывается ErrorContext, а не Error:
// без контекста связать строку лога с трейсом нечем, и расследование
// инцидента распадается на два несвязанных инструмента.
//
// Идентификаторы обязаны оставаться на верхнем уровне записи при любых
// With и WithGroup: поиск в OpenSearch идёт по фиксированному полю trace_id,
// и переезд его внутрь группы ломает выборку ровно тогда, когда она нужна.
// Поэтому при открытой группе цепочка вызовов переигрывается поверх
// обработчика, которому идентификаторы добавлены первыми.
type traceHandler struct {
	base slog.Handler
	// built — цепочка With/WithGroup, применённая заранее. Пересобирать её
	// на каждую запись означало бы аллокацию на каждую строку лога.
	built slog.Handler
	// chain нужна только когда открыта группа: там идентификаторы приходится
	// добавлять до неё, а значит собирать обработчик заново.
	chain   []handlerOp
	grouped bool
}

type handlerOp func(slog.Handler) slog.Handler

func newTraceHandler(base slog.Handler) traceHandler {
	return traceHandler{base: base, built: base}
}

// Enabled отвечает за уровень — решение принимает вложенный обработчик.
func (h traceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.built.Enabled(ctx, level)
}

// Handle дописывает trace_id и span_id, если запрос трассируется.
func (h traceHandler) Handle(ctx context.Context, record slog.Record) error {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return h.built.Handle(ctx, record)
	}

	attrs := []slog.Attr{
		slog.String(fieldTraceID, sc.TraceID().String()),
		slog.String(fieldSpanID, sc.SpanID().String()),
	}

	// Групп нет — вложенности не возникнет, добавляем прямо в запись.
	if !h.grouped {
		record.AddAttrs(attrs...)

		return h.built.Handle(ctx, record)
	}

	// Группа открыта: идентификаторы кладём до неё, затем повторяем цепочку.
	handler := h.base.WithAttrs(attrs)
	for _, op := range h.chain {
		handler = op(handler)
	}

	return handler.Handle(ctx, record)
}

func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}

	return h.append(func(inner slog.Handler) slog.Handler {
		return inner.WithAttrs(attrs)
	}, h.grouped)
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}

	return h.append(func(inner slog.Handler) slog.Handler {
		return inner.WithGroup(name)
	}, true)
}

// append возвращает копию с добавленным звеном цепочки.
func (h traceHandler) append(op handlerOp, grouped bool) traceHandler {
	chain := make([]handlerOp, len(h.chain), len(h.chain)+1)
	copy(chain, h.chain)

	return traceHandler{
		base:    h.base,
		built:   op(h.built),
		chain:   append(chain, op),
		grouped: grouped,
	}
}
