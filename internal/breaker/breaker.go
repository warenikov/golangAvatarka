// Package breaker защищает обращения к внешним зависимостям автоматическим выключателем.
package breaker

import (
	"context"
	"errors"
	"fmt"

	"github.com/sony/gobreaker/v2"

	"go-avatar-service/internal/config"
	"go-avatar-service/internal/domain"
)

type State int

const (
	StateClosed State = iota
	StateHalfOpen
	StateOpen
)

// String возвращает имя состояния для логов.
func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateHalfOpen:
		return "half-open"
	case StateOpen:
		return "open"
	default:
		return fmt.Sprintf("unknown(%d)", int(s))
	}
}

// Причины, по которым выключатель отклоняет вызов.
const (
	// RejectOpen — выключатель разомкнут, зависимость считается недоступной.
	RejectOpen = "open"
	// RejectProbing — выключатель полуоткрыт, и все пробные места уже заняты.
	RejectProbing = "probing"
)

// Observer получает отказы выключателя, например для метрик.
type Observer interface {
	BreakerRejected(dependency, reason string)
}

// Option настраивает выключатель.
type Option func(*Breaker)

// WithHealthyErrors задаёт ошибки, которые зависимость возвращает в исправном
// состоянии: отказ по данным запроса, а не сбой. Такие ошибки счётчик не трогают.
func WithHealthyErrors(fn func(error) bool) Option {
	return func(b *Breaker) { b.healthyErr = fn }
}

// Breaker — выключатель одной зависимости. Нулевой указатель пропускает все вызовы.
type Breaker struct {
	name       string
	cb         *gobreaker.CircuitBreaker[struct{}]
	observer   Observer
	healthyErr func(error) bool
}

// New создаёт выключатель зависимости name. observer может быть nil.
func New(name string, cfg config.Breaker, observer Observer, opts ...Option) *Breaker {
	b := &Breaker{name: name, observer: observer}
	for _, opt := range opts {
		opt(b)
	}

	b.cb = gobreaker.NewCircuitBreaker[struct{}](gobreaker.Settings{
		Name:        name,
		MaxRequests: cfg.HalfOpenRequests,
		Timeout:     cfg.OpenTimeout,
		ReadyToTrip: func(c gobreaker.Counts) bool {
			return c.ConsecutiveFailures >= cfg.FailureThreshold
		},
		IsExcluded:   isExcluded,
		IsSuccessful: b.isSuccessful,
	})

	return b
}

// Name возвращает имя зависимости.
func (b *Breaker) Name() string { return b.name }

// State возвращает текущее состояние выключателя. Вызов сам переводит
// разомкнутый выключатель в полуоткрытый, когда истекла пауза, — даже без трафика.
func (b *Breaker) State() State {
	if b == nil {
		return StateClosed
	}

	return fromGobreaker(b.cb.State())
}

// Do выполняет fn через выключатель. Пока выключатель разомкнут, fn не вызывается,
// а возвращается ошибка, обёрнутая в domain.ErrUnavailable.
func (b *Breaker) Do(fn func() error) error {
	if b == nil {
		return fn()
	}

	_, err := b.cb.Execute(func() (struct{}, error) {
		return struct{}{}, fn()
	})

	var reason string

	switch {
	case errors.Is(err, gobreaker.ErrOpenState):
		reason = RejectOpen
	case errors.Is(err, gobreaker.ErrTooManyRequests):
		reason = RejectProbing
	default:
		return err
	}

	if b.observer != nil {
		b.observer.BreakerRejected(b.name, reason)
	}

	return fmt.Errorf("%s (%s): %w", b.name, reason, domain.ErrUnavailable)
}

// Call выполняет fn через выключатель и возвращает её результат.
func Call[T any](b *Breaker, fn func() (T, error)) (T, error) {
	var result T

	err := b.Do(func() error {
		var err error
		result, err = fn()

		return err
	})

	return result, err
}

// isExcluded отбрасывает отказы, в которых зависимость не виновата: клиент ушёл, не дождавшись ответа.
func isExcluded(err error) bool {
	return errors.Is(err, context.Canceled)
}

// isSuccessful считает успехом ответы зависимости, означающие отказ по смыслу, а не сбой.
func (b *Breaker) isSuccessful(err error) bool {
	if b.healthyErr != nil && b.healthyErr(err) {
		return true
	}

	return err == nil ||
		errors.Is(err, domain.ErrAvatarNotFound) ||
		errors.Is(err, domain.ErrObjectNotFound) ||
		errors.Is(err, domain.ErrForbidden) ||
		errors.Is(err, domain.ErrInvalidUserID)
}

func fromGobreaker(s gobreaker.State) State {
	switch s {
	case gobreaker.StateHalfOpen:
		return StateHalfOpen
	case gobreaker.StateOpen:
		return StateOpen
	default:
		return StateClosed
	}
}
