package observability_test

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-avatar-service/internal/observability"
)

// Слои собираются в тестах без метрик, поэтому все методы обязаны переживать
// nil-приёмник: иначе половину тестов пришлось бы переписать ради счётчика.
func TestBusinessMetricsAreNilSafe(t *testing.T) {
	var b *observability.Business

	assert.NotPanics(t, func() {
		b.UploadFinished(observability.ResultOK, 1024)
		b.AvatarDeleted()
		b.EventPublished(observability.EventUpload, observability.ResultError)
		b.ProcessingFinished(observability.ResultOK, time.Now(), 2)
		b.EventDeadLettered()
		b.BacklogSize(5)
	})
}

func TestBusinessMetricsCount(t *testing.T) {
	reg := prometheus.NewRegistry()
	b, err := observability.NewBusiness(reg)
	require.NoError(t, err)

	b.UploadFinished(observability.ResultOK, 200*1024)
	b.UploadFinished(observability.ResultOK, 400*1024)
	b.UploadFinished(observability.ResultError, 0)
	b.AvatarDeleted()
	b.EventPublished(observability.EventUpload, observability.ResultOK)
	b.EventDeadLettered()
	b.BacklogSize(7)
	b.ProcessingFinished(observability.ResultOK, time.Now().Add(-time.Second), 2)
	b.ProcessingFinished(observability.ResultSkipped, time.Now(), 0)

	families, err := reg.Gather()
	require.NoError(t, err)

	names := make(map[string]bool, len(families))
	for _, f := range families {
		names[f.GetName()] = true
	}

	for _, want := range []string{
		"avatar_uploads_total",
		"avatar_upload_bytes",
		"avatar_processing_duration_seconds",
		"avatar_processing_total",
		"avatar_thumbnails_created_total",
		"avatar_deleted_total",
		"avatar_events_published_total",
		"avatar_events_dead_lettered_total",
		"avatar_processing_backlog",
	} {
		assert.True(t, names[want], "метрика %s должна быть зарегистрирована", want)
	}

	assert.InDelta(t, 7.0, gauge(t, reg, "avatar_processing_backlog"), 0.001)
	assert.InDelta(t, 2.0, counter(t, reg, "avatar_thumbnails_created_total"), 0.001)
	assert.InDelta(t, 1.0, counter(t, reg, "avatar_deleted_total"), 0.001)
	assert.InDelta(t, 1.0, counter(t, reg, "avatar_events_dead_lettered_total"), 0.001)
}

// Неудачная загрузка не должна попадать в гистограмму размеров: там нечего
// мерить, а нули испортили бы перцентили.
func TestFailedUploadIsNotSized(t *testing.T) {
	reg := prometheus.NewRegistry()
	b, err := observability.NewBusiness(reg)
	require.NoError(t, err)

	b.UploadFinished(observability.ResultError, 999)

	assert.Equal(t, uint64(0), histogramCount(t, reg, "avatar_upload_bytes"))

	b.UploadFinished(observability.ResultOK, 999)
	assert.Equal(t, uint64(1), histogramCount(t, reg, "avatar_upload_bytes"))
}

// Пропущенный дубль не должен попадать в длительность обработки: он ничего
// не обрабатывал, а околонулевые значения занизили бы перцентили.
func TestSkippedProcessingIsNotTimed(t *testing.T) {
	reg := prometheus.NewRegistry()
	b, err := observability.NewBusiness(reg)
	require.NoError(t, err)

	b.ProcessingFinished(observability.ResultSkipped, time.Now(), 0)

	assert.Equal(t, uint64(0), histogramCount(t, reg, "avatar_processing_duration_seconds"))
	assert.InDelta(t, 1.0, counterWithLabel(t, reg, "avatar_processing_total", observability.ResultSkipped), 0.001,
		"счётчик результата всё равно растёт")
}

func gauge(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()

	for _, f := range gather(t, reg) {
		if f.GetName() == name {
			return f.GetMetric()[0].GetGauge().GetValue()
		}
	}

	return 0
}

func counter(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()

	for _, f := range gather(t, reg) {
		if f.GetName() == name {
			return f.GetMetric()[0].GetCounter().GetValue()
		}
	}

	return 0
}

func histogramCount(t *testing.T, reg *prometheus.Registry, name string) uint64 {
	t.Helper()

	for _, f := range gather(t, reg) {
		if f.GetName() == name {
			return f.GetMetric()[0].GetHistogram().GetSampleCount()
		}
	}

	return 0
}

func counterWithLabel(t *testing.T, reg *prometheus.Registry, name, label string) float64 {
	t.Helper()

	for _, f := range gather(t, reg) {
		if f.GetName() != name {
			continue
		}

		for _, m := range f.GetMetric() {
			for _, pair := range m.GetLabel() {
				if pair.GetValue() == label {
					return m.GetCounter().GetValue()
				}
			}
		}
	}

	return 0
}

func gather(t *testing.T, reg *prometheus.Registry) []*dto.MetricFamily {
	t.Helper()

	families, err := reg.Gather()
	require.NoError(t, err)

	return families
}
