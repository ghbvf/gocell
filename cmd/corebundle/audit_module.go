package main

import (
	"context"
	"fmt"

	auditcore "github.com/ghbvf/gocell/cells/auditcore"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// AuditCoreModule wires auditcore: HMAC key + EventBus + cursor codec.
// It reads auditcore-namespaced environment variables directly.
//
// ref: uber-go/fx fx.Module("auditcore", ...) — self-contained module.
// backlog: S29 CORE-BUNDLE-APP-BUILDER-01
type AuditCoreModule struct{}

// ID returns the stable identifier used in error messages.
func (AuditCoreModule) ID() string { return "auditcore" }

// Provide resolves all auditcore-specific dependencies and returns the
// constructed cell. Audit-core is in-memory only, so no bootstrap.Options
// are needed.
//
// Reads GOCELL_AUDITCORE_HMAC_KEY, GOCELL_AUDITCORE_CURSOR_KEY, and
// GOCELL_AUDITCORE_CURSOR_PREVIOUS_KEY from the environment.
func (AuditCoreModule) Provide(_ context.Context, shared *SharedDeps) (cell.Cell, []bootstrap.Option, error) {
	// Cursor codec for auditcore.
	cursorCodec, err := loadCursorCodec(shared.Topology.AdapterMode,
		"GOCELL_AUDITCORE_CURSOR_KEY", "GOCELL_AUDITCORE_CURSOR_PREVIOUS_KEY",
		"corebundle-audit-cursor-key-32b!", "audit")
	if err != nil {
		return nil, nil, fmt.Errorf("auditcore cursor codec: %w", err)
	}

	// HMAC key for audit hash chain.
	hmacKeyStr, err := loadSecret("GOCELL_AUDITCORE_HMAC_KEY", "dev-hmac-key-replace-in-prod!!!!", shared.Topology.AdapterMode)
	if err != nil {
		return nil, nil, fmt.Errorf("auditcore HMAC key: %w", err)
	}
	if err := rejectDemoKey(shared.Topology.AdapterMode, "GOCELL_AUDITCORE_HMAC_KEY", hmacKeyStr); err != nil {
		return nil, nil, err
	}

	c := auditcore.NewAuditCore(
		auditcore.WithInMemoryDefaults(),
		auditcore.WithPublisher(shared.EventBus),
		auditcore.WithHMACKey(hmacKeyStr),
		auditcore.WithCursorCodec(cursorCodec),
	)
	return c, nil, nil
}

var _ CellModule = AuditCoreModule{}
