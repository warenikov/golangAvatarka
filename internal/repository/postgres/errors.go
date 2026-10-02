package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// Классы SQLSTATE, в которых база отвергла сами данные запроса.
const (
	sqlStateDataException       = "22"
	sqlStateIntegrityConstraint = "23"
)

// IsDataError сообщает, что база ответила и отвергла данные запроса:
// слишком длинное значение, недопустимый символ, нарушение ограничения.
// Это ответ исправной базы, а не её сбой.
func IsDataError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || len(pgErr.Code) < 2 {
		return false
	}

	class := pgErr.Code[:2]

	return class == sqlStateDataException || class == sqlStateIntegrityConstraint
}
