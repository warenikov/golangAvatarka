// Package broker содержит транспорт событий обработки аватарок.
package broker

import (
	"context"
	"log/slog"

	"go-avatar-service/internal/domain"
)

type NoopPublisher struct {
	log *slog.Logger
}

// NewNoopPublisher создаёт публикатор-заглушку: события только логируются.
func NewNoopPublisher(log *slog.Logger) *NoopPublisher {
	return &NoopPublisher{log: log}
}

// PublishUpload логирует событие загрузки вместо отправки в брокер.
func (p *NoopPublisher) PublishUpload(ctx context.Context, event domain.AvatarUploadEvent) error {
	p.log.WarnContext(ctx, "брокер не подключён, событие загрузки пропущено", "avatar_id", event.AvatarID)

	return nil
}

// PublishDelete логирует событие удаления вместо отправки в брокер.
func (p *NoopPublisher) PublishDelete(ctx context.Context, event domain.AvatarDeleteEvent) error {
	p.log.WarnContext(ctx, "брокер не подключён, событие удаления пропущено", "avatar_id", event.AvatarID)

	return nil
}
