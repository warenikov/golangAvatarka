package logger_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"go-avatar-service/internal/logger"
)

func entry(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()

	var v map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &v))

	return v
}

// Контекст со спаном: идентификаторы должны попасть в запись, иначе строку
// лога нечем связать с трейсом в Jaeger.
func TestLoggerAddsTraceIDs(t *testing.T) {
	traceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	require.NoError(t, err)

	spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	require.NoError(t, err)

	ctx := trace.ContextWithSpanContext(t.Context(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID,
		SpanID:  spanID,
		Remote:  true,
	}))

	var buf bytes.Buffer
	log, err := logger.New("info", &buf)
	require.NoError(t, err)

	log.InfoContext(ctx, "запрос обработан")

	got := entry(t, &buf)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", got["trace_id"])
	assert.Equal(t, "00f067aa0ba902b7", got["span_id"])
}

func TestLoggerWithoutTraceStaysClean(t *testing.T) {
	var buf bytes.Buffer

	log, err := logger.New("info", &buf)
	require.NoError(t, err)

	log.InfoContext(t.Context(), "старт")

	got := entry(t, &buf)
	assert.NotContains(t, got, "trace_id", "без трассировки лишних полей быть не должно")
	assert.NotContains(t, got, "span_id")
}

// Слои получают именованный логгер через With — обёртка обязана пережить это,
// иначе корреляция пропадёт ровно там, где она нужнее всего.
func TestCorrelationSurvivesWithAttrsAndGroup(t *testing.T) {
	traceID, err := trace.TraceIDFromHex("0af7651916cd43dd8448eb211c80319c")
	require.NoError(t, err)

	spanID, err := trace.SpanIDFromHex("b7ad6b7169203331")
	require.NoError(t, err)

	ctx := trace.ContextWithSpanContext(t.Context(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID,
		SpanID:  spanID,
	}))

	tests := []struct {
		name string
		with func(*testing.T, *bytes.Buffer) func(string)
	}{
		{
			name: "With",
			with: func(t *testing.T, buf *bytes.Buffer) func(string) {
				t.Helper()
				log, err := logger.New("info", buf)
				require.NoError(t, err)
				named := log.With("component", "repo.pg")

				return func(msg string) { named.InfoContext(ctx, msg) }
			},
		},
		{
			name: "WithGroup",
			with: func(t *testing.T, buf *bytes.Buffer) func(string) {
				t.Helper()
				log, err := logger.New("info", buf)
				require.NoError(t, err)
				grouped := log.WithGroup("db").With("table", "avatars")

				return func(msg string) { grouped.InfoContext(ctx, msg) }
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			write := tt.with(t, &buf)

			write("запрос к базе")

			got := entry(t, &buf)
			assert.Equal(t, "0af7651916cd43dd8448eb211c80319c", got["trace_id"])
			assert.Equal(t, "b7ad6b7169203331", got["span_id"])
		})
	}
}
