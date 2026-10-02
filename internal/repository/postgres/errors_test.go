package postgres_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"

	"go-avatar-service/internal/repository/postgres"
)

func TestIsDataError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"слишком длинное значение", &pgconn.PgError{Code: "22001"}, true},
		{"NUL в строке", fmt.Errorf("create avatar: %w", &pgconn.PgError{Code: "22021"}), true},
		{"нарушение уникальности", &pgconn.PgError{Code: "23505"}, true},
		{"база не принимает подключения", &pgconn.PgError{Code: "57P03"}, false},
		{"нехватка соединений", &pgconn.PgError{Code: "53300"}, false},
		{"сетевая ошибка", errors.New("connection refused"), false},
		{"нет ошибки", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, postgres.IsDataError(tt.err))
		})
	}
}
