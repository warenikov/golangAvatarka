package observability_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-avatar-service/internal/observability"
)

// freePort просит ядро выдать свободный порт: фиксированный номер в тестах
// конфликтует с параллельными прогонами и с занятыми портами разработчика.
func freePort(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	addr := listener.Addr().String()
	require.NoError(t, listener.Close())

	return addr
}

func discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Воркер не принимал трафика и не отдавал метрик — вся его часть показателей
// была невидима. Служебный сервер закрывает ровно это.
func TestAdminServerServesMetricsAndHealth(t *testing.T) {
	addr := freePort(t)

	reg := prometheus.NewRegistry()
	business := observability.NewBusiness(reg)
	business.AvatarDeleted()

	health := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}

	srv := observability.NewServer(addr, reg, health, discard())
	assert.Equal(t, addr, srv.Addr())

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})

	go func() {
		defer close(done)
		srv.Run(ctx)
	}()

	base := "http://" + addr
	waitReady(t, base+"/health")

	t.Run("метрики", func(t *testing.T) {
		body := get(t, base+"/metrics")
		assert.Contains(t, body, "avatar_deleted_total")
	})

	t.Run("состояние", func(t *testing.T) {
		body := get(t, base+"/health")
		assert.Contains(t, body, `"status":"ok"`)
	})

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("служебный сервер не остановился по отмене контекста")
	}
}

// Без проверки состояния сервер всё равно обязан отдавать метрики:
// процессу может быть нечего проверять, но показатели у него есть.
func TestAdminServerWithoutHealthHandler(t *testing.T) {
	addr := freePort(t)

	srv := observability.NewServer(addr, prometheus.NewRegistry(), nil, discard())

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go srv.Run(ctx)

	waitReady(t, "http://"+addr+"/metrics")

	resp, err := http.Get("http://" + addr + "/health")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func waitReady(t *testing.T, url string) {
	t.Helper()

	require.Eventually(t, func() bool {
		resp, err := http.Get(url)
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()

		return true
	}, 5*time.Second, 50*time.Millisecond, "служебный сервер не поднялся")
}

func get(t *testing.T, url string) string {
	t.Helper()

	resp, err := http.Get(url)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return string(body)
}
