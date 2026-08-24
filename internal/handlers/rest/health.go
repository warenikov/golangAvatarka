package rest

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

const (
	healthTimeout = 2 * time.Second

	statusOK   = "ok"
	statusDown = "down"
)

// Checker описывает компонент, состояние которого отражается в ответе /health.
type Checker interface {
	Name() string
	Check(ctx context.Context) error
}

type componentHealth struct {
	Status    string `json:"status"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

type healthResponse struct {
	Status     string                     `json:"status"`
	Components map[string]componentHealth `json:"components"`
	Version    string                     `json:"version"`
	UptimeS    int64                      `json:"uptime_s"`
}

type HealthHandler struct {
	responder
	checkers []Checker
	version  string
	timeout  time.Duration
	started  time.Time
}

// NewHealthHandler создаёт обработчик проверки работоспособности сервиса.
func NewHealthHandler(log *slog.Logger, version string, timeout time.Duration, checkers ...Checker) *HealthHandler {
	return &HealthHandler{
		responder: responder{log: log},
		checkers:  checkers,
		version:   version,
		timeout:   timeout,
		started:   time.Now(),
	}
}

// Handle опрашивает компоненты параллельно и отвечает 503, если хотя бы один недоступен.
func (h *HealthHandler) Handle(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()

	results := make([]componentHealth, len(h.checkers))

	var wg sync.WaitGroup
	for i, c := range h.checkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = probe(ctx, c)
		}()
	}
	wg.Wait()

	resp := healthResponse{
		Status:     statusOK,
		Components: make(map[string]componentHealth, len(h.checkers)),
		Version:    h.version,
		UptimeS:    int64(time.Since(h.started).Seconds()),
	}

	code := http.StatusOK
	for i, c := range h.checkers {
		resp.Components[c.Name()] = results[i]
		if results[i].Status != statusOK {
			resp.Status = statusDown
			code = http.StatusServiceUnavailable
		}
	}

	h.JSON(ctx, w, code, resp)
}

func probe(ctx context.Context, c Checker) componentHealth {
	started := time.Now()
	err := c.Check(ctx)
	health := componentHealth{
		Status:    statusOK,
		LatencyMS: time.Since(started).Milliseconds(),
	}

	if err != nil {
		health.Status = statusDown
		health.Error = err.Error()
	}

	return health
}
