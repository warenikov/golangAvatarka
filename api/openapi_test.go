package openapi_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	openapi "go-avatar-service/api"
)

// Спецификация написана руками, а не сгенерирована из кода, поэтому сломать
// её можно незаметно: YAML с опечаткой остаётся валидным файлом, а битая
// ссылка проявляется только когда кто-то откроет /docs и увидит пустую схему.
func TestSpecParses(t *testing.T) {
	doc := parseSpec(t)

	assert.Equal(t, "3.1.0", doc["openapi"])

	paths, ok := doc["paths"].(map[string]any)
	require.True(t, ok, "раздел paths обязателен")

	// Полный список публичных маршрутов. Новый эндпоинт без описания
	// в спецификации — это контракт, о котором клиент не узнает.
	for _, path := range []string{
		"/api/v1/avatars",
		"/api/v1/avatars/{avatar_id}",
		"/api/v1/avatars/{avatar_id}/metadata",
		"/api/v1/users/{user_id}/avatar",
		"/api/v1/users/{user_id}/avatars",
		"/health",
		"/livez",
		"/readyz",
	} {
		assert.Contains(t, paths, path)
	}
}

// Внутренняя ссылка на несуществующий узел не мешает файлу разобраться:
// Swagger UI просто покажет пустое место там, где должна быть схема.
func TestSpecReferencesResolve(t *testing.T) {
	doc := parseSpec(t)

	refs := regexp.MustCompile(`#/([A-Za-z0-9/_-]+)`).FindAllStringSubmatch(string(openapi.Spec), -1)
	require.NotEmpty(t, refs)

	for _, ref := range refs {
		node := any(doc)

		for _, part := range strings.Split(ref[1], "/") {
			section, isMap := node.(map[string]any)
			require.True(t, isMap, "ссылка %s упирается в не-объект на %q", ref[0], part)

			value, exists := section[part]
			require.True(t, exists, "ссылка %s указывает на несуществующий %q", ref[0], part)

			node = value
		}
	}
}

// Метрики отдаются на служебном порту, и упоминание их среди публичных
// маршрутов означало бы, что спецификация зовёт клиента туда, где ему
// ответят 404.
func TestSpecDoesNotAdvertiseMetrics(t *testing.T) {
	doc := parseSpec(t)

	paths, ok := doc["paths"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, paths, "/metrics")
}

func parseSpec(t *testing.T) map[string]any {
	t.Helper()

	var doc map[string]any
	require.NoError(t, yaml.Unmarshal(openapi.Spec, &doc))

	return doc
}
