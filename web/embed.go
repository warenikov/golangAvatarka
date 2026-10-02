// Package web содержит статические файлы и шаблоны веб-интерфейса, вшитые в бинарь.
package web

import (
	"embed"
)

//go:embed static
var StaticFS embed.FS

//go:embed templates
var TemplatesFS embed.FS

//go:embed static/default-avatar.png
var DefaultAvatar []byte

// SwaggerPage — страница просмотра спецификации. Сами файлы Swagger UI
// лежат в static/swagger и отдаются обычным файловым обработчиком.
//
//go:embed static/swagger/index.html
var SwaggerPage []byte
