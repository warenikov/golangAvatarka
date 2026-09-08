// Package s3 реализует хранение файлов аватарок в S3-совместимом хранилище.
package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"go-avatar-service/internal/config"
	"go-avatar-service/internal/domain"
)

const codeNoSuchKey = "NoSuchKey"

var _ domain.ObjectStorage = (*Storage)(nil)

type Storage struct {
	client *minio.Client
	bucket string
}

// NewStorage подключается к хранилищу и создаёт бакет, если его ещё нет.
func NewStorage(ctx context.Context, cfg config.S3) (*Storage, error) {
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("create client: %w", err)
	}

	s := &Storage{client: client, bucket: cfg.Bucket}
	if err = s.ensureBucket(ctx, cfg.Region); err != nil {
		return nil, err
	}

	return s, nil
}

func (s *Storage) ensureBucket(ctx context.Context, region string) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("check bucket %s: %w", s.bucket, err)
	}
	if exists {
		return nil
	}

	if err = s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{Region: region}); err != nil {
		exists, existsErr := s.client.BucketExists(ctx, s.bucket)
		if existsErr == nil && exists {
			return nil
		}

		return fmt.Errorf("create bucket %s: %w", s.bucket, err)
	}

	return nil
}

// Put загружает объект в хранилище потоком, не читая его целиком в память.
func (s *Storage) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	if err := validateKey(key); err != nil {
		return err
	}

	_, err := s.client.PutObject(ctx, s.bucket, key, r, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return fmt.Errorf("put object %s: %w", key, err)
	}

	return nil
}

// Get возвращает объект из хранилища. Вызывающий обязан закрыть Body.
func (s *Storage) Get(ctx context.Context, key string) (*domain.Object, error) {
	if err := validateKey(key); err != nil {
		return nil, err
	}

	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("get object %s: %w", key, err)
	}

	info, err := obj.Stat()
	if err != nil {
		_ = obj.Close()

		if isNotFound(err) {
			return nil, fmt.Errorf("get object %s: %w", key, domain.ErrObjectNotFound)
		}

		return nil, fmt.Errorf("stat object %s: %w", key, err)
	}

	return &domain.Object{
		Body:        obj,
		ContentType: info.ContentType,
		Size:        info.Size,
		ETag:        info.ETag,
	}, nil
}

// Delete удаляет объект. Удаление отсутствующего объекта считается успехом.
func (s *Storage) Delete(ctx context.Context, key string) error {
	if err := validateKey(key); err != nil {
		return err
	}

	if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		if isNotFound(err) {
			return nil
		}

		return fmt.Errorf("delete object %s: %w", key, err)
	}

	return nil
}

// DeleteMany удаляет набор объектов одним пакетом.
func (s *Storage) DeleteMany(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}

	for _, key := range keys {
		if err := validateKey(key); err != nil {
			return err
		}
	}

	objects := make(chan minio.ObjectInfo, len(keys))
	for _, key := range keys {
		objects <- minio.ObjectInfo{Key: key}
	}
	close(objects)

	var errs []error
	for removeErr := range s.client.RemoveObjects(ctx, s.bucket, objects, minio.RemoveObjectsOptions{}) {
		if removeErr.Err != nil && !isNotFound(removeErr.Err) {
			errs = append(errs, fmt.Errorf("delete object %s: %w", removeErr.ObjectName, removeErr.Err))
		}
	}

	return errors.Join(errs...)
}

func validateKey(key string) error {
	switch {
	case key == "":
		return errors.New("пустой ключ объекта")
	case strings.HasPrefix(key, "/"):
		return fmt.Errorf("ключ объекта не должен начинаться со слэша: %q", key)
	case strings.Contains(key, ".."):
		return fmt.Errorf("ключ объекта не должен содержать \"..\": %q", key)
	case strings.Contains(key, "//"):
		return fmt.Errorf("ключ объекта не должен содержать пустых сегментов: %q", key)
	default:
		return nil
	}
}

func isNotFound(err error) bool {
	code := minio.ToErrorResponse(err).Code

	return code == codeNoSuchKey
}
