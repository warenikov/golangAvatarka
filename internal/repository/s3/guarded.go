package s3

import (
	"context"
	"io"

	"go-avatar-service/internal/breaker"
	"go-avatar-service/internal/domain"
)

// WithBreaker пропускает все операции с объектами через выключатель.
// Проверка готовности идёт в обход него: она должна видеть хранилище, а не выключатель.
func (s *Storage) WithBreaker(b *breaker.Breaker) *Storage {
	s.breaker = b

	return s
}

// Put загружает объект в хранилище потоком, не читая его целиком в память.
func (s *Storage) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	return s.breaker.Do(func() error { return s.put(ctx, key, r, size, contentType) })
}

// Get возвращает объект из хранилища. Вызывающий обязан закрыть Body.
func (s *Storage) Get(ctx context.Context, key string) (*domain.Object, error) {
	return breaker.Call(s.breaker, func() (*domain.Object, error) { return s.get(ctx, key) })
}

// Delete удаляет объект. Удаление отсутствующего объекта считается успехом.
func (s *Storage) Delete(ctx context.Context, key string) error {
	return s.breaker.Do(func() error { return s.delete(ctx, key) })
}

// DeleteMany удаляет набор объектов одним пакетом.
func (s *Storage) DeleteMany(ctx context.Context, keys []string) error {
	return s.breaker.Do(func() error { return s.deleteMany(ctx, keys) })
}
