package rest

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMatchesIfNoneMatch(t *testing.T) {
	const etag = `"abc123"`

	tests := []struct {
		name   string
		header string
		want   bool
	}{
		{"заголовка нет", "", false},
		{"точное совпадение", `"abc123"`, true},
		{"другой тег", `"other"`, false},

		// Звёздочка означает «любое представление, если оно есть».
		{"звёздочка", "*", true},
		{"звёздочка с пробелами", "  *  ", true},

		// Список: прокси и кеши присылают несколько сохранённых представлений.
		{"список, совпадение первым", `"abc123", "other"`, true},
		{"список, совпадение последним", `"x", "y", "abc123"`, true},
		{"список, совпадения нет", `"x", "y"`, false},
		{"список без пробелов", `"x","abc123"`, true},
		{"лишние запятые", `, "abc123" ,`, true},

		// Слабое сравнение: посредник мог ослабить тег.
		{"слабый в запросе", `W/"abc123"`, true},
		{"слабый в списке", `"x", W/"abc123"`, true},

		// Повреждённый заголовок не должен давать ложный 304.
		{"без кавычек", "abc123", false},
		{"незакрытая кавычка", `"abc123`, false},
		{"мусор", "!!!", false},
		{"только запятые", ",,,", false},
		{"пустые кавычки", `""`, false},
		{"мусор после валидного тега", `"x" мусор`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, matchesIfNoneMatch(tt.header, etag))
		})
	}
}

func TestScanETag(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		wantETag   string
		wantRemain string
	}{
		{"одиночный", `"abc"`, `"abc"`, ""},
		{"первый из списка", `"abc", "def"`, `"abc"`, `, "def"`},
		{"слабый", `W/"abc"`, `W/"abc"`, ""},
		{"с ведущими пробелами", `   "abc"`, `"abc"`, ""},
		{"без кавычек", `abc`, "", ""},
		{"пустая строка", "", "", ""},
		{"только W/", `W/`, "", ""},
		{"недопустимый символ внутри", "\"a\x01b\"", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			etag, remain := scanETag(tt.in)
			assert.Equal(t, tt.wantETag, etag)
			assert.Equal(t, tt.wantRemain, remain)
		})
	}
}

func TestWeakMatch(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{`"a"`, `"a"`, true},
		{`W/"a"`, `"a"`, true},
		{`"a"`, `W/"a"`, true},
		{`W/"a"`, `W/"a"`, true},
		{`"a"`, `"b"`, false},
	}

	for _, tt := range tests {
		t.Run(tt.a+" vs "+tt.b, func(t *testing.T) {
			assert.Equal(t, tt.want, weakMatch(tt.a, tt.b))
		})
	}
}
