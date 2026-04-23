package main

import (
	"context"
	"fmt"

	auditcore "github.com/ghbvf/gocell/cells/auditcore"
	"github.com/ghbvf/gocell/kernel/cell"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
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
// or provisional resources are needed.
//
// Reads GOCELL_AUDITCORE_HMAC_KEY, GOCELL_AUDITCORE_CURSOR_KEY, and
// GOCELL_AUDITCORE_CURSOR_PREVIOUS_KEY from the environment.
func (AuditCoreModule) Provide(_ context.Context, shared *SharedDeps) (cell.Cell, []bootstrap.Option, []kernellifecycle.ManagedResource, error) {
	// Cursor codec for auditcore: read env via LoadCursorKeys then build.
	auditPrimary, auditPrevious := LoadCursorKeys("AUDITCORE")
	cursorCodec, err := buildCursorCodec(cursorCodecConfig{
		AdapterMode:  shared.Topology.AdapterMode,
		EnvLabel:     "GOCELL_AUDITCORE_CURSOR_KEY",
		PrevEnvLabel: "GOCELL_AUDITCORE_CURSOR_PREVIOUS_KEY",
		Primary:      auditPrimary,
		Previous:     auditPrevious,
		DevDefault:   "corebundle-audit-cursor-key-32b!",
		Label:        "audit",
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("auditcore cursor codec: %w", err)
	}

	// HMAC key for audit hash chain.
	hmacPrimary := LoadCellHMACKey("AUDITCORE")
	hmacKeyStr, err := buildHMACKey(hmacKeyConfig{
		AdapterMode: shared.Topology.AdapterMode,
		EnvLabel:    "GOCELL_AUDITCORE_HMAC_KEY",
		Primary:     hmacPrimary,
		DevDefault:  "dev-hmac-key-replace-in-prod!!!!",
		Label:       "auditcore HMAC",
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("auditcore HMAC key: %w", err)
	}

	c := auditcore.NewAuditCore(
		auditcore.WithInMemoryDefaults(),
		auditcore.WithPublisher(shared.EventBus),
		auditcore.WithHMACKey(hmacKeyStr),
		auditcore.WithCursorCodec(cursorCodec),
	)
	return c, nil, nil, nil
}

var _ CellModule = AuditCoreModule{}
