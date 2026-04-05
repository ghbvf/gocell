// Package health provides /healthz (liveness) and /readyz (readiness) HTTP
// endpoints that aggregate kernel/assembly health status and custom readiness
// checkers.
package health

import (
	"encoding/json"
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

// RegisterChecker adds a named readiness checker. It is safe for concurrent
// use, but should normally be called during setup before serving.
func (h *Handler) RegisterChecker(name string, fn Checker) {
	h.mu.Lock()
	defer h.mu.Unlock()
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
func (h *Handler) ReadyzHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cellHealth := h.assembly.Health()

		checks := make(map[string]string)
		allHealthy := true
		for id, hs := range cellHealth {
			checks[id] = hs.Status
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

		for name, fn := range checkersCopy {
			if err := fn(); err != nil {
				checks[name] = "unhealthy"
				allHealthy = false
			} else {
				checks[name] = "healthy"
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

func writeJSON(w http.ResponseWriter, statusCode int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(v)
}
