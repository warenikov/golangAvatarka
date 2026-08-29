// Package rest содержит HTTP-обработчики, middleware и сборку маршрутов публичного API.
package rest

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
)

type ErrorResponse struct {
	Error   string `json:"error"`
	Details string `json:"details,omitempty"`
}

type responder struct {
	log *slog.Logger
}

// JSON сериализует значение в тело ответа с указанным статусом.
func (rs responder) JSON(ctx context.Context, w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(v); err != nil {
		rs.log.ErrorContext(ctx, "запись тела ответа", "err", err)
	}
}

// Error отправляет ошибку в едином для API формате.
func (rs responder) Error(ctx context.Context, w http.ResponseWriter, status int, msg, details string) {
	rs.JSON(ctx, w, status, ErrorResponse{Error: msg, Details: details})
}
