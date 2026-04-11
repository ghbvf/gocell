// Package health provides /healthz (liveness) and /readyz (readiness) HTTP
// endpoints that aggregate kernel/assembly health status and custom readiness
// checkers.
package health

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/ghbvf/gocell/kernel/assembly"
)

// Checker is a named readiness probe. Returning a non-nil error marks the
// check as unhealthy.
type Checker func() error

// Handler exposes /healthz and /readyz endpoints.
type Handler struct {
	assembly *assembly.CoreAssembly

	mu       sync.RWMutex
	checkers map[string]Checker
}

// New creates a Handler backed by the given CoreAssembly.
func New(asm *assembly.CoreAssembly) *Handler {
	return &Handler{
		assembly: asm,
		checkers: make(map[string]Checker),
	}
}

// RegisterChecker adds a named readiness checker. It panics if a checker with
// the same name is already registered (fail-fast at startup, matching Go
// convention for registration functions like http.HandleFunc).
func (h *Handler) RegisterChecker(name string, fn Checker) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.checkers[name]; exists {
		panic(fmt.Sprintf("health: duplicate checker name %q", name))
	}
	h.checkers[name] = fn
}

// LivezHandler returns an http.HandlerFunc for the /healthz liveness endpoint.
// It aggregates Health() from every registered Cell in the assembly.
func (h *Handler) LivezHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cellHealth := h.assembly.Health()

		checks := make(map[string]string, len(cellHealth))
		allHealthy := true
		for id, hs := range cellHealth {
			checks[id] = hs.Status
			if hs.Status != "healthy" {
				allHealthy = false
			}
		}

		status := "healthy"
		httpStatus := http.StatusOK
		if !allHealthy {
			status = "unhealthy"
			httpStatus = http.StatusServiceUnavailable
		}

		writeJSON(w, httpStatus, map[string]any{
			"status": status,
			"checks": checks,
		})
	}
}

// ReadyzHandler returns an http.HandlerFunc for the /readyz readiness endpoint.
// It runs all registered readiness checkers in addition to the Cell health.
// Cell health and dependency checkers are placed in separate JSON namespaces
// ("cells" and "dependencies") to prevent name collisions.
func (h *Handler) ReadyzHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cellHealth := h.assembly.Health()

		cells := make(map[string]string, len(cellHealth))
		allHealthy := true
		for id, hs := range cellHealth {
			cells[id] = hs.Status
			if hs.Status != "healthy" {
				allHealthy = false
			}
		}

		h.mu.RLock()
		checkersCopy := make(map[string]Checker, len(h.checkers))
		for k, v := range h.checkers {
			checkersCopy[k] = v
		}
		h.mu.RUnlock()

		dependencies := make(map[string]string, len(checkersCopy))
		for name, fn := range checkersCopy {
			if err := fn(); err != nil {
				dependencies[name] = "unhealthy"
				allHealthy = false
			} else {
				dependencies[name] = "healthy"
			}
		}

		status := "healthy"
		httpStatus := http.StatusOK
		if !allHealthy {
			status = "unhealthy"
			httpStatus = http.StatusServiceUnavailable
		}

		writeJSON(w, httpStatus, map[string]any{
			"status":       status,
			"cells":        cells,
			"dependencies": dependencies,
		})
	}
}

func writeJSON(w http.ResponseWriter, statusCode int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("health: failed to write response", slog.String("error", err.Error()))
	}
}
