package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"go-avatar-service/internal/config"
)

const (
	connectAttempts   = 10
	connectBaseDelay  = 500 * time.Millisecond
	connectMaxDelay   = 10 * time.Second
	publishConfirmTTL = 5 * time.Second
)

var errConnectionClosed = errors.New("соединение с брокером закрыто")

// Connection — соединение с брокером, которое само восстанавливается после разрыва.
//
// Без восстановления разорванное соединение оставалось мёртвым навсегда: сервер
// после рестарта брокера выпадал из готовности, и вернуть его мог только ручной
// перезапуск пода. Встроенное восстановление библиотеки (Config.Recovery) не
// используется: оно заодно переподписывает потребителей, а воркер при разрыве
// намеренно завершается и поднимается заново с чистым состоянием.
type Connection struct {
	url      string
	topology topology
	log      *slog.Logger

	mu       sync.RWMutex
	conn     *amqp.Connection
	channel  *amqp.Channel
	confirms bool
	closed   bool

	stop chan struct{}
	done chan struct{}
}

// Connect подключается к брокеру и объявляет топологию, повторяя попытки
// с растущей задержкой: брокер в docker-compose поднимается дольше приложения.
// После подключения соединение следит за собой и восстанавливается при разрыве.
func Connect(ctx context.Context, cfg config.RabbitMQ, log *slog.Logger) (*Connection, error) {
	c := &Connection{
		url:      cfg.URL,
		topology: newTopology(cfg),
		log:      log,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}

	var err error

	delay := connectBaseDelay
	for attempt := 1; attempt <= connectAttempts; attempt++ {
		c.conn, c.channel, err = c.open()
		if err == nil {
			break
		}

		log.WarnContext(ctx, "брокер недоступен, повтор",
			"attempt", attempt, "of", connectAttempts, "delay", delay.String(), "err", err)

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}

		delay = min(delay*2, connectMaxDelay)
	}

	if err != nil {
		return nil, fmt.Errorf("connect after %d attempts: %w", connectAttempts, err)
	}

	go c.watch()

	return c, nil
}

// open устанавливает соединение, открывает служебный канал и объявляет топологию.
func (c *Connection) open() (*amqp.Connection, *amqp.Channel, error) {
	conn, err := amqp.Dial(c.url)
	if err != nil {
		return nil, nil, fmt.Errorf("dial: %w", err)
	}

	channel, err := conn.Channel()
	if err != nil {
		_ = conn.Close()

		return nil, nil, fmt.Errorf("open channel: %w", err)
	}

	if err = c.topology.declare(channel); err != nil {
		_ = conn.Close()

		return nil, nil, err
	}

	c.mu.RLock()
	confirms := c.confirms
	c.mu.RUnlock()

	if confirms {
		if err = channel.Confirm(false); err != nil {
			_ = conn.Close()

			return nil, nil, fmt.Errorf("enable publisher confirms: %w", err)
		}
	}

	return conn, channel, nil
}

// watch ждёт разрыва соединения и восстанавливает его, пока соединение не закрыто штатно.
func (c *Connection) watch() {
	defer close(c.done)

	for {
		c.mu.RLock()
		conn := c.conn
		c.mu.RUnlock()

		notify := conn.NotifyClose(make(chan *amqp.Error, 1))

		select {
		case <-c.stop:
			return
		case amqpErr := <-notify:
			select {
			case <-c.stop:
				return
			default:
			}

			c.log.Warn("соединение с брокером разорвано, переподключаемся", "err", amqpErr)

			if !c.reconnect() {
				return
			}
		}
	}
}

// reconnect повторяет подключение с растущей задержкой, пока не получится
// или пока соединение не закроют. Возвращает false, если его закрыли.
func (c *Connection) reconnect() bool {
	delay := connectBaseDelay

	for attempt := 1; ; attempt++ {
		select {
		case <-c.stop:
			return false
		case <-time.After(delay):
		}

		conn, channel, err := c.open()
		if err != nil {
			c.log.Warn("брокер недоступен, повтор",
				"attempt", attempt, "delay", delay.String(), "err", err)

			delay = min(delay*2, connectMaxDelay)

			continue
		}

		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			_ = conn.Close()

			return false
		}

		c.conn, c.channel = conn, channel
		c.mu.Unlock()

		c.log.Info("соединение с брокером восстановлено", "attempts", attempt)

		return true
	}
}

// enableConfirms включает подтверждения публикации на служебном канале.
// Настройка запоминается и повторяется на каждом новом канале после переподключения.
func (c *Connection) enableConfirms() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.confirms = true

	if err := c.channel.Confirm(false); err != nil {
		return fmt.Errorf("enable publisher confirms: %w", err)
	}

	return nil
}

// publishChannel возвращает текущий служебный канал для публикации.
func (c *Connection) publishChannel() *amqp.Channel {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.channel
}

// Close останавливает восстановление и закрывает канал и соединение с брокером.
func (c *Connection) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()

		return nil
	}

	c.closed = true
	close(c.stop)
	conn, channel := c.conn, c.channel
	c.mu.Unlock()

	<-c.done

	if err := channel.Close(); err != nil && !conn.IsClosed() {
		c.log.Warn("не удалось закрыть канал", "err", err)
	}

	if err := conn.Close(); err != nil && !errors.Is(err, amqp.ErrClosed) {
		return fmt.Errorf("close connection: %w", err)
	}

	return nil
}

// OpenChannel открывает отдельный канал. Канал AMQP не рассчитан на параллельное
// использование, поэтому каждому потребителю нужен свой.
func (c *Connection) OpenChannel() (*amqp.Channel, error) {
	c.mu.RLock()
	conn, closed := c.conn, c.closed
	c.mu.RUnlock()

	if closed {
		return nil, errConnectionClosed
	}

	ch, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("open channel: %w", err)
	}

	return ch, nil
}

// IsClosed сообщает, разорвано ли соединение с брокером прямо сейчас.
// Во время восстановления возвращает true, после — снова false.
func (c *Connection) IsClosed() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.closed || c.conn.IsClosed()
}
