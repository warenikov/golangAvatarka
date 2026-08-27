// Package worker содержит фоновую обработку событий аватарок.
package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"go-avatar-service/internal/broker/rabbitmq"
	"go-avatar-service/internal/domain"
	"go-avatar-service/internal/services/imageproc"
)

var thumbnailSizes = map[string]int{
	domain.ThumbnailSmall: 100,
	domain.ThumbnailLarge: 300,
}

type Repository interface {
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Avatar, error)
	UpdateProcessingResult(ctx context.Context, id uuid.UUID, thumbnails map[string]string, width, height int) (bool, error)
	SetProcessingStatus(ctx context.Context, id uuid.UUID, status domain.ProcessingStatus) error
	ListPendingOlderThan(ctx context.Context, age time.Duration, limit int) ([]domain.Avatar, error)
}

type Processor struct {
	repo      Repository
	storage   domain.ObjectStorage
	maxPixels int64
	log       *slog.Logger
}

// NewProcessor создаёт обработчик событий обработки аватарок.
func NewProcessor(repo Repository, storage domain.ObjectStorage, maxPixels int64, log *slog.Logger) *Processor {
	return &Processor{repo: repo, storage: storage, maxPixels: maxPixels, log: log}
}

// HandleUpload строит миниатюры загруженной аватарки.
//
// Идемпотентен на трёх уровнях: уже обработанная аватарка пропускается сразу,
// ключи миниатюр детерминированы, а запись результата в БД не трогает строку
// со статусом completed.
func (p *Processor) HandleUpload(ctx context.Context, body []byte) error {
	event, err := rabbitmq.Decode[domain.AvatarUploadEvent](body)
	if err != nil {
		return err
	}

	avatarID, err := uuid.Parse(event.AvatarID)
	if err != nil {
		return fmt.Errorf("parse avatar id %q: %w", event.AvatarID, err)
	}

	log := p.log.With("avatar_id", event.AvatarID)

	avatar, err := p.repo.GetByID(ctx, avatarID)
	if errors.Is(err, domain.ErrAvatarNotFound) {
		log.InfoContext(ctx, "аватарка удалена до обработки, событие пропущено")

		return nil
	}
	if err != nil {
		return err
	}

	if avatar.IsProcessed() {
		log.InfoContext(ctx, "аватарка уже обработана, повтор пропущен")

		return nil
	}

	data, err := p.download(ctx, avatar.S3Key)
	if err != nil {
		return err
	}

	img, err := imageproc.Decode(data, p.maxPixels)
	if err != nil {
		// Повторы не помогут: файл не станет корректным. Помечаем провал и подтверждаем событие.
		log.ErrorContext(ctx, "изображение не удалось разобрать", "err", err)

		if statusErr := p.repo.SetProcessingStatus(ctx, avatarID, domain.ProcessingStatusFailed); statusErr != nil {
			return statusErr
		}

		return nil
	}

	thumbnails, err := p.uploadThumbnails(ctx, avatar, img)
	if err != nil {
		return err
	}

	width, height := imageproc.Dimensions(img)

	updated, err := p.repo.UpdateProcessingResult(ctx, avatarID, thumbnails, width, height)
	if err != nil {
		return err
	}

	log.InfoContext(ctx, "аватарка обработана",
		"width", width, "height", height, "thumbnails", len(thumbnails), "updated", updated)

	return nil
}

func (p *Processor) uploadThumbnails(ctx context.Context, avatar *domain.Avatar, img image.Image) (map[string]string, error) {
	thumbnails := make(map[string]string, len(thumbnailSizes))

	for name, side := range thumbnailSizes {
		var buf bytes.Buffer
		if err := imageproc.EncodeJPEG(&buf, imageproc.Thumbnail(img, side)); err != nil {
			return nil, err
		}

		key := domain.ThumbnailObjectKey(avatar.UserID, avatar.ID, name)
		if err := p.storage.Put(ctx, key, &buf, int64(buf.Len()), imageproc.ThumbnailMIME); err != nil {
			return nil, err
		}

		thumbnails[name] = key
	}

	return thumbnails, nil
}

func (p *Processor) download(ctx context.Context, key string) ([]byte, error) {
	obj, err := p.storage.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer func() { _ = obj.Body.Close() }()

	data, err := io.ReadAll(obj.Body)
	if err != nil {
		return nil, fmt.Errorf("read object %s: %w", key, err)
	}

	return data, nil
}

// HandleDelete удаляет файлы аватарки из хранилища.
func (p *Processor) HandleDelete(ctx context.Context, body []byte) error {
	event, err := rabbitmq.Decode[domain.AvatarDeleteEvent](body)
	if err != nil {
		return err
	}

	if err = p.storage.DeleteMany(ctx, event.S3Keys); err != nil {
		return err
	}

	p.log.InfoContext(ctx, "файлы аватарки удалены",
		"avatar_id", event.AvatarID, "keys", len(event.S3Keys))

	return nil
}
