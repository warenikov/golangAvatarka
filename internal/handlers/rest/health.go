package rest

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const (
	statusOK       = "ok"
	statusDown     = "down"
	statusDraining = "draining"
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

	detail string
}

type healthResponse struct {
	Status     string                     `json:"status"`
	Components map[string]componentHealth `json:"components"`
	Version    string                     `json:"version"`
	UptimeS    int64                      `json:"uptime_s"`
}

// statusResponse — ответ без опроса компонентов: живость и режим слива.
type statusResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	UptimeS int64  `json:"uptime_s"`
}

type HealthHandler struct {
	responder
	checkers    []Checker
	version     string
	timeout     time.Duration
	started     time.Time
	exposeError bool

	// draining взводится при получении сигнала остановки. С этого момента
	// готовность отвечает отказом, оркестратор убирает под из балансировки,
	// и только потом процесс перестаёт принимать соединения.
	draining atomic.Bool
}

// NewHealthHandler создаёт обработчик проверки работоспособности сервиса.
// Причина отказа компонента попадает в ответ только при exposeError: текст ошибки
// подключения раскрывает адреса и учётные записи инфраструктуры.
func NewHealthHandler(log *slog.Logger, version string, timeout time.Duration, exposeError bool, checkers ...Checker) *HealthHandler {
	return &HealthHandler{
		responder:   responder{log: log},
		checkers:    checkers,
		version:     version,
		timeout:     timeout,
		started:     time.Now(),
		exposeError: exposeError,
	}
}

// Drain переводит обработчик в режим остановки: готовность начинает отвечать отказом.
func (h *HealthHandler) Drain() {
	h.draining.Store(true)
}

// Live отвечает, жив ли процесс, и ничего больше не проверяет.
//
// Состояние зависимостей сюда намеренно не входит. Проверку живости
// оркестратор лечит перезапуском, а перезапуск не чинит ни упавшую базу,
// ни недоступный брокер: он лишь превращает частичный отказ в полный,
// разом убивая все реплики, которые на самом деле исправны.
func (h *HealthHandler) Live(w http.ResponseWriter, r *http.Request) {
	h.JSON(r.Context(), w, http.StatusOK, statusResponse{
		Status:  statusOK,
		Version: h.version,
		UptimeS: int64(time.Since(h.started).Seconds()),
	})
}

// Ready опрашивает компоненты параллельно и отвечает 503, если хотя бы один недоступен.
func (h *HealthHandler) Ready(w http.ResponseWriter, r *http.Request) {
	if h.draining.Load() {
		h.JSON(r.Context(), w, http.StatusServiceUnavailable, statusResponse{
			Status:  statusDraining,
			Version: h.version,
			UptimeS: int64(time.Since(h.started).Seconds()),
		})

		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()

	results := make([]componentHealth, len(h.checkers))

	var wg sync.WaitGroup
	for i, c := range h.checkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = probe(ctx, c, h.exposeError)
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

			h.log.ErrorContext(ctx, "компонент недоступен", "component", c.Name(), "err", results[i].detail)
		}
	}

	h.JSON(ctx, w, code, resp)
}

func probe(ctx context.Context, c Checker, exposeError bool) componentHealth {
	started := time.Now()
	err := c.Check(ctx)
	health := componentHealth{
		Status:    statusOK,
		LatencyMS: time.Since(started).Milliseconds(),
	}

	if err != nil {
		health.Status = statusDown
		health.detail = err.Error()
		health.Error = "component unavailable"
		if exposeError {
			health.Error = health.detail
		}
	}

	return health
}
