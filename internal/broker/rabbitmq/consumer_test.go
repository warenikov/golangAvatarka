package rabbitmq

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testExchange = "avatars.exchange"

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeAck запоминает решения по сообщению вместо обращения к брокеру.
type fakeAck struct {
	mu sync.Mutex

	acks    int
	nacks   int
	requeue bool
	err     error
}

func (f *fakeAck) Ack(uint64, bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acks++

	return f.err
}

func (f *fakeAck) Nack(_ uint64, _, requeue bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nacks++
	f.requeue = requeue

	return f.err
}

func (f *fakeAck) Reject(uint64, bool) error { return f.err }

func (f *fakeAck) counts() (acks, nacks int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.acks, f.nacks
}

// fakeChannel подменяет канал AMQP.
type fakeChannel struct {
	mu sync.Mutex

	deliveries <-chan amqp.Delivery
	consumeErr error
	publishErr error
	closeErr   error

	published []publishedMessage
	cancelled []string
	closed    bool
}

type publishedMessage struct {
	exchange string
	key      string
	msg      amqp.Publishing
}

func (f *fakeChannel) Qos(int, int, bool) error { return nil }

func (f *fakeChannel) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true

	return f.closeErr
}

func (f *fakeChannel) Cancel(consumer string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = append(f.cancelled, consumer)

	return nil
}

func (f *fakeChannel) ConsumeWithContext(
	context.Context, string, string, bool, bool, bool, bool, amqp.Table,
) (<-chan amqp.Delivery, error) {
	if f.consumeErr != nil {
		return nil, f.consumeErr
	}

	return f.deliveries, nil
}

func (f *fakeChannel) PublishWithContext(
	_ context.Context, exchange, key string, _, _ bool, msg amqp.Publishing,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.publishErr != nil {
		return f.publishErr
	}

	f.published = append(f.published, publishedMessage{exchange: exchange, key: key, msg: msg})

	return nil
}

func (f *fakeChannel) publishedMessages() []publishedMessage {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]publishedMessage(nil), f.published...)
}

func newTestConsumer(ch amqpChannel, maxRetries int) *Consumer {
	return &Consumer{
		channel:    ch,
		exchange:   testExchange,
		prefetch:   1,
		maxRetries: maxRetries,
		log:        testLogger(),
	}
}

func deliveryWithDeaths(ack amqp.Acknowledger, rejected int64, body string) amqp.Delivery {
	d := amqp.Delivery{
		Acknowledger: ack,
		MessageId:    "avatar-1",
		ContentType:  "application/json",
		Body:         []byte(body),
	}

	if rejected > 0 {
		d.Headers = amqp.Table{
			"x-death": []any{amqp.Table{"reason": deathReasonRejected, "count": rejected}},
		}
	}

	return d
}

func TestHandleDeliveryAcksOnSuccess(t *testing.T) {
	ack := &fakeAck{}
	ch := &fakeChannel{}
	c := newTestConsumer(ch, 3)

	var gotBody []byte
	c.handleDelivery(t.Context(), testLogger(), deliveryWithDeaths(ack, 0, "тело"),
		func(_ context.Context, body []byte) error {
			gotBody = body

			return nil
		})

	acks, nacks := ack.counts()
	assert.Equal(t, 1, acks)
	assert.Equal(t, 0, nacks)
	assert.Equal(t, "тело", string(gotBody))
	assert.Empty(t, ch.publishedMessages(), "успешное сообщение в очередь разбора не уходит")
}

func TestHandleDeliveryNacksForRetry(t *testing.T) {
	tests := []struct {
		name     string
		rejected int64
	}{
		{"первая попытка", 0},
		{"вторая попытка", 1},
		{"последняя попытка до лимита", 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ack := &fakeAck{}
			ch := &fakeChannel{}
			c := newTestConsumer(ch, 3)

			c.handleDelivery(t.Context(), testLogger(), deliveryWithDeaths(ack, tt.rejected, "{}"),
				func(context.Context, []byte) error { return errors.New("временный сбой") })

			acks, nacks := ack.counts()
			assert.Equal(t, 0, acks)
			assert.Equal(t, 1, nacks)
			assert.False(t, ack.requeue,
				"requeue=false обязателен: сообщение должно уйти в DLX, а не вернуться в ту же очередь")
			assert.Empty(t, ch.publishedMessages())
		})
	}
}

// Ключевой инвариант: после перекладывания в очередь разбора исходное сообщение
// подтверждается. Nack вместо Ack вернул бы его в цикл ретраев, а счётчик
// попыток при этом начался бы заново — сообщение крутилось бы вечно.
func TestHandleDeliverySendsToDeadLetterWhenRetriesExhausted(t *testing.T) {
	ack := &fakeAck{}
	ch := &fakeChannel{}
	c := newTestConsumer(ch, 3)

	delivery := deliveryWithDeaths(ack, 3, `{"avatar_id":"a-1"}`)

	c.handleDelivery(t.Context(), testLogger(), delivery,
		func(context.Context, []byte) error { return errors.New("битая картинка") })

	acks, nacks := ack.counts()
	assert.Equal(t, 1, acks, "исходное сообщение подтверждается")
	assert.Equal(t, 0, nacks, "повторный отказ вернул бы сообщение в ретраи со сброшенным счётчиком")

	published := ch.publishedMessages()
	require.Len(t, published, 1)
	assert.Equal(t, testExchange, published[0].exchange)
	assert.Equal(t, RoutingDead, published[0].key)
	assert.Equal(t, delivery.Body, published[0].msg.Body)
	assert.Equal(t, delivery.MessageId, published[0].msg.MessageId)
	assert.Equal(t, delivery.ContentType, published[0].msg.ContentType)
	assert.Equal(t, amqp.Persistent, published[0].msg.DeliveryMode,
		"очередь разбора должна пережить перезапуск брокера")
	assert.Equal(t, delivery.Headers, published[0].msg.Headers,
		"история x-death нужна для разбора причины")
}

