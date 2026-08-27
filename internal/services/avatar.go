// Package services содержит бизнес-логику сервиса аватарок.
package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"go-avatar-service/internal/domain"
)

// orphanCleanupTimeout ограничивает уборку файла, оставшегося без метаданных.
const orphanCleanupTimeout = 5 * time.Second

type AvatarRepository interface {
	Create(ctx context.Context, a *domain.Avatar) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Avatar, error)
	GetCurrentByUserID(ctx context.Context, userID string) (*domain.Avatar, error)
	ListByUserID(ctx context.Context, userID string) ([]domain.Avatar, error)
	SoftDelete(ctx context.Context, id uuid.UUID, userID string) ([]string, error)
}

type EventPublisher interface {
	PublishUpload(ctx context.Context, event domain.AvatarUploadEvent) error
	PublishDelete(ctx context.Context, event domain.AvatarDeleteEvent) error
}

type UploadInput struct {
	UserID   string
	FileName string
	MimeType string
	Size     int64
	Body     io.Reader
}

type AvatarService struct {
	repo      AvatarRepository
	storage   domain.ObjectStorage
	publisher EventPublisher
	log       *slog.Logger
}

// NewAvatarService собирает сервис аватарок из его зависимостей.
func NewAvatarService(
	repo AvatarRepository,
	storage domain.ObjectStorage,
	publisher EventPublisher,
	log *slog.Logger,
) *AvatarService {
	return &AvatarService{repo: repo, storage: storage, publisher: publisher, log: log}
}

// Upload сохраняет файл в хранилище, заводит метаданные и ставит задачу на обработку.
func (s *AvatarService) Upload(ctx context.Context, in UploadInput) (*domain.Avatar, error) {
	if err := domain.ValidateUserID(in.UserID); err != nil {
		return nil, err
	}

	avatarID := uuid.New()
	key := domain.OriginalObjectKey(in.UserID, avatarID)

	if err := s.storage.Put(ctx, key, in.Body, in.Size, in.MimeType); err != nil {
		return nil, fmt.Errorf("upload avatar: %w", err)
	}

	avatar := &domain.Avatar{
		ID:               avatarID,
		UserID:           in.UserID,
		FileName:         in.FileName,
		MimeType:         in.MimeType,
		SizeBytes:        in.Size,
		S3Key:            key,
		ThumbnailS3Keys:  map[string]string{},
		UploadStatus:     domain.UploadStatusUploaded,
		ProcessingStatus: domain.ProcessingStatusPending,
	}

	if err := s.repo.Create(ctx, avatar); err != nil {
		s.removeOrphan(ctx, key, avatarID)

		return nil, fmt.Errorf("upload avatar: %w", err)
	}

	event := domain.AvatarUploadEvent{
		AvatarID: avatarID.String(),
		UserID:   in.UserID,
		S3Key:    key,
	}

	if err := s.publisher.PublishUpload(ctx, event); err != nil {
		s.log.ErrorContext(ctx, "событие загрузки не опубликовано",
			"avatar_id", avatarID, "err", err)
	}

	return avatar, nil
}

// removeOrphan убирает файл, на который не осталось ссылки в базе.
//
// Объект уже в хранилище, а метаданные не записались: реконсилятор обходит
// только строки БД и такой файл не найдёт никогда — он остался бы там навсегда.
// Уборка идёт на контексте без отмены: исходный запрос к этому моменту мог быть
// уже отменён, а мусор убрать всё равно нужно. Если и удаление не удалось,
// ошибка загрузки важнее — она и возвращается, а про файл остаётся запись в логе.
func (s *AvatarService) removeOrphan(ctx context.Context, key string, avatarID uuid.UUID) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), orphanCleanupTimeout)
	defer cancel()

	if err := s.storage.Delete(cleanupCtx, key); err != nil {
		s.log.ErrorContext(ctx, "в хранилище остался файл без метаданных",
			"avatar_id", avatarID, "key", key, "err", err)
	}
}

// Open открывает содержимое аватарки запрошенного размера.
// Если миниатюры ещё не готовы, отдаётся оригинал.
//
// Метаданные и содержимое разделены намеренно: ETag считается по метаданным,
// и условный запрос с совпавшим If-None-Match должен отвечать 304, ни разу
// не сходив в хранилище.
func (s *AvatarService) Open(ctx context.Context, avatar *domain.Avatar, size string) (*domain.Object, error) {
	key := avatar.S3Key

	if size != "" && size != domain.SizeOriginal {
		thumbKey, ok := avatar.ThumbnailKey(size)
		if ok {
			key = thumbKey
		}
	}

	obj, err := s.storage.Get(ctx, key)
	if errors.Is(err, domain.ErrObjectNotFound) && key != avatar.S3Key {
		obj, err = s.storage.Get(ctx, avatar.S3Key)
	}
	if err != nil {
		return nil, fmt.Errorf("get avatar content %s: %w", avatar.ID, err)
	}

	return obj, nil
}

// Metadata возвращает метаданные аватарки.
func (s *AvatarService) Metadata(ctx context.Context, id uuid.UUID) (*domain.Avatar, error) {
	return s.repo.GetByID(ctx, id)
}

// CurrentMetadata возвращает метаданные последней аватарки пользователя.
func (s *AvatarService) CurrentMetadata(ctx context.Context, userID string) (*domain.Avatar, error) {
	if err := domain.ValidateUserID(userID); err != nil {
		return nil, err
	}

	return s.repo.GetCurrentByUserID(ctx, userID)
}

// List возвращает все аватарки пользователя.
func (s *AvatarService) List(ctx context.Context, userID string) ([]domain.Avatar, error) {
	if err := domain.ValidateUserID(userID); err != nil {
		return nil, err
	}

	return s.repo.ListByUserID(ctx, userID)
}

// Delete помечает аватарку удалённой и ставит задачу на удаление файлов.
func (s *AvatarService) Delete(ctx context.Context, id uuid.UUID, userID string) error {
	if err := domain.ValidateUserID(userID); err != nil {
		return err
	}

	keys, err := s.repo.SoftDelete(ctx, id, userID)
	if err != nil {
		return err
	}

	event := domain.AvatarDeleteEvent{AvatarID: id.String(), S3Keys: keys}
	if err = s.publisher.PublishDelete(ctx, event); err != nil {
		s.log.ErrorContext(ctx, "событие удаления не опубликовано", "avatar_id", id, "err", err)
	}

	return nil
}

// DeleteCurrent удаляет последнюю аватарку пользователя.
func (s *AvatarService) DeleteCurrent(ctx context.Context, userID string) error {
	avatar, err := s.CurrentMetadata(ctx, userID)
	if err != nil {
		return err
	}

	return s.Delete(ctx, avatar.ID, userID)
}
