package rest_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-avatar-service/internal/handlers/rest"
	"go-avatar-service/internal/observability"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
}

func TestRequestLoggerWritesEntry(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))

	rec := httptest.NewRecorder()
	rest.RequestLogger(log)(okHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/avatars", nil))

	require.Equal(t, http.StatusOK, rec.Code)

	var entry map[string]any
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &entry))

	assert.Equal(t, http.MethodGet, entry["method"])
	assert.Equal(t, "/api/v1/avatars", entry["path"])
	assert.Equal(t, float64(http.StatusOK), entry["status"])
	assert.Equal(t, float64(2), entry["bytes"])
}

func TestRecovererTurnsPanicIntoInternalError(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))

	panicking := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("что-то пошло не так")
	})

	rec := httptest.NewRecorder()

	require.NotPanics(t, func() {
		rest.Recoverer(log)(panicking).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	})

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "Internal error")
	assert.NotContains(t, rec.Body.String(), "что-то пошло не так", "детали паники клиенту не показываем")
	assert.Contains(t, logs.String(), "паника в обработчике")
}

// http.ErrAbortHandler — договорённость stdlib о тихом обрыве соединения,
// её нельзя гасить: net/http сам обработает такую панику.
func TestRecovererRepanicsOnAbortHandler(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))

	aborting := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	})

	assert.PanicsWithValue(t, http.ErrAbortHandler, func() {
		rest.Recoverer(log)(aborting).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	})
}

func TestRecovererPassesSuccessThrough(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))

	rec := httptest.NewRecorder()
	rest.Recoverer(log)(okHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", rec.Body.String())
}

func TestMetricsCountsRequests(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := observability.NewHTTP(reg)

	r := chi.NewRouter()
	r.Use(rest.Metrics(m))
	r.Get("/api/v1/avatars/{avatar_id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/avatars/42", nil))
	require.Equal(t, http.StatusTeapot, rec.Code)

	// Метки берутся из шаблона маршрута, а не из пути: иначе кардинальность
	// счётчика растёт с числом аватарок.
	assert.Equal(t, float64(1), counterValue(t, reg, "http_requests_total",
		map[string]string{"method": http.MethodGet, "route": "/api/v1/avatars/{avatar_id}", "status": "418"}))
	assert.Equal(t, float64(0), gaugeValue(t, reg, "http_requests_in_flight"),
		"после ответа счётчик активных запросов должен вернуться к нулю")
}

func TestMetricsLabelsUnmatchedRoute(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := observability.NewHTTP(reg)

	r := chi.NewRouter()
	r.Use(rest.Metrics(m))
	// Хотя бы один маршрут обязателен: без зарегистрированных обработчиков
	// chi отвечает 404 в обход цепочки middleware.
	r.Get("/health", func(http.ResponseWriter, *http.Request) {})
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/нет-такого", nil))

	assert.Equal(t, float64(1), counterValue(t, reg, "http_requests_total",
		map[string]string{"method": http.MethodGet, "route": "unmatched", "status": "404"}))
}

func counterValue(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) float64 {
	t.Helper()

	metric := findMetric(t, reg, name, labels)
	if metric == nil {
		return 0
	}

	return metric.GetCounter().GetValue()
}

func gaugeValue(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()

	metric := findMetric(t, reg, name, nil)
	if metric == nil {
		return 0
	}

	return metric.GetGauge().GetValue()
}

func findMetric(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) *dto.Metric {
	t.Helper()

	families, err := reg.Gather()
	require.NoError(t, err)

	for _, family := range families {
		if family.GetName() != name {
			continue
		}

		for _, metric := range family.GetMetric() {
			if matchesLabels(metric, labels) {
				return metric
			}
		}
	}

	return nil
}

func matchesLabels(metric *dto.Metric, labels map[string]string) bool {
	for name, want := range labels {
		found := false
		for _, pair := range metric.GetLabel() {
			if pair.GetName() == name && pair.GetValue() == want {
				found = true

				break
			}
		}

		if !found {
			return false
		}
	}

	return true
}

type stubChecker struct {
	name string
	err  error
}

func (c stubChecker) Name() string                { return c.name }
func (c stubChecker) Check(context.Context) error { return c.err }

func TestHealthAllComponentsUp(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	h := rest.NewHealthHandler(log, "1.2.3", time.Second, false,
		stubChecker{name: "postgres"}, stubChecker{name: "s3"}, stubChecker{name: "rabbitmq"})

	rec := httptest.NewRecorder()
	h.Handle(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	require.Equal(t, http.StatusOK, rec.Code)

	body := decodeJSON[map[string]any](t, rec)
	assert.Equal(t, "ok", body["status"])
	assert.Equal(t, "1.2.3", body["version"])

	components, ok := body["components"].(map[string]any)
	require.True(t, ok)
	assert.Len(t, components, 3)

	for name, raw := range components {
		component, isMap := raw.(map[string]any)
		require.True(t, isMap, name)
		assert.Equal(t, "ok", component["status"], name)
	}
}

func TestHealthReportsDownComponent(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	h := rest.NewHealthHandler(log, "dev", time.Second, false,
		stubChecker{name: "postgres"},
		stubChecker{name: "rabbitmq", err: errors.New("dial tcp 10.0.0.1:5672: connection refused")})

	rec := httptest.NewRecorder()
	h.Handle(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)

	body := decodeJSON[map[string]any](t, rec)
	assert.Equal(t, "down", body["status"])

	components := body["components"].(map[string]any)
	rabbit := components["rabbitmq"].(map[string]any)
	assert.Equal(t, "down", rabbit["status"])
	assert.Equal(t, "component unavailable", rabbit["error"])
	assert.NotContains(t, rec.Body.String(), "10.0.0.1",
		"в prod адреса инфраструктуры не раскрываются")
}

// В dev причина отказа полезнее скрытности.
func TestHealthExposesErrorInDev(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	h := rest.NewHealthHandler(log, "dev", time.Second, true,
		stubChecker{name: "postgres", err: errors.New("connection refused")})

	rec := httptest.NewRecorder()
	h.Handle(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), "connection refused")
}

func TestHealthWithoutCheckers(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	h := rest.NewHealthHandler(log, "dev", time.Second, false)

	rec := httptest.NewRecorder()
	h.Handle(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"components":{}`)
}

func TestResponderErrorFormat(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))
	h := rest.NewHealthHandler(log, "dev", time.Second, false, stubChecker{name: "db", err: assert.AnError})

	rec := httptest.NewRecorder()
	h.Handle(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	assert.True(t, strings.HasSuffix(rec.Body.String(), "\n"))
}
