// Package migrations содержит SQL-миграции схемы, вшитые в бинарь.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
