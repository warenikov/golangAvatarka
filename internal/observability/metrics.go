// Package metrics объявляет метрики Prometheus и регистрирует их.
package observability

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

type HTTP struct {
	Requests *prometheus.CounterVec
	Duration *prometheus.HistogramVec
	InFlight prometheus.Gauge
}

// NewHTTP создаёт метрики HTTP-слоя и регистрирует их в переданном реестре.
//
// Ошибка возвращается, а не вызывает панику: конструктор зовут из run(),
// которая умеет её обработать. Must-варианты уместны там, где обработать
// ошибку негде — переменные уровня пакета и init().
func NewHTTP(reg prometheus.Registerer) (*HTTP, error) {
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

	if err := register(reg, m.Requests, m.Duration, m.InFlight); err != nil {
		return nil, fmt.Errorf("http metrics: %w", err)
	}

	return m, nil
}

// NewRegistry создаёт реестр с метриками среды выполнения Go и процесса.
func NewRegistry() (*prometheus.Registry, error) {
	reg := prometheus.NewRegistry()

	err := register(reg,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	if err != nil {
		return nil, fmt.Errorf("runtime metrics: %w", err)
	}

	return reg, nil
}

// register регистрирует набор коллекторов, останавливаясь на первой ошибке.
//
// Дубликат метрики — это ошибка сборки приложения: два реестра с одним
// набором или повторная инициализация. Понятное сообщение здесь полезнее
// паники со стектрейсом из глубины Prometheus.
func register(reg prometheus.Registerer, collectors ...prometheus.Collector) error {
	for _, c := range collectors {
		if err := reg.Register(c); err != nil {
			return fmt.Errorf("register collector: %w", err)
		}
	}

	return nil
}
