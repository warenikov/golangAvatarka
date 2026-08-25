// Package web содержит статические файлы веб-интерфейса, вшитые в бинарь.
package web

import (
	"embed"
)

//go:embed static
var StaticFS embed.FS

//go:embed static/default-avatar.png
var DefaultAvatar []byte
