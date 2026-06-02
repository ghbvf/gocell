// Package orderprojection implements the public-facing order-projection slice
// (L3 CQRS harness reference): reg.RegisterProjection drives HandleOrderCreated
// (apply) and ResetOrderStatus (onReset) via the framework projection.Coordinator.
// The subscription wiring is emitted into cell_gen.go from the slice.yaml
// contractUsages[role=subscribe, projection=order_status, onReset=ResetOrderStatus].
// Rebuild is now framework-owned (Coordinator.Rebuild); the separate
// orderprojectionrebuild slice has been removed.
//
// The projection logic lives in cells/ordercell/internal/orderprojection.
// This file re-exports only the symbols that the cell composition root and
// tests need from the shared internal package.
package orderprojection

import internalproj "github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/orderprojection"

// Service is the slice service type; the projection logic lives in
// cells/ordercell/internal/orderprojection.
type Service = internalproj.Service

// StatusBucket re-exports the type from the shared internal package.
type StatusBucket = internalproj.StatusBucket

// Summary re-exports the type from the shared internal package.
type Summary = internalproj.Summary

// Option re-exports the type from the shared internal package.
type Option = internalproj.Option

// NewService creates a new orderprojection Service.
var NewService = internalproj.NewService

// WithLogger sets the logger.
var WithLogger = internalproj.WithLogger
