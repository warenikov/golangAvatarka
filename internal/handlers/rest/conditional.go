package rest

import (
	"net/textproto"
	"strings"
)

// matchesIfNoneMatch сообщает, можно ли ответить 304 на условный запрос.
//
// Заголовок по RFC 9110 §13.1.2 — это не одно значение: он бывает "*",
// списком тегов через запятую и может содержать слабые теги вида W/"...".
// Сравнение строк целиком пропускало все три случая, и клиент получал полное
// тело вместо 304.
//
// Алгоритм повторяет checkIfNoneMatch из net/http/fs.go: разбор там закрыт
// неэкспортируемыми scanETag и etagWeakMatch, переиспользовать их нельзя.
// Сравнение слабое — так предписывает спецификация именно для If-None-Match.
func matchesIfNoneMatch(header, etag string) bool {
	if header == "" {
		return false
	}

	buf := header
	for {
		buf = textproto.TrimString(buf)
		if buf == "" {
			return false
		}

		if buf[0] == ',' {
			buf = buf[1:]

			continue
		}

		// "*" совпадает с любым представлением, если оно вообще существует.
		if buf[0] == '*' {
			return true
		}

		candidate, remain := scanETag(buf)
		if candidate == "" {
			return false
		}

		if weakMatch(candidate, etag) {
			return true
		}

		buf = remain
	}
}

// scanETag отделяет первый тег от остатка списка.
// Пустой результат означает, что дальше разбирать нечего: заголовок повреждён.
func scanETag(s string) (etag, remain string) {
	s = textproto.TrimString(s)

	start := 0
	if strings.HasPrefix(s, "W/") {
		start = 2
	}

	if len(s[start:]) < 2 || s[start] != '"' {
		return "", ""
	}

	for i := start + 1; i < len(s); i++ {
		c := s[i]
		switch {
		// Допустимые в теге символы, RFC 9110 §8.8.3.
		case c == 0x21, c >= 0x23 && c <= 0x7E, c >= 0x80:
		case c == '"':
			return s[:i+1], s[i+1:]
		default:
			return "", ""
		}
	}

	return "", ""
}

// weakMatch сравнивает теги по слабой эквивалентности: префикс W/ не влияет
// на результат.
func weakMatch(a, b string) bool {
	return strings.TrimPrefix(a, "W/") == strings.TrimPrefix(b, "W/")
}
