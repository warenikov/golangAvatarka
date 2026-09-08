package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"go-avatar-service/internal/domain"
)

const avatarColumns = `id, user_id, file_name, mime_type, size_bytes, width, height,
	s3_key, thumbnail_s3_keys, upload_status, processing_status,
	created_at, updated_at, deleted_at`

type AvatarRepository struct {
	pool *pgxpool.Pool
}

// NewAvatarRepository создаёт репозиторий метаданных аватарок.
func NewAvatarRepository(pool *pgxpool.Pool) *AvatarRepository {
	return &AvatarRepository{pool: pool}
}

// Create сохраняет метаданные загруженной аватарки.
func (r *AvatarRepository) Create(ctx context.Context, a *domain.Avatar) error {
	thumbs, err := marshalThumbnails(a.ThumbnailS3Keys)
	if err != nil {
		return err
	}

	const q = `INSERT INTO avatars
		(id, user_id, file_name, mime_type, size_bytes, width, height,
		 s3_key, thumbnail_s3_keys, upload_status, processing_status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		RETURNING created_at, updated_at`

	err = r.pool.QueryRow(ctx, q,
		a.ID, a.UserID, a.FileName, a.MimeType, a.SizeBytes, a.Width, a.Height,
		a.S3Key, thumbs, a.UploadStatus, a.ProcessingStatus,
	).Scan(&a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create avatar %s: %w", a.ID, err)
	}

	return nil
}

// GetByID возвращает аватарку по идентификатору, исключая мягко удалённые.
func (r *AvatarRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Avatar, error) {
	q := `SELECT ` + avatarColumns + ` FROM avatars WHERE id = $1 AND deleted_at IS NULL`

	a, err := scanAvatar(r.pool.QueryRow(ctx, q, id))
	if err != nil {
		return nil, fmt.Errorf("get avatar %s: %w", id, err)
	}

	return a, nil
}

// GetCurrentByUserID возвращает последнюю загруженную аватарку пользователя.
func (r *AvatarRepository) GetCurrentByUserID(ctx context.Context, userID string) (*domain.Avatar, error) {
	q := `SELECT ` + avatarColumns + ` FROM avatars
		WHERE user_id = $1 AND deleted_at IS NULL
		ORDER BY created_at DESC
		LIMIT 1`

	a, err := scanAvatar(r.pool.QueryRow(ctx, q, userID))
	if err != nil {
		return nil, fmt.Errorf("get current avatar of %s: %w", userID, err)
	}

	return a, nil
}

// ListByUserID возвращает все аватарки пользователя, новые первыми.
func (r *AvatarRepository) ListByUserID(ctx context.Context, userID string) ([]domain.Avatar, error) {
	q := `SELECT ` + avatarColumns + ` FROM avatars
		WHERE user_id = $1 AND deleted_at IS NULL
		ORDER BY created_at DESC`

	rows, err := r.pool.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("list avatars of %s: %w", userID, err)
	}
	defer rows.Close()

	avatars := make([]domain.Avatar, 0)
	for rows.Next() {
		a, scanErr := scanAvatar(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("list avatars of %s: %w", userID, scanErr)
		}
		avatars = append(avatars, *a)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("list avatars of %s: %w", userID, err)
	}

	return avatars, nil
}

// SoftDelete помечает аватарку удалённой и возвращает ключи её объектов в хранилище.
func (r *AvatarRepository) SoftDelete(ctx context.Context, id uuid.UUID, userID string) ([]string, error) {
	const q = `UPDATE avatars
		SET deleted_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
		RETURNING s3_key, thumbnail_s3_keys`

	var (
		s3Key  string
		thumbs []byte
	)

	err := r.pool.QueryRow(ctx, q, id, userID).Scan(&s3Key, &thumbs)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, r.deleteConflict(ctx, id, userID)
	}
	if err != nil {
		return nil, fmt.Errorf("delete avatar %s: %w", id, err)
	}

	keys, err := unmarshalThumbnails(thumbs)
	if err != nil {
		return nil, fmt.Errorf("delete avatar %s: %w", id, err)
	}

	a := domain.Avatar{S3Key: s3Key, ThumbnailS3Keys: keys}

	return a.AllS3Keys(), nil
}

