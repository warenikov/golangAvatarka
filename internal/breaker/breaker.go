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

// Observer получает события выключателя, например для метрик.
type Observer interface {
	BreakerStateChanged(dependency string, state State)
	BreakerRejected(dependency string)
}

// Breaker — выключатель одной зависимости. Нулевой указатель пропускает все вызовы.
type Breaker struct {
	name     string
	cb       *gobreaker.CircuitBreaker[struct{}]
	observer Observer
}

// New создаёт выключатель зависимости name. observer может быть nil.
func New(name string, cfg config.Breaker, observer Observer) *Breaker {
	b := &Breaker{name: name, observer: observer}

	b.cb = gobreaker.NewCircuitBreaker[struct{}](gobreaker.Settings{
		Name:        name,
		MaxRequests: cfg.HalfOpenRequests,
		Timeout:     cfg.OpenTimeout,
		ReadyToTrip: func(c gobreaker.Counts) bool {
			return c.ConsecutiveFailures >= cfg.FailureThreshold
		},
		OnStateChange: func(_ string, _, to gobreaker.State) {
			b.notifyState(fromGobreaker(to))
		},
		IsExcluded:   isExcluded,
		IsSuccessful: isSuccessful,
	})

	b.notifyState(StateClosed)

	return b
}

// State возвращает текущее состояние выключателя.
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
	if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
		if b.observer != nil {
			b.observer.BreakerRejected(b.name)
		}

		return fmt.Errorf("%s: %w: %w", b.name, domain.ErrUnavailable, err)
	}

	return err
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

func (b *Breaker) notifyState(s State) {
	if b.observer != nil {
		b.observer.BreakerStateChanged(b.name, s)
	}
}

// isExcluded отбрасывает отказы, в которых зависимость не виновата: клиент ушёл, не дождавшись ответа.
func isExcluded(err error) bool {
	return errors.Is(err, context.Canceled)
}

// isSuccessful считает успехом ответы зависимости, означающие отказ по смыслу, а не сбой.
func isSuccessful(err error) bool {
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
