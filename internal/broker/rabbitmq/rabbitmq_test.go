package rabbitmq

import (
	"math"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-avatar-service/internal/config"
	"go-avatar-service/internal/domain"
)

func TestRetryTTLMillis(t *testing.T) {
	tests := []struct {
		name string
		ttl  time.Duration
		want int32
	}{
		{"обычная задержка", 30 * time.Second, 30_000},
		{"нулевая", 0, 0},
		{"отрицательная приводится к нулю", -time.Second, 0},
		{"больше int32 обрезается", 30 * 24 * time.Hour, math.MaxInt32},
		{"ровно граница int32", time.Duration(math.MaxInt32) * time.Millisecond, math.MaxInt32},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, retryTTLMillis(tt.ttl))
		})
	}
}

func TestNewTopology(t *testing.T) {
	cfg := config.RabbitMQ{
		Exchange:     "avatars.exchange",
		QueueProcess: "avatars.process",
		QueueDelete:  "avatars.delete",
		QueueRetry:   "avatars.retry",
		QueueDead:    "avatars.dead",
		RetryTTL:     15 * time.Second,
	}

	top := newTopology(cfg)

	assert.Equal(t, "avatars.exchange", top.exchange)
	assert.Equal(t, "avatars.process", top.queueProcess)
	assert.Equal(t, "avatars.delete", top.queueDelete)
	assert.Equal(t, "avatars.retry.process", top.retryProcess)
	assert.Equal(t, "avatars.retry.delete", top.retryDelete)
	assert.Equal(t, "avatars.dead", top.queueDead)
	assert.Equal(t, int32(15_000), top.retryTTL)
}

func TestDeathCount(t *testing.T) {
	death := func(reason string, count int64) amqp.Table {
		return amqp.Table{"reason": reason, "count": count}
	}

	tests := []struct {
		name    string
		headers amqp.Table
		want    int64
	}{
		{"первая доставка", nil, 0},
		{"пустой заголовок", amqp.Table{"x-death": []any{}}, 0},
		{"заголовок не того типа", amqp.Table{"x-death": "нет"}, 0},
		{"один отказ", amqp.Table{"x-death": []any{death("rejected", 1)}}, 1},
		{
			name:    "истечение TTL не считается попыткой",
			headers: amqp.Table{"x-death": []any{death("rejected", 2), death("expired", 2)}},
			want:    2,
		},
		{
			name:    "несколько отказов суммируются",
			headers: amqp.Table{"x-death": []any{death("rejected", 2), death("rejected", 3)}},
			want:    5,
		},
		{
			name:    "мусор в записях пропускается",
			headers: amqp.Table{"x-death": []any{"строка", amqp.Table{"reason": 42}, death("rejected", 1)}},
			want:    1,
		},
		{
			name:    "count не того типа игнорируется",
			headers: amqp.Table{"x-death": []any{amqp.Table{"reason": "rejected", "count": "два"}}},
			want:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, deathCount(amqp.Delivery{Headers: tt.headers}))
		})
	}
}

func TestDecode(t *testing.T) {
	t.Run("событие загрузки", func(t *testing.T) {
		event, err := Decode[domain.AvatarUploadEvent](
			[]byte(`{"avatar_id":"a-1","user_id":"user-1","s3_key":"avatars/user-1/a-1/original"}`))
		require.NoError(t, err)

		assert.Equal(t, "a-1", event.AvatarID)
		assert.Equal(t, "user-1", event.UserID)
		assert.Equal(t, "avatars/user-1/a-1/original", event.S3Key)
	})

	t.Run("событие удаления", func(t *testing.T) {
		event, err := Decode[domain.AvatarDeleteEvent]([]byte(`{"avatar_id":"a-1","s3_keys":["k1","k2"]}`))
		require.NoError(t, err)

		assert.Equal(t, "a-1", event.AvatarID)
		assert.Equal(t, []string{"k1", "k2"}, event.S3Keys)
	})

	t.Run("битый JSON", func(t *testing.T) {
		_, err := Decode[domain.AvatarUploadEvent]([]byte("{не json"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unmarshal event")
	})
}
