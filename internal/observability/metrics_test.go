package observability_test

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-avatar-service/internal/observability"
)

func TestNewHTTPRegistersMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := observability.NewHTTP(reg)

	m.Requests.WithLabelValues("GET", "/api/v1/avatars/{avatar_id}", "200").Inc()
	m.Duration.WithLabelValues("GET", "/api/v1/avatars/{avatar_id}").Observe(0.05)
	m.InFlight.Inc()

	assert.Equal(t, 1, testutil.CollectAndCount(m.Requests))
	assert.Equal(t, 1, testutil.CollectAndCount(m.Duration))
	assert.InDelta(t, 1.0, testutil.ToFloat64(m.InFlight), 0.0001)

	families, err := reg.Gather()
	require.NoError(t, err)

	names := make([]string, 0, len(families))
	for _, f := range families {
		names = append(names, f.GetName())
	}

	assert.Contains(t, names, "http_requests_total")
	assert.Contains(t, names, "http_request_duration_seconds")
	assert.Contains(t, names, "http_requests_in_flight")
}

func TestNewHTTPPanicsOnDoubleRegistration(t *testing.T) {
	reg := prometheus.NewRegistry()
	observability.NewHTTP(reg)

	assert.Panics(t, func() { observability.NewHTTP(reg) },
		"повторная регистрация метрик — ошибка сборки приложения, а не рантайма")
}

func TestNewRegistryHasRuntimeCollectors(t *testing.T) {
	reg := observability.NewRegistry()

	families, err := reg.Gather()
	require.NoError(t, err)

	var hasGo bool
	for _, f := range families {
		if strings.HasPrefix(f.GetName(), "go_") {
			hasGo = true

			break
		}
	}

	assert.True(t, hasGo, "реестр должен собирать метрики среды выполнения Go")
}
