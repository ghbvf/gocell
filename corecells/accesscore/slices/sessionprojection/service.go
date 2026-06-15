// Package sessionprojection implements the public-facing session-projection slice
// (L3 CQRS projection): reg.RegisterProjection drives HandleSessionCreated
// (apply) and ResetSessionRegistry (onReset) via the framework projection.Coordinator.
// This slice serves the session registry-summary query endpoint on the
// PrimaryListener (/api/v1/access/sessions/registry-summary).
//
// The projection logic lives in corecells/accesscore/internal/sessionprojection.
// This file re-exports only the symbols that the cell composition root and
// tests need from the shared internal package.
package sessionprojection

import internalprojection "github.com/ghbvf/gocell/corecells/accesscore/internal/sessionprojection"

// Service is the slice service type; the projection logic lives in
// corecells/accesscore/internal/sessionprojection.
type Service = internalprojection.Service

// Option re-exports the type from the shared internal package.
type Option = internalprojection.Option

// NewService creates a new sessionprojection Service.
var NewService = internalprojection.NewService

// WithLogger sets the logger.
var WithLogger = internalprojection.WithLogger
