package main

import (
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/correlation"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/observability/correlate"
)

// buildCorrelateOption constructs the bootstrap.WithCorrelateRoutes option for
// the correlate reverse-lookup endpoint (#1048 Batch 3).
//
// store is the audit read-side QueryStore exported by the auditcore module via
// ModuleExports.AuditQueryStore; it is the same multiStore instance used by
// auditcore, not a second allocation.
//
// generatedCellOwners() is compile-time static: it is derived from each cell's
// cell.yaml owner block by gocell generate assembly and lives in modules_gen.go.
// The Topology therefore does not depend on runtime state and is available
// immediately (no construction-order hazard).
//
// Returns a nil error when the endpoint is successfully constructed. When store
// is nil (bare or typed-nil interface) the caller must fail-fast: auditcore is a
// fixed corebundle cell, so a missing AuditQueryStore export is a composition
// wiring bug, not a tolerable degradation (no soft fallback / silent endpoint
// disable).
func buildCorrelateOption(store ledger.QueryStore) (bootstrap.Option, error) {
	if validation.IsNilInterface(store) {
		return nil, fmt.Errorf("correlate: AuditQueryStore not available from module exports (auditcore absent?)")
	}
	topo := correlation.Topology(generatedCellOwners())
	svc, err := correlate.NewService(store, topo, nil)
	if err != nil {
		return nil, fmt.Errorf("correlate: %w", err)
	}
	return bootstrap.WithCorrelateRoutes(svc), nil
}