func TestHandleDeliveryDeadLetterBoundary(t *testing.T) {
	tests := []struct {
		name         string
		rejected     int64
		maxRetries   int
		wantDeadLett bool
	}{
		{"на единицу меньше лимита", 2, 3, false},
		{"ровно лимит", 3, 3, true},
		{"больше лимита", 5, 3, true},
		{"лимит в одну попытку", 1, 1, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ack := &fakeAck{}
			ch := &fakeChannel{}
			c := newTestConsumer(ch, tt.maxRetries)

			c.handleDelivery(t.Context(), testLogger(), deliveryWithDeaths(ack, tt.rejected, "{}"),
				func(context.Context, []byte) error { return errors.New("сбой") })

			acks, nacks := ack.counts()
			if tt.wantDeadLett {
				assert.Len(t, ch.publishedMessages(), 1)
				assert.Equal(t, 1, acks)
				assert.Equal(t, 0, nacks)

				return
			}

			assert.Empty(t, ch.publishedMessages())
			assert.Equal(t, 0, acks)
			assert.Equal(t, 1, nacks)
		})
	}
}

// Если переложить не удалось, сообщение нельзя подтверждать: оно потеряется.
func TestHandleDeliveryNacksWhenDeadLetterPublishFails(t *testing.T) {
	ack := &fakeAck{}
	ch := &fakeChannel{publishErr: errors.New("брокер недоступен")}
	c := newTestConsumer(ch, 1)

	c.handleDelivery(t.Context(), testLogger(), deliveryWithDeaths(ack, 5, "{}"),
		func(context.Context, []byte) error { return errors.New("сбой") })

	acks, nacks := ack.counts()
	assert.Equal(t, 0, acks, "неопубликованное сообщение подтверждать нельзя — оно исчезнет")
	assert.Equal(t, 1, nacks)
}

// Сбой подтверждения не должен ронять потребителя: соединение переоткроют,
// сообщение придёт повторно, обработчик идемпотентен.
func TestHandleDeliverySurvivesAckFailure(t *testing.T) {
	tests := []struct {
		name     string
		rejected int64
		handler  Handler
	}{
		{
			name:    "успех",
			handler: func(context.Context, []byte) error { return nil },
		},
		{
			name:     "ретрай",
			handler:  func(context.Context, []byte) error { return errors.New("сбой") },
			rejected: 0,
		},
		{
			name:     "очередь разбора",
			handler:  func(context.Context, []byte) error { return errors.New("сбой") },
			rejected: 9,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ack := &fakeAck{err: errors.New("канал закрыт")}
			c := newTestConsumer(&fakeChannel{}, 3)

			assert.NotPanics(t, func() {
				c.handleDelivery(t.Context(), testLogger(), deliveryWithDeaths(ack, tt.rejected, "{}"), tt.handler)
			})
		})
	}
}

func TestConsumeReturnsErrorWhenSubscriptionFails(t *testing.T) {
	c := newTestConsumer(&fakeChannel{consumeErr: errors.New("очередь не существует")}, 3)

	err := c.Consume(t.Context(), "avatars.process", func(context.Context, []byte) error { return nil })

	require.Error(t, err)
	assert.Contains(t, err.Error(), "consume avatars.process")
}

func TestConsumeProcessesMessagesUntilContextCancelled(t *testing.T) {
	deliveries := make(chan amqp.Delivery, 2)
	ch := &fakeChannel{deliveries: deliveries}
	c := newTestConsumer(ch, 3)

	ack := &fakeAck{}
	deliveries <- deliveryWithDeaths(ack, 0, "первое")
	deliveries <- deliveryWithDeaths(ack, 0, "второе")

	handled := make(chan string, 2)
	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)
	go func() {
		done <- c.Consume(ctx, "avatars.process", func(_ context.Context, body []byte) error {
			handled <- string(body)

			return nil
		})
	}()

	assert.Equal(t, "первое", <-handled)
	assert.Equal(t, "второе", <-handled)

	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("потребитель не остановился по отмене контекста")
	}

	acks, _ := ack.counts()
	assert.Equal(t, 2, acks)

	ch.mu.Lock()
	defer ch.mu.Unlock()
	assert.Len(t, ch.cancelled, 1, "подписка должна быть снята при остановке")
}

// Брокер может закрыть канал доставки сам — потребитель обязан выйти без ошибки,
// чтобы воркер переподключился штатно.
func TestConsumeStopsWhenBrokerClosesChannel(t *testing.T) {
	deliveries := make(chan amqp.Delivery)
	c := newTestConsumer(&fakeChannel{deliveries: deliveries}, 3)

	done := make(chan error, 1)
	go func() {
		done <- c.Consume(t.Context(), "avatars.process", func(context.Context, []byte) error { return nil })
	}()

	close(deliveries)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("потребитель не заметил закрытия канала доставки")
	}
}

func TestConsumerClose(t *testing.T) {
	t.Run("успех", func(t *testing.T) {
		ch := &fakeChannel{}
		require.NoError(t, newTestConsumer(ch, 3).Close())

		ch.mu.Lock()
		defer ch.mu.Unlock()
		assert.True(t, ch.closed)
	})

	t.Run("ошибка оборачивается", func(t *testing.T) {
		c := newTestConsumer(&fakeChannel{closeErr: errors.New("канал уже закрыт")}, 3)

		err := c.Close()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "close consumer channel")
	})
}