// deleteConflict уточняет, почему удаление не затронуло строк: аватарки нет или она чужая.
func (r *AvatarRepository) deleteConflict(ctx context.Context, id uuid.UUID, userID string) error {
	const q = `SELECT user_id FROM avatars WHERE id = $1 AND deleted_at IS NULL`

	var owner string
	err := r.pool.QueryRow(ctx, q, id).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("delete avatar %s: %w", id, domain.ErrAvatarNotFound)
	}
	if err != nil {
		return fmt.Errorf("delete avatar %s: %w", id, err)
	}
	if owner != userID {
		return fmt.Errorf("delete avatar %s by %s: %w", id, userID, domain.ErrForbidden)
	}

	return fmt.Errorf("delete avatar %s: %w", id, domain.ErrAvatarNotFound)
}

// UpdateProcessingResult сохраняет результат обработки. Повторный вызов для уже
// обработанной аватарки ничего не меняет и возвращает false.
func (r *AvatarRepository) UpdateProcessingResult(
	ctx context.Context, id uuid.UUID, thumbnails map[string]string, width, height int,
) (bool, error) {
	thumbs, err := marshalThumbnails(thumbnails)
	if err != nil {
		return false, err
	}

	const q = `UPDATE avatars
		SET thumbnail_s3_keys = $1, width = $2, height = $3,
		    processing_status = $4, updated_at = NOW()
		WHERE id = $5 AND processing_status <> $4`

	tag, err := r.pool.Exec(ctx, q, thumbs, width, height, domain.ProcessingStatusCompleted, id)
	if err != nil {
		return false, fmt.Errorf("update processing result %s: %w", id, err)
	}

	return tag.RowsAffected() > 0, nil
}

// SetProcessingStatus меняет статус обработки аватарки.
func (r *AvatarRepository) SetProcessingStatus(ctx context.Context, id uuid.UUID, status domain.ProcessingStatus) error {
	const q = `UPDATE avatars SET processing_status = $1, updated_at = NOW() WHERE id = $2`

	if _, err := r.pool.Exec(ctx, q, status, id); err != nil {
		return fmt.Errorf("set processing status %s: %w", id, err)
	}

	return nil
}

// ListPendingOlderThan возвращает аватарки, застрявшие в ожидании обработки.
func (r *AvatarRepository) ListPendingOlderThan(ctx context.Context, age time.Duration, limit int) ([]domain.Avatar, error) {
	q := `SELECT ` + avatarColumns + ` FROM avatars
		WHERE processing_status = $1 AND deleted_at IS NULL AND created_at < $2
		ORDER BY created_at
		LIMIT $3`

	rows, err := r.pool.Query(ctx, q, domain.ProcessingStatusPending, time.Now().Add(-age), limit)
	if err != nil {
		return nil, fmt.Errorf("list pending avatars: %w", err)
	}
	defer rows.Close()

	avatars := make([]domain.Avatar, 0, limit)
	for rows.Next() {
		a, scanErr := scanAvatar(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("list pending avatars: %w", scanErr)
		}
		avatars = append(avatars, *a)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("list pending avatars: %w", err)
	}

	return avatars, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAvatar(row rowScanner) (*domain.Avatar, error) {
	var (
		a      domain.Avatar
		thumbs []byte
	)

	err := row.Scan(
		&a.ID, &a.UserID, &a.FileName, &a.MimeType, &a.SizeBytes, &a.Width, &a.Height,
		&a.S3Key, &thumbs, &a.UploadStatus, &a.ProcessingStatus,
		&a.CreatedAt, &a.UpdatedAt, &a.DeletedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrAvatarNotFound
	}
	if err != nil {
		return nil, err
	}

	a.ThumbnailS3Keys, err = unmarshalThumbnails(thumbs)
	if err != nil {
		return nil, err
	}

	return &a, nil
}

func marshalThumbnails(m map[string]string) ([]byte, error) {
	if m == nil {
		m = map[string]string{}
	}

	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("marshal thumbnails: %w", err)
	}

	return b, nil
}

func unmarshalThumbnails(b []byte) (map[string]string, error) {
	m := map[string]string{}
	if len(b) == 0 {
		return m, nil
	}

	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("unmarshal thumbnails: %w", err)
	}

	return m, nil
}
