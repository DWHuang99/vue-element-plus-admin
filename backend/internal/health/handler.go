package health

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// Pinger is an interface for checking database connectivity.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Probe is a named readiness check for one module (T071). The composition
// root registers one probe per module — iam_database, organization_database,
// admin_workflow_store, outbox_dispatcher — so /health/ready can distinguish
// which module is unavailable instead of collapsing everything into a single
// database check.
type Probe struct {
	Name  string
	Check func(ctx context.Context) error
}

// Handler holds health check dependencies.
type Handler struct {
	db     Pinger
	logger *slog.Logger
	probes []Probe

	mu         sync.RWMutex
	lastStates map[string]bool
}

// HealthResponse is the JSON response for health check endpoints.
type HealthResponse struct {
	Status    string            `json:"status"`
	Timestamp time.Time         `json:"timestamp"`
	Checks    map[string]string `json:"checks,omitempty"`
}

// NewHandler creates a new health check Handler for the base database only.
func NewHandler(db Pinger, logger *slog.Logger) *Handler {
	return NewHandlerWithProbes(db, logger, nil)
}

// NewHandlerWithProbes creates a health check Handler with the base database
// check plus one named probe per module.
func NewHandlerWithProbes(db Pinger, logger *slog.Logger, probes []Probe) *Handler {
	return &Handler{
		db:         db,
		logger:     logger,
		probes:     probes,
		lastStates: make(map[string]bool),
	}
}

// Live handles the GET /health/live endpoint.
// It always returns 200 as long as the process is running.
func (h *Handler) Live(c *gin.Context) {
	c.JSON(http.StatusOK, HealthResponse{
		Status:    "ok",
		Timestamp: time.Now().UTC(),
	})
}

// Ready handles the GET /health/ready endpoint.
// It returns 200 if all critical dependencies (database + every registered
// module probe) are available; 503 otherwise, with the failing module named
// in the per-check status map.
func (h *Handler) Ready(c *gin.Context) {
	checks := make(map[string]string)
	allAvailable := true

	for _, r := range h.runChecks() {
		checks[r.name] = boolToStatus(r.ok)
		if !r.ok {
			allAvailable = false
		}
		h.recordTransition(r.name, r.ok)
	}

	status := "ok"
	httpStatus := http.StatusOK
	if !allAvailable {
		status = "degraded"
		httpStatus = http.StatusServiceUnavailable
	}

	c.JSON(httpStatus, HealthResponse{
		Status:    status,
		Timestamp: time.Now().UTC(),
		Checks:    checks,
	})
}

// checkResult is one named check's outcome.
type checkResult struct {
	name string
	ok   bool
}

// runChecks evaluates the base database and every module probe.
func (h *Handler) runChecks() []checkResult {
	results := []checkResult{{name: "database", ok: h.checkDatabase()}}
	for _, p := range h.probes {
		results = append(results, checkResult{name: p.Name, ok: h.checkProbe(p)})
	}
	return results
}

// checkProbe runs one module probe with the same 5-second timeout as the
// database ping.
func (h *Handler) checkProbe(p Probe) bool {
	if p.Check == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return p.Check(ctx) == nil
}

// checkDatabase pings the database with a 5-second timeout.
func (h *Handler) checkDatabase() bool {
	if h.db == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return h.db.Ping(ctx) == nil
}

// recordTransition logs a module's state change once; steady state stays
// quiet. The first observation initializes the baseline without logging.
func (h *Handler) recordTransition(name string, ok bool) {
	h.mu.Lock()
	prev, seen := h.lastStates[name]
	h.lastStates[name] = ok
	h.mu.Unlock()

	if !seen || prev == ok {
		return
	}
	if ok {
		h.logger.Warn("module restored, service is now ready", "module", name)
	} else {
		h.logger.Warn("module unavailable, service is degraded", "module", name)
	}
}

// boolToStatus converts a boolean to a health status string.
func boolToStatus(ok bool) string {
	if ok {
		return "ok"
	}
	return "unavailable"
}
