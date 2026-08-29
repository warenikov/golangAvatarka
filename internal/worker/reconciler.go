package worker

import (
	"context"
	"log/slog"
	"time"

	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/services"
)

const reconcileBatch = 100

type Reconciler struct {
	repo      Repository
	publisher services.EventPublisher
	interval  time.Duration
	age       time.Duration
	log       *slog.Logger
}

// NewReconciler создаёт фоновую задачу, которая находит аватарки, застрявшие
// в ожидании обработки, и публикует события повторно.
//
// Событие может не доехать до брокера: файл уже лежит в хранилище, метаданные
// в базе, а публикация упала. Без этой задачи такая аватарка осталась бы
// без миниатюр навсегда.
func NewReconciler(
	repo Repository, publisher services.EventPublisher,
	interval, age time.Duration, log *slog.Logger,
) *Reconciler {
	return &Reconciler{repo: repo, publisher: publisher, interval: interval, age: age, log: log}
}

// Run работает до отмены контекста.
func (r *Reconciler) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	r.log.InfoContext(ctx, "реконсилятор запущен",
		"interval", r.interval.String(), "age", r.age.String())

	for {
		select {
		case <-ctx.Done():
			r.log.Info("реконсилятор остановлен")

			return nil
		case <-ticker.C:
			r.reconcile(ctx)
		}
	}
}

func (r *Reconciler) reconcile(ctx context.Context) {
	stuck, err := r.repo.ListPendingOlderThan(ctx, r.age, reconcileBatch)
	if err != nil {
		r.log.ErrorContext(ctx, "не удалось выбрать застрявшие аватарки", "err", err)

		return
	}

	if len(stuck) == 0 {
		return
	}

	r.log.WarnContext(ctx, "найдены аватарки без обработки, публикуем события повторно", "count", len(stuck))

	for i := range stuck {
		avatar := &stuck[i]

		event := domain.AvatarUploadEvent{
			AvatarID: avatar.ID.String(),
			UserID:   avatar.UserID,
			S3Key:    avatar.S3Key,
		}

		if err = r.publisher.PublishUpload(ctx, event); err != nil {
			r.log.ErrorContext(ctx, "повторная публикация не удалась",
				"avatar_id", avatar.ID, "err", err)
		}
	}
}
