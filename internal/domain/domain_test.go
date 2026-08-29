package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-avatar-service/internal/domain"
)

func TestValidateUserID(t *testing.T) {
	tests := []struct {
		name    string
		userID  string
		wantErr bool
	}{
		{"обычный", "user-42", false},
		{"почта", "user.name@example.com", false},
		{"плюс и подчёркивание", "user+tag_1", false},
		{"максимальная длина", strings.Repeat("u", 255), false},
		{"пустой", "", true},
		{"длиннее лимита", strings.Repeat("u", 256), true},
		{"слеш — обход пути в ключе", "user/../other", true},
		{"пробел", "user 42", true},
		{"кириллица", "пользователь", true},
		{"нулевой байт", "user\x00", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := domain.ValidateUserID(tt.userID)
			if tt.wantErr {
				require.ErrorIs(t, err, domain.ErrInvalidUserID)

				return
			}

			require.NoError(t, err)
		})
	}
}

func TestObjectKeys(t *testing.T) {
	id := uuid.MustParse("11111111-2222-3333-4444-555555555555")

	assert.Equal(t, "avatars/user-1/11111111-2222-3333-4444-555555555555/original",
		domain.OriginalObjectKey("user-1", id))
	assert.Equal(t, "avatars/user-1/11111111-2222-3333-4444-555555555555/100x100.jpg",
		domain.ThumbnailObjectKey("user-1", id, domain.ThumbnailSmall))

	keys := domain.ThumbnailKeys("user-1", id)
	require.Len(t, keys, 2)
	assert.Equal(t, domain.ThumbnailObjectKey("user-1", id, domain.ThumbnailSmall), keys[domain.ThumbnailSmall])
	assert.Equal(t, domain.ThumbnailObjectKey("user-1", id, domain.ThumbnailLarge), keys[domain.ThumbnailLarge])
}

func TestAvatarState(t *testing.T) {
	deletedAt := time.Now()

	tests := []struct {
		name          string
		avatar        domain.Avatar
		wantProcessed bool
		wantDeleted   bool
	}{
		{
			name:          "обработана",
			avatar:        domain.Avatar{ProcessingStatus: domain.ProcessingStatusCompleted},
			wantProcessed: true,
		},
		{
			name:   "в очереди",
			avatar: domain.Avatar{ProcessingStatus: domain.ProcessingStatusPending},
		},
		{
			name:        "удалена",
			avatar:      domain.Avatar{DeletedAt: &deletedAt},
			wantDeleted: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantProcessed, tt.avatar.IsProcessed())
			assert.Equal(t, tt.wantDeleted, tt.avatar.IsDeleted())
		})
	}
}

func TestAvatarOwnedBy(t *testing.T) {
	a := domain.Avatar{UserID: "user-1"}

	assert.True(t, a.OwnedBy("user-1"))
	assert.False(t, a.OwnedBy("user-2"))
	assert.False(t, a.OwnedBy(""))
}

func TestAvatarThumbnailKey(t *testing.T) {
	a := domain.Avatar{ThumbnailS3Keys: map[string]string{domain.ThumbnailSmall: "small.jpg"}}

	key, ok := a.ThumbnailKey(domain.ThumbnailSmall)
	assert.True(t, ok)
	assert.Equal(t, "small.jpg", key)

	_, ok = a.ThumbnailKey(domain.ThumbnailLarge)
	assert.False(t, ok)
}

func TestAvatarAllS3Keys(t *testing.T) {
	a := domain.Avatar{
		S3Key: "original",
		ThumbnailS3Keys: map[string]string{
			domain.ThumbnailSmall: "small.jpg",
			domain.ThumbnailLarge: "large.jpg",
		},
	}

	keys := a.AllS3Keys()
	require.Len(t, keys, 3)
	assert.Equal(t, "original", keys[0], "оригинал должен идти первым")
	assert.ElementsMatch(t, []string{"original", "small.jpg", "large.jpg"}, keys)

	empty := domain.Avatar{S3Key: "original"}
	assert.Equal(t, []string{"original"}, empty.AllS3Keys())
}
