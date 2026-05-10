package main

import (
	"context"
	"fmt"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	auditcore "github.com/ghbvf/gocell/cells/auditcore"
	"github.com/ghbvf/gocell/kernel/cell"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// AuditCoreModule wires auditcore: ledger.Protocol + ledger.Store + EventBus + cursor codec.
// It reads auditcore-namespaced environment variables directly.
//
// ref: uber-go/fx fx.Module("auditcore", ...) — self-contained module.
// backlog: S29 CORE-BUNDLE-APP-BUILDER-01
type AuditCoreModule struct{}

// ID returns the stable identifier used in error messages.
func (AuditCoreModule) ID() string { return "auditcore" }

// Provide resolves all auditcore-specific dependencies and returns the
// constructed cell. The HMAC key is injected into the ledger.Protocol;
// cells never hold the raw key (B2-C-01 restart-continuity via strict tail verify).
//
// Reads GOCELL_AUDITCORE_HMAC_KEY, GOCELL_AUDITCORE_CURSOR_KEY, and
// GOCELL_AUDITCORE_CURSOR_PREVIOUS_KEY from the environment.
func (AuditCoreModule) Provide(
	_ context.Context, shared *SharedDeps,
) (cell.Cell, []bootstrap.Option, []kernellifecycle.ManagedResource, error) {
	// Cursor codec for auditcore.
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

	// HMAC key for audit hash chain — injected into Protocol, not cell.
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

	// Build ledger.Protocol (composition root responsibility per
	// AUDIT-LEDGER-PROTOCOL-COMPOSITION-ROOT-01 archtest).
	auditNamespace, err := ledger.ParseNamespaceID("auditcore")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("auditcore namespace: %w", err)
	}
	protocol := ledger.MustNewProtocol(
		ledger.WithChainHMAC(hmacKey),
		ledger.WithNamespace(auditNamespace),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)

	// Build ledger.Store: postgres or mem.
	var ledgerStore ledger.Store
	auditOpts := []auditcore.Option{
		auditcore.WithClock(shared.Clock),
		auditcore.WithLedgerProtocol(protocol),
		// Publisher set unconditionally; outboxWriter set conditionally below.
		// cell.ResolveEmitter picks DirectEmitter(FailOpen) when writer is nil
		// (memory mode) and WriterEmitter when both pub+writer are non-nil (durable).
		auditcore.WithOutboxDeps(outbox.WrapPublisherForCell(shared.EventBus), nil),
		auditcore.WithCursorCodec(cursorCodec),
		auditcore.WithMetricsProvider(shared.PromStack.metricProvider),
	}

	if shared.Topology.StorageBackend == "postgres" {
		if shared.SharedPGPool == nil {
			return nil, nil, nil, fmt.Errorf("AuditCoreModule: postgres mode requires SharedPGPool " +
				"(ConfigCoreModule must run before AuditCoreModule)")
		}
		txMgr := adapterpg.NewTxManager(shared.SharedPGPool)
		pgStore, storeErr := adapterpg.NewLedgerStore(shared.SharedPGPool.DB(), txMgr, protocol, shared.Clock)
		if storeErr != nil {
			return nil, nil, nil, fmt.Errorf("auditcore LedgerStore: %w", storeErr)
		}
		ledgerStore = pgStore
		writer := adapterpg.NewOutboxWriter(shared.Clock)
		auditOpts = append(auditOpts,
			auditcore.WithOutboxDeps(nil, outbox.WrapWriterForCell(writer)),
			auditcore.WithTxManager(persistence.WrapForCell(txMgr)),
		)
	} else {
		memStore, storeErr := ledger.NewMemStore(protocol, shared.Clock)
		if storeErr != nil {
			return nil, nil, nil, fmt.Errorf("auditcore MemStore: %w", storeErr)
		}
		ledgerStore = memStore
	}

	auditOpts = append(auditOpts, auditcore.WithLedgerStore(ledgerStore))

	c := auditcore.NewAuditCore(auditOpts...) //archtest:allow:clock-injection:via-slice WithClock prepended to auditOpts above
	return c, nil, nil, nil
}

var _ CellModule = AuditCoreModule{}
