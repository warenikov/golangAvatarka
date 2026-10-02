package rabbitmq

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"go-avatar-service/internal/config"
	"go-avatar-service/internal/domain"
)

const brokerImage = "rabbitmq:4-management-alpine"

// startBroker поднимает RabbitMQ в контейнере и возвращает конфигурацию для подключения к нему.
func startBroker(t *testing.T) (testcontainers.Container, config.RabbitMQ) {
	t.Helper()

	if testing.Short() {
		t.Skip("нужен Docker: интеграционные тесты идут без -short")
	}

	ctx := context.Background()

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        brokerImage,
			ExposedPorts: []string{"5672/tcp"},
			WaitingFor:   wait.ForLog("Server startup complete").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5672/tcp")
	require.NoError(t, err)

	return container, config.RabbitMQ{
		URL:          fmt.Sprintf("amqp://guest:guest@%s:%s/", host, port.Port()),
		Exchange:     "avatars.exchange",
		QueueProcess: "avatars.process",
		QueueDelete:  "avatars.delete",
		QueueRetry:   "avatars.retry",
		QueueDead:    "avatars.dead",
		RetryTTL:     time.Second,
	}
}

// Брокер закрывает соединения сам — так же, как при рестарте или аларме.
// Раньше соединение после этого оставалось мёртвым навсегда, и сервер
// выпадал из готовности до ручного перезапуска пода.
func TestConnectionRecoversAfterBrokerClosesIt(t *testing.T) {
	container, cfg := startBroker(t)
	ctx := context.Background()

	conn, err := Connect(ctx, cfg, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	publisher, err := NewPublisher(conn)
	require.NoError(t, err)

	event := domain.AvatarUploadEvent{AvatarID: "a1", UserID: "u1", S3Key: "k"}
	require.NoError(t, publisher.PublishUpload(ctx, event))

	code, _, err := container.Exec(ctx, []string{"rabbitmqctl", "close_all_connections", "test"})
	require.NoError(t, err)
	require.Zero(t, code)

	require.Eventually(t, conn.IsClosed, 10*time.Second, 50*time.Millisecond,
		"проверка готовности должна увидеть разрыв")
	require.Eventually(t, func() bool { return !conn.IsClosed() }, 30*time.Second, 100*time.Millisecond,
		"соединение должно восстановиться само")

	require.NoError(t, publisher.PublishUpload(ctx, event),
		"публикация после восстановления идёт с подтверждением на новом канале")

	consumerCh, err := conn.OpenChannel()
	require.NoError(t, err, "новые каналы открываются на восстановленном соединении")
	_ = consumerCh.Close()
}

func TestConnectionCloseStopsRecovery(t *testing.T) {
	_, cfg := startBroker(t)

	conn, err := Connect(context.Background(), cfg, testLogger())
	require.NoError(t, err)

	require.NoError(t, conn.Close())
	require.NoError(t, conn.Close(), "повторное закрытие безопасно")

	assert.True(t, conn.IsClosed())

	_, err = conn.OpenChannel()
	require.ErrorIs(t, err, errConnectionClosed)
}
