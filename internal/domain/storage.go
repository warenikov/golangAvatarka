package domain

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"

	"github.com/google/uuid"
)

var (
	ErrObjectNotFound = errors.New("object not found")
	ErrInvalidUserID  = errors.New("invalid user id")
)

const maxUserIDLen = 255

var userIDPattern = regexp.MustCompile(`^[A-Za-z0-9._@+-]+$`)

// Object — содержимое объекта из хранилища. Body обязателен к закрытию вызывающим.
type Object struct {
	Body        io.ReadCloser
	ContentType string
	Size        int64
	ETag        string
}

// ObjectStorage — хранилище файлов аватарок.
type ObjectStorage interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	Get(ctx context.Context, key string) (*Object, error)
	Delete(ctx context.Context, key string) error
	DeleteMany(ctx context.Context, keys []string) error
}

// ValidateUserID проверяет, что идентификатор пользователя безопасно подставлять в ключ хранилища.
func ValidateUserID(userID string) error {
	if userID == "" {
		return fmt.Errorf("%w: пустой идентификатор", ErrInvalidUserID)
	}
	if len(userID) > maxUserIDLen {
		return fmt.Errorf("%w: длиннее %d символов", ErrInvalidUserID, maxUserIDLen)
	}
	if !userIDPattern.MatchString(userID) {
		return fmt.Errorf("%w: допустимы только буквы, цифры и . _ @ + -", ErrInvalidUserID)
	}

	return nil
}

// OriginalObjectKey возвращает ключ оригинала аватарки в хранилище.
func OriginalObjectKey(userID string, avatarID uuid.UUID) string {
	return fmt.Sprintf("avatars/%s/%s/original", userID, avatarID)
}

// ThumbnailObjectKey возвращает ключ миниатюры указанного размера.
func ThumbnailObjectKey(userID string, avatarID uuid.UUID, size string) string {
	return fmt.Sprintf("avatars/%s/%s/%s.jpg", userID, avatarID, size)
}

// ThumbnailKeys возвращает ключи всех миниатюр, которые создаёт воркер.
func ThumbnailKeys(userID string, avatarID uuid.UUID) map[string]string {
	return map[string]string{
		ThumbnailSmall: ThumbnailObjectKey(userID, avatarID, ThumbnailSmall),
		ThumbnailLarge: ThumbnailObjectKey(userID, avatarID, ThumbnailLarge),
	}
}
