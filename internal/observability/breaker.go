package observability

import (
	"fmt"
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"go-avatar-service/internal/breaker"
	"go-avatar-service/internal/config"
)

var (
	_ breaker.Observer     = (*Breakers)(nil)
	_ prometheus.Collector = (*Breakers)(nil)
)

// Breakers — метрики выключателей внешних зависимостей. Методы безопасны на nil-приёмнике.
//
// Состояние снимается в момент сбора, а не по событию перехода: разомкнутый
// выключатель становится полуоткрытым только при обращении к нему, и без трафика
// метрика по событиям так и показывала бы «разомкнут».
type Breakers struct {
	state    *prometheus.Desc
	rejected *prometheus.CounterVec

	mu      sync.Mutex
	tracked []*breaker.Breaker
}

// NewBreakers создаёт метрики выключателей и регистрирует их в переданном реестре.
func NewBreakers(reg prometheus.Registerer) (*Breakers, error) {
	b := &Breakers{
		state: prometheus.NewDesc("circuit_breaker_state",
			"Состояние выключателя зависимости: 0 — замкнут, 1 — полуоткрыт, 2 — разомкнут.",
			[]string{"dependency"}, nil),
		rejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "circuit_breaker_rejected_total",
			Help: "Обращения к зависимости, отклонённые выключателем без вызова: open — разомкнут, probing — заняты пробные места.",
		}, []string{"dependency", "reason"}),
	}

	if err := register(reg, b, b.rejected); err != nil {
		return nil, fmt.Errorf("breaker metrics: %w", err)
	}

	return b, nil
}

// NewBreaker создаёт выключатель зависимости, чьи отказы и состояние попадают в эти метрики.
func (b *Breakers) NewBreaker(name string, cfg config.Breaker, opts ...breaker.Option) *breaker.Breaker {
	if b == nil {
		return breaker.New(name, cfg, nil, opts...)
	}

	br := breaker.New(name, cfg, b, opts...)

	b.mu.Lock()
	defer b.mu.Unlock()

	b.tracked = append(b.tracked, br)

	return br
}

// BreakerRejected учитывает обращение, отклонённое выключателем.
func (b *Breakers) BreakerRejected(dependency, reason string) {
	if b == nil {
		return
	}

	b.rejected.WithLabelValues(dependency, reason).Inc()
}

// Describe описывает метрику состояния для реестра.
func (b *Breakers) Describe(ch chan<- *prometheus.Desc) {
	ch <- b.state
}

// Collect отдаёт текущее состояние каждого выключателя.
func (b *Breakers) Collect(ch chan<- prometheus.Metric) {
	b.mu.Lock()
	tracked := append([]*breaker.Breaker(nil), b.tracked...)
	b.mu.Unlock()

	for _, br := range tracked {
		ch <- prometheus.MustNewConstMetric(b.state, prometheus.GaugeValue, float64(br.State()), br.Name())
	}
}
