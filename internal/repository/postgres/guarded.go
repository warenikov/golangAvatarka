package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"

	"go-avatar-service/internal/breaker"
	"go-avatar-service/internal/domain"
)

// WithBreaker пропускает все запросы репозитория через выключатель.
func (r *AvatarRepository) WithBreaker(b *breaker.Breaker) *AvatarRepository {
	r.breaker = b

	return r
}

// Create сохраняет метаданные загруженной аватарки.
func (r *AvatarRepository) Create(ctx context.Context, a *domain.Avatar) error {
	return r.breaker.Do(func() error { return r.create(ctx, a) })
}

// GetByID возвращает аватарку по идентификатору, исключая мягко удалённые.
func (r *AvatarRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Avatar, error) {
	return breaker.Call(r.breaker, func() (*domain.Avatar, error) { return r.getByID(ctx, id) })
}

// GetCurrentByUserID возвращает последнюю загруженную аватарку пользователя.
func (r *AvatarRepository) GetCurrentByUserID(ctx context.Context, userID string) (*domain.Avatar, error) {
	return breaker.Call(r.breaker, func() (*domain.Avatar, error) { return r.getCurrentByUserID(ctx, userID) })
}

// ListByUserID возвращает все аватарки пользователя, новые первыми.
func (r *AvatarRepository) ListByUserID(ctx context.Context, userID string) ([]domain.Avatar, error) {
	return breaker.Call(r.breaker, func() ([]domain.Avatar, error) { return r.listByUserID(ctx, userID) })
}

// SoftDelete помечает аватарку удалённой и возвращает ключи её объектов в хранилище.
func (r *AvatarRepository) SoftDelete(ctx context.Context, id uuid.UUID, userID string) ([]string, error) {
	return breaker.Call(r.breaker, func() ([]string, error) { return r.softDelete(ctx, id, userID) })
}

// UpdateProcessingResult сохраняет результат обработки. Повторный вызов для уже
// обработанной аватарки ничего не меняет и возвращает false.
func (r *AvatarRepository) UpdateProcessingResult(
	ctx context.Context, id uuid.UUID, thumbnails map[string]string, width, height int,
) (bool, error) {
	return breaker.Call(r.breaker, func() (bool, error) {
		return r.updateProcessingResult(ctx, id, thumbnails, width, height)
	})
}

// SetProcessingStatus меняет статус обработки аватарки.
func (r *AvatarRepository) SetProcessingStatus(ctx context.Context, id uuid.UUID, status domain.ProcessingStatus) error {
	return r.breaker.Do(func() error { return r.setProcessingStatus(ctx, id, status) })
}

// ListPendingOlderThan возвращает аватарки, застрявшие в ожидании обработки.
func (r *AvatarRepository) ListPendingOlderThan(ctx context.Context, age time.Duration, limit int) ([]domain.Avatar, error) {
	return breaker.Call(r.breaker, func() ([]domain.Avatar, error) { return r.listPendingOlderThan(ctx, age, limit) })
}

// CountPendingOlderThan считает аватарки, застрявшие в ожидании обработки.
//
// Отдельный запрос нужен потому, что ListPendingOlderThan ограничен размером
// пачки: показывать в метрике отставания её потолок значит не отличать
// небольшую задержку от полной остановки воркера.
func (r *AvatarRepository) CountPendingOlderThan(ctx context.Context, age time.Duration) (int, error) {
	return breaker.Call(r.breaker, func() (int, error) { return r.countPendingOlderThan(ctx, age) })
}
