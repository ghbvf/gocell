// Package sessionprojection implements the public-facing session-projection slice
// (L3 CQRS projection): reg.RegisterProjection drives HandleSessionCreated
// (apply) and ResetSessionRegistry (onReset) via the framework projection.Coordinator.
// This slice serves the session registry-summary query endpoint on the
// PrimaryListener (/api/v1/access/sessions/registry-summary).
//
// Layer split: corecells/accesscore/internal/sessionprojection holds the pure
// projection logic (apply, reset, query) with no HTTP dependency. This file
// re-exports the public surface as forwarding functions rather than assignable
// vars — callers cannot reassign NewService or WithLogger, which preserves the
// sealed-construction invariant (consistent with the todoorder orderprojection
// re-export pattern and cell-patterns.md DTO scope B).
package sessionprojection

import (
	"log/slog"

	internalprojection "github.com/ghbvf/gocell/corecells/accesscore/internal/sessionprojection"
)

// Service is the slice service type; the projection logic lives in
// corecells/accesscore/internal/sessionprojection.
type Service = internalprojection.Service

// Option re-exports the type from the shared internal package.
type Option = internalprojection.Option

// NewService creates a new sessionprojection Service.
func NewService(opts ...Option) (*Service, error) {
	return internalprojection.NewService(opts...)
}

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) Option {
	return internalprojection.WithLogger(l)
}
