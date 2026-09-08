package domain

import "errors"

var (
	ErrAvatarNotFound = errors.New("avatar not found")
	ErrForbidden      = errors.New("forbidden")
	ErrInvalidFormat  = errors.New("invalid file format")
	ErrTooLarge       = errors.New("file too large")
)
