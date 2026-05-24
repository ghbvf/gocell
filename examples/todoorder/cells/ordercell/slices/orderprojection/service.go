// Package orderprojection implements the public-facing order-projection slice
// (L3): subscribes to order-created and order-status-changed events, maintains
// an in-memory "by-status" read model, and serves the summary query endpoint on
// the PrimaryListener (/api/v1). The internal control-plane rebuild endpoint is
// kept in a separate slice (orderprojectionrebuild) per governance rule
// SLICE-HTTP-VISIBILITY-SEGREGATION-01 (FMT-33).
//
// Projection logic is shared with the orderprojectionrebuild slice via the
// internal package cells/ordercell/internal/orderprojection, following the
// FMT-33 internal-shared-package pattern (prior art: configcore/internal/configreader).
package orderprojection

import internalproj "github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/orderprojection"

// Service is the slice service type; the projection logic is shared with the
// orderprojectionrebuild slice via cells/ordercell/internal/orderprojection.
type Service = internalproj.Service

// StatusBucket re-exports the type from the shared internal package.
type StatusBucket = internalproj.StatusBucket

// Summary re-exports the type from the shared internal package.
type Summary = internalproj.Summary

// RebuildReport re-exports the type from the shared internal package.
type RebuildReport = internalproj.RebuildReport

// Option re-exports the type from the shared internal package.
type Option = internalproj.Option

// NewService creates a new orderprojection Service.
var NewService = internalproj.NewService

// WithLogger sets the logger.
var WithLogger = internalproj.WithLogger
