package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestWaitDrainWaitsFullDelay(t *testing.T) {
	start := time.Now()

	assert.True(t, waitDrain(context.Background(), 30*time.Millisecond))
	assert.GreaterOrEqual(t, time.Since(start), 30*time.Millisecond)
}

func TestWaitDrainStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(10*time.Millisecond, cancel)

	start := time.Now()

	assert.False(t, waitDrain(ctx, time.Minute), "отмена должна прервать паузу")
	assert.Less(t, time.Since(start), time.Second)
}
