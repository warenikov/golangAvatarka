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
	m, err := observability.NewHTTP(reg)
	require.NoError(t, err)

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

// Двойная регистрация — ошибка сборки приложения, и сообщить о ней надо
// понятной ошибкой, а не паникой со стектрейсом из глубины Prometheus:
// конструктор зовут из run(), которая умеет её обработать.
func TestNewHTTPReportsDoubleRegistration(t *testing.T) {
	reg := prometheus.NewRegistry()

	_, err := observability.NewHTTP(reg)
	require.NoError(t, err)

	second, err := observability.NewHTTP(reg)
	require.Error(t, err)
	assert.Nil(t, second)
	assert.Contains(t, err.Error(), "http metrics")
}

func TestNewBusinessReportsDoubleRegistration(t *testing.T) {
	reg := prometheus.NewRegistry()

	_, err := observability.NewBusiness(reg)
	require.NoError(t, err)

	second, err := observability.NewBusiness(reg)
	require.Error(t, err)
	assert.Nil(t, second)
	assert.Contains(t, err.Error(), "business metrics")
}

func TestNewRegistryHasRuntimeCollectors(t *testing.T) {
	reg, err := observability.NewRegistry()
	require.NoError(t, err)

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
