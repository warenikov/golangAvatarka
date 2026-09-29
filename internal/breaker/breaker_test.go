package breaker_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go-avatar-service/internal/breaker"
	"go-avatar-service/internal/config"
	"go-avatar-service/internal/domain"
)

var errDown = errors.New("connection refused")

type recorder struct {
	mu       sync.Mutex
	states   []breaker.State
	rejected int
}

func (r *recorder) BreakerStateChanged(_ string, state breaker.State) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.states = append(r.states, state)
}

func (r *recorder) BreakerRejected(string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.rejected++
}

func testConfig(openTimeout time.Duration) config.Breaker {
	return config.Breaker{FailureThreshold: 3, OpenTimeout: openTimeout, HalfOpenRequests: 1}
}

func fail(b *breaker.Breaker, n int, err error) {
	for range n {
		_ = b.Do(func() error { return err })
	}
}

func TestNilBreakerPassesThrough(t *testing.T) {
	var b *breaker.Breaker

	err := b.Do(func() error { return errDown })
	require.ErrorIs(t, err, errDown)

	got, err := breaker.Call(b, func() (int, error) { return 42, nil })
	require.NoError(t, err)
	assert.Equal(t, 42, got)
	assert.Equal(t, breaker.StateClosed, b.State())
}

func TestBreakerOpensAfterConsecutiveFailures(t *testing.T) {
	rec := &recorder{}
	b := breaker.New("postgres", testConfig(time.Minute), rec)

	fail(b, 2, errDown)
	assert.Equal(t, breaker.StateClosed, b.State(), "порог ещё не достигнут")

	fail(b, 1, errDown)
	require.Equal(t, breaker.StateOpen, b.State())

	called := false
	err := b.Do(func() error {
		called = true

		return nil
	})

	require.ErrorIs(t, err, domain.ErrUnavailable)
	assert.Contains(t, err.Error(), "postgres")
	assert.False(t, called, "разомкнутый выключатель не должен звать зависимость")
	assert.Equal(t, []breaker.State{breaker.StateClosed, breaker.StateOpen}, rec.states)
	assert.Equal(t, 1, rec.rejected)
}

func TestBreakerSuccessResetsFailureStreak(t *testing.T) {
	b := breaker.New("s3", testConfig(time.Minute), nil)

	fail(b, 2, errDown)
	require.NoError(t, b.Do(func() error { return nil }))
	fail(b, 2, errDown)

	assert.Equal(t, breaker.StateClosed, b.State())
}

func TestBreakerIgnoresNonInfrastructureErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"аватарка не найдена", fmt.Errorf("get avatar: %w", domain.ErrAvatarNotFound)},
		{"объект не найден", fmt.Errorf("get object: %w", domain.ErrObjectNotFound)},
		{"чужая аватарка", domain.ErrForbidden},
		{"некорректный пользователь", domain.ErrInvalidUserID},
		{"клиент ушёл", fmt.Errorf("query: %w", context.Canceled)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := breaker.New("postgres", testConfig(time.Minute), nil)

			fail(b, 10, tt.err)

			assert.Equal(t, breaker.StateClosed, b.State())
			require.ErrorIs(t, b.Do(func() error { return tt.err }), tt.err, "ошибка возвращается как есть")
		})
	}
}

func TestBreakerCountsDeadlineAsFailure(t *testing.T) {
	b := breaker.New("rabbitmq", testConfig(time.Minute), nil)

	fail(b, 3, fmt.Errorf("wait confirm: %w", context.DeadlineExceeded))

	assert.Equal(t, breaker.StateOpen, b.State(), "зависшая зависимость — такой же отказ, как упавшая")
}

func TestBreakerRecoversThroughHalfOpen(t *testing.T) {
	rec := &recorder{}
	b := breaker.New("s3", testConfig(20*time.Millisecond), rec)

	fail(b, 3, errDown)
	require.Equal(t, breaker.StateOpen, b.State())

	require.Eventually(t, func() bool { return b.State() == breaker.StateHalfOpen },
		time.Second, 5*time.Millisecond)

	got, err := breaker.Call(b, func() (string, error) { return "ok", nil })
	require.NoError(t, err)
	assert.Equal(t, "ok", got)
	assert.Equal(t, breaker.StateClosed, b.State())
	assert.Equal(t,
		[]breaker.State{breaker.StateClosed, breaker.StateOpen, breaker.StateHalfOpen, breaker.StateClosed},
		rec.states)
}

func TestBreakerReopensOnHalfOpenFailure(t *testing.T) {
	b := breaker.New("s3", testConfig(20*time.Millisecond), nil)

	fail(b, 3, errDown)
	require.Eventually(t, func() bool { return b.State() == breaker.StateHalfOpen },
		time.Second, 5*time.Millisecond)

	fail(b, 1, errDown)

	assert.Equal(t, breaker.StateOpen, b.State())
}

func TestStateString(t *testing.T) {
	assert.Equal(t, "closed", breaker.StateClosed.String())
	assert.Equal(t, "half-open", breaker.StateHalfOpen.String())
	assert.Equal(t, "open", breaker.StateOpen.String())
	assert.Equal(t, "unknown(7)", breaker.State(7).String())
}
