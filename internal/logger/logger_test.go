package logger_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-avatar-service/internal/logger"
)

func TestNewLevels(t *testing.T) {
	tests := []struct {
		level        string
		wantDebug    bool
		wantInfo     bool
		wantErrLevel bool
	}{
		{level: "debug", wantDebug: true, wantInfo: true, wantErrLevel: true},
		{level: "info", wantInfo: true, wantErrLevel: true},
		{level: "warn", wantErrLevel: true},
		{level: "error", wantErrLevel: true},
	}

	for _, tt := range tests {
		t.Run(tt.level, func(t *testing.T) {
			var buf bytes.Buffer

			log, err := logger.New(tt.level, &buf)
			require.NoError(t, err)

			assert.Equal(t, tt.wantDebug, log.Enabled(t.Context(), slog.LevelDebug))
			assert.Equal(t, tt.wantInfo, log.Enabled(t.Context(), slog.LevelInfo))
			assert.Equal(t, tt.wantErrLevel, log.Enabled(t.Context(), slog.LevelError))
		})
	}
}

func TestNewRejectsUnknownLevel(t *testing.T) {
	log, err := logger.New("trace", &bytes.Buffer{})

	require.Error(t, err)
	assert.Nil(t, log)
	assert.Contains(t, err.Error(), "trace")
}

func TestNewWritesJSON(t *testing.T) {
	var buf bytes.Buffer

	log, err := logger.New("info", &buf)
	require.NoError(t, err)

	log.Info("сервис запущен", "component", "server", "port", 8080)

	var entry map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &entry))

	assert.Equal(t, "сервис запущен", entry["msg"])
	assert.Equal(t, "INFO", entry["level"])
	assert.Equal(t, "server", entry["component"])
	assert.Equal(t, float64(8080), entry["port"])
	assert.NotEmpty(t, entry["time"])
}
