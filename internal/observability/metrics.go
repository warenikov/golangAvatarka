// Package metrics объявляет метрики Prometheus и регистрирует их.
package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

type HTTP struct {
	Requests *prometheus.CounterVec
	Duration *prometheus.HistogramVec
	InFlight prometheus.Gauge
}

// NewHTTP создаёт метрики HTTP-слоя и регистрирует их в переданном реестре.
func NewHTTP(reg prometheus.Registerer) *HTTP {
	m := &HTTP{
		Requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Количество обработанных HTTP-запросов.",
		}, []string{"method", "route", "status"}),
		Duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "Длительность обработки HTTP-запроса.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),
		InFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "http_requests_in_flight",
			Help: "Количество запросов в обработке прямо сейчас.",
		}),
	}

	reg.MustRegister(m.Requests, m.Duration, m.InFlight)

	return m
}

// NewRegistry создаёт реестр с метриками среды выполнения Go и процесса.
func NewRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	return reg
}
