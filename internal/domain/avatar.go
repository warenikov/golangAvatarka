// Package domain содержит сущности предметной области и доменные ошибки.
package domain

import (
	"time"

	"github.com/google/uuid"
)

type UploadStatus string

const (
	UploadStatusUploading UploadStatus = "uploading"
	UploadStatusUploaded  UploadStatus = "uploaded"
	UploadStatusFailed    UploadStatus = "failed"
)

type ProcessingStatus string

const (
	ProcessingStatusPending    ProcessingStatus = "pending"
	ProcessingStatusProcessing ProcessingStatus = "processing"
	ProcessingStatusCompleted  ProcessingStatus = "completed"
	ProcessingStatusFailed     ProcessingStatus = "failed"
)

const (
	ThumbnailSmall = "100x100"
	ThumbnailLarge = "300x300"
	SizeOriginal   = "original"
)

type Avatar struct {
	ID               uuid.UUID
	UserID           string
	FileName         string
	MimeType         string
	SizeBytes        int64
	Width            *int
	Height           *int
	S3Key            string
	ThumbnailS3Keys  map[string]string
	UploadStatus     UploadStatus
	ProcessingStatus ProcessingStatus
	CreatedAt        time.Time
	UpdatedAt        time.Time
	DeletedAt        *time.Time
}

// IsProcessed сообщает, готовы ли миниатюры аватарки.
func (a Avatar) IsProcessed() bool {
	return a.ProcessingStatus == ProcessingStatusCompleted
}

// IsDeleted сообщает, помечена ли аватарка удалённой.
func (a Avatar) IsDeleted() bool {
	return a.DeletedAt != nil
}

// OwnedBy сообщает, принадлежит ли аватарка указанному пользователю.
func (a Avatar) OwnedBy(userID string) bool {
	return a.UserID == userID
}

// ThumbnailKey возвращает ключ миниатюры запрошенного размера.
func (a Avatar) ThumbnailKey(size string) (string, bool) {
	key, ok := a.ThumbnailS3Keys[size]

	return key, ok
}

// AllS3Keys возвращает ключи оригинала и всех миниатюр — для удаления из хранилища.
func (a Avatar) AllS3Keys() []string {
	keys := make([]string, 0, len(a.ThumbnailS3Keys)+1)
	keys = append(keys, a.S3Key)
	for _, key := range a.ThumbnailS3Keys {
		keys = append(keys, key)
	}

	return keys
}
