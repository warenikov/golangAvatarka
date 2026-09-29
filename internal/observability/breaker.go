package observability

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"

	"go-avatar-service/internal/breaker"
)

var _ breaker.Observer = (*Breakers)(nil)

// Breakers — метрики выключателей внешних зависимостей. Методы безопасны на nil-приёмнике.
type Breakers struct {
	state    *prometheus.GaugeVec
	rejected *prometheus.CounterVec
}

// NewBreakers создаёт метрики выключателей и регистрирует их в переданном реестре.
func NewBreakers(reg prometheus.Registerer) (*Breakers, error) {
	b := &Breakers{
		state: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "circuit_breaker_state",
			Help: "Состояние выключателя зависимости: 0 — замкнут, 1 — полуоткрыт, 2 — разомкнут.",
		}, []string{"dependency"}),
		rejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "circuit_breaker_rejected_total",
			Help: "Обращения к зависимости, отклонённые выключателем без вызова.",
		}, []string{"dependency"}),
	}

	if err := register(reg, b.state, b.rejected); err != nil {
		return nil, fmt.Errorf("breaker metrics: %w", err)
	}

	return b, nil
}

// BreakerStateChanged записывает новое состояние выключателя.
func (b *Breakers) BreakerStateChanged(dependency string, state breaker.State) {
	if b == nil {
		return
	}

	b.state.WithLabelValues(dependency).Set(float64(state))
}

// BreakerRejected учитывает обращение, отклонённое выключателем.
func (b *Breakers) BreakerRejected(dependency string) {
	if b == nil {
		return
	}

	b.rejected.WithLabelValues(dependency).Inc()
}
