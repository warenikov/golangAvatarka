// Package imageproc содержит преобразования изображений без обращений к внешним системам.
package imageproc

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"slices"

	"go-avatar-service/internal/domain"
)

// SniffLen — размер выборки, по которой определяется формат файла.
const SniffLen = 512

// Detect определяет MIME-тип по сигнатуре файла и возвращает читатель,
// из которого прочитанное начало доступно повторно.
func Detect(r io.Reader) (string, io.Reader, error) {
	head := make([]byte, SniffLen)

	n, err := io.ReadFull(r, head)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return "", nil, fmt.Errorf("read head: %w", err)
	}

	head = head[:n]

	return http.DetectContentType(head), io.MultiReader(bytes.NewReader(head), r), nil
}

// ValidateMIME проверяет, что формат файла разрешён к загрузке.
func ValidateMIME(mime string, allowed []string) error {
	if slices.Contains(allowed, mime) {
		return nil
	}

	return fmt.Errorf("%w: %s", domain.ErrInvalidFormat, mime)
}
