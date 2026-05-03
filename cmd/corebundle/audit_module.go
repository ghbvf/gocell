package main

import (
	"context"
	"fmt"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
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
func (AuditCoreModule) Provide(
	_ context.Context, shared *SharedDeps,
) (cell.Cell, []bootstrap.Option, []kernellifecycle.ManagedResource, error) {
	// Cursor codec for auditcore: read env via LoadCursorKeys then build.
	auditPrimary, auditPrevious := LoadCursorKeys("AUDITCORE")
	cursorCodec, err := buildCursorCodec(cursorCodecConfig{
		AdapterMode: shared.Topology.AdapterMode,
		EnvName:     "GOCELL_AUDITCORE_CURSOR_KEY",
		PrevEnvName: "GOCELL_AUDITCORE_CURSOR_PREVIOUS_KEY",
		Primary:     auditPrimary,
		Previous:    auditPrevious,
		DevDefault:  "corebundle-audit-cursor-key-32b!",
		Label:       "audit",
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("auditcore cursor codec: %w", err)
	}

	// HMAC key for audit hash chain.
	hmacPrimary := LoadCellHMACKey("AUDITCORE")
	hmacKey, err := buildHMACKey(hmacKeyConfig{
		AdapterMode: shared.Topology.AdapterMode,
		EnvName:     "GOCELL_AUDITCORE_HMAC_KEY",
		Primary:     hmacPrimary,
		DevDefault:  "dev-hmac-key-replace-in-prod!!!!",
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("auditcore HMAC key: %w", err)
	}

	auditOpts := []auditcore.Option{
		auditcore.WithClock(shared.Clock),
		auditcore.WithInMemoryDefaults(),
		// Publisher set unconditionally; outboxWriter set conditionally below.
		// cell.ResolveEmitter picks DirectEmitter(FailOpen) when writer is nil
		// (memory mode) and WriterEmitter when both pub+writer are non-nil (durable).
		auditcore.WithOutboxDeps(shared.EventBus, nil),
		auditcore.WithHMACKey(hmacKey),
		auditcore.WithCursorCodec(cursorCodec),
		auditcore.WithMetricsProvider(shared.PromStack.metricProvider),
	}
	if shared.Topology.StorageBackend == "postgres" {
		if shared.SharedPGPool == nil {
			return nil, nil, nil, fmt.Errorf("AuditCoreModule: postgres mode requires SharedPGPool " +
				"(ConfigCoreModule must run before AuditCoreModule)")
		}
		writer := adapterpg.NewOutboxWriter(shared.Clock)
		txMgr := adapterpg.NewTxManager(shared.SharedPGPool)
		// Accumulative WithOutboxDeps: adds writer without replacing the publisher
		// set above. WithTxManager wires the TxRunner for L2 transactional atomicity.
		auditOpts = append(auditOpts,
			auditcore.WithOutboxDeps(nil, writer),
			auditcore.WithTxManager(txMgr),
		)
	}
	c := auditcore.NewAuditCore(auditOpts...) //archtest:allow:clock-injection:via-slice WithClock prepended to auditOpts above
	return c, nil, nil, nil
}

var _ CellModule = AuditCoreModule{}
