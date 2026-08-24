// Package logger настраивает структурированный логгер сервиса.
package logger

import (
	"fmt"
	"io"
	"log/slog"
)

// New возвращает JSON-логгер с указанным уровнем.
func New(level string, w io.Writer) (*slog.Logger, error) {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "info":
		lvl = slog.LevelInfo
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		return nil, fmt.Errorf("неизвестный уровень логирования %q", level)
	}

	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl})), nil
}
