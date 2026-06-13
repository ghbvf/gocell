// Package auditcore is the platform composition module for the auditcore Cell.
// It implements [composition.CellModule] and wires all auditcore-specific
// dependencies from [composition.SharedDeps].
//
// This is a composition-root-layer package: it may import cells/, adapters/,
// and cellmodules/cellsecrets/. It must NOT be imported by cells/, runtime/, or
// adapters/.
//
// ref: uber-go/fx fx.Module("auditcore", ...) — self-contained module.
// ref: google/trillian storage/log_storage.go — per-tree partition pattern.
// ref: k8s.io/apiserver/pkg/audit/union.go — read-side fan-out aggregator.
// ref: hashicorp/vault audit/broker.go — per-device Salt isolates HMAC state.
package auditcore

import (
	"context"
	"fmt"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/cellmodules/cellsecrets"
	auditcell "github.com/ghbvf/gocell/corecells/auditcore"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/audit"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
	"github.com/ghbvf/gocell/runtime/composition"
)

type module struct{}

// Module returns a composition.CellModule that wires the auditcore Cell.
func Module() composition.CellModule { return module{} }

// ID returns the stable identifier used in error messages and logs.
func (module) ID() string { return "auditcore" }

// Provide resolves all auditcore-specific dependencies from the composition
// shared context and returns the constructed Cell.
//
// Reads from the environment:
//   - GOCELL_AUDITCORE_HMAC_KEY (relay chain HMAC, namespace="auditcore")
//   - GOCELL_AUDIT_BOOTSTRAP_HMAC_KEY (bootstrap chain HMAC, namespace="bootstrap")
//   - GOCELL_AUDITCORE_CURSOR_KEY / *_PREVIOUS_KEY (cursor codec)
func (m module) Provide(
	ctx context.Context, shared *composition.SharedDeps,
) (composition.ModuleResult, error) {
	cursorCodec, auditProtocol, bootstrapProtocol, err := buildAuditProtocols(shared)
	if err != nil {
		return composition.ModuleResult{}, err
	}

	// Build the two ledger.Store instances (PG or mem).
	auditcoreStore, bootstrapInnerStore, err := buildAuditStores(shared, auditProtocol, bootstrapProtocol)
	if err != nil {
		return composition.ModuleResult{}, err
	}

	// Run strict tail verify against the bootstrap chain at composition time.
	bootstrapWrapped, err := audit.NewBootstrapLedgerStore(bootstrapInnerStore)
	if err != nil {
		return composition.ModuleResult{}, fmt.Errorf("wrap bootstrap audit store: %w", err)
	}
	if err := audit.VerifyBootstrapTailOnStartup(ctx, bootstrapWrapped, nil); err != nil {
		return composition.ModuleResult{}, err
	}

	// C4 durable-mode fail-fast: in durable (postgres) mode, bootstrapWrapped
	// must always be non-nil before the cell starts. Nil store would cause
	// auditappendbootstrap.HandleEvent to reject every bootstrap-failed event
	// (Reject → DLX) — a permanent data loss in production.
	// Demo/memory mode allows nil (in-memory stores are always non-nil here
	// anyway, but the guard is storage-mode scoped to mirror existing patterns).
	if bootstrapWrapped == nil && shared.Topology.StorageBackend() == "postgres" {
		return composition.ModuleResult{}, fmt.Errorf("auditcore: bootstrap ledger store is nil in durable mode (postgres); " +
			"this is a permanent misconfiguration — auditappendbootstrap would Reject all events")
	}

	// Build the read-side aggregator. auditquery reads across both chains.
	multiStore, err := ledger.NewMultiStore(auditcoreStore, bootstrapInnerStore)
	if err != nil {
		return composition.ModuleResult{}, fmt.Errorf("audit multi-store: %w", err)
	}

	auditOpts := []auditcell.Option{
		auditcell.WithLedgerProtocol(auditProtocol),
		auditcell.WithLedgerStore(auditcoreStore),
		auditcell.WithQueryStore(multiStore),
		auditcell.WithOutboxDeps(outbox.WrapPublisherForCell(shared.Publisher), nil),
		auditcell.WithCursorCodec(cursorCodec),
		auditcell.WithMetricsProvider(shared.MetricsProvider),
		// Wire the bootstrap ledger store into the cell so the auditappendbootstrap
		// subscriber slice can append bootstrap-chain entries. Previously this was
		// exported via ModuleExports.BootstrapLedgerStore to accesscore; after Wave-1
		// #1423 the bootstrap chain write-path is event-driven (accesscore publishes
		// event.auth.bootstrap-failed.v1 → auditcore subscriber appends). The store
		// stays auditcore-internal.
		auditcell.WithBootstrapStore(bootstrapWrapped),
	}

	if shared.Topology.StorageBackend() == "postgres" {
		auditOpts = append(
			auditOpts,
			auditcell.WithOutboxDeps(nil, outbox.WrapWriterForCell(shared.PG.OutboxWriter())),
			auditcell.WithTxManager(persistence.WrapForCell(shared.PG.TxManager())),
		)
	}

	c := auditcell.NewAuditCore(shared.Clock, auditOpts...)

	return composition.ModuleResult{Cell: c}, nil
}

// buildAuditProtocols builds the cursor codec and both ledger protocols
// (auditcore relay chain + bootstrap chain) from environment-sourced secrets.
// Extracted from Provide to keep funlen within 80.
func buildAuditProtocols(shared *composition.SharedDeps) (
	cursorCodec *query.CursorCodec,
	auditProtocol *ledger.Protocol,
	bootstrapProtocol *ledger.Protocol,
	err error,
) {
	// Cursor codec for auditcore.
	auditPrimary, auditPrevious := cellsecrets.LoadCursorKeys("AUDITCORE")
	cursorCodec, err = cellsecrets.BuildCursorCodec(cellsecrets.CursorCodecConfig{
		AdapterMode: shared.Topology.AdapterMode(),
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

	// Build the auditcore relay protocol (HMAC key A, namespace="auditcore").
	auditNamespace, err := ledger.ParseNamespaceID("auditcore")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("auditcore namespace: %w", err)
	}
	auditProtocol, err = buildAuditProtocol(shared.Topology.AdapterMode(),
		"GOCELL_AUDITCORE_HMAC_KEY",
		cellsecrets.LoadCellHMACKey("AUDITCORE"),
		"dev-hmac-key-replace-in-prod!!!!",
		auditNamespace)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("auditcore HMAC key: %w", err)
	}

	// Build the bootstrap protocol (HMAC key B, namespace="bootstrap").
	// Independent HMAC key per chain (ref: hashicorp/vault audit/backend.go
	// per-device Salt) so compromise of one chain's key cannot forge entries
	// in the other.
	bootstrapProtocol, err = buildAuditProtocol(shared.Topology.AdapterMode(),
		"GOCELL_AUDIT_BOOTSTRAP_HMAC_KEY",
		cellsecrets.LoadCellHMACKey("AUDIT_BOOTSTRAP"),
		"dev-hmac-bootstrap-replace-32b!!",
		audit.BootstrapNamespace())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("bootstrap audit HMAC key: %w", err)
	}
	return cursorCodec, auditProtocol, bootstrapProtocol, nil
}

// buildAuditProtocol assembles a ledger.Protocol with an isolated HMAC key.
func buildAuditProtocol(adapterMode, envName, primary, devDefault string, ns ledger.NamespaceID) (*ledger.Protocol, error) {
	hmacKey, err := cellsecrets.BuildHMACKey(cellsecrets.HMACKeyConfig{
		AdapterMode: adapterMode,
		EnvName:     envName,
		Primary:     primary,
		DevDefault:  devDefault,
	})
	if err != nil {
		return nil, err
	}
	defer clear(hmacKey)

	return ledger.NewProtocol(
		ns,
		hmacKey,
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
}

// buildAuditStores constructs the two ledger.Store instances backing the
// auditcore relay chain and the bootstrap chain.
func buildAuditStores(
	shared *composition.SharedDeps,
	auditProtocol, bootstrapProtocol *ledger.Protocol,
) (ledger.Store, ledger.Store, error) {
	if shared.Topology.StorageBackend() == "postgres" {
		if shared.PG == nil {
			return nil, nil, fmt.Errorf("AuditCoreModule: postgres mode requires the postgres capability provider " +
				"(the composition root must provision the postgres capability on SharedDeps before composition.Build)")
		}
		db, poolErr := cellsecrets.PgxPoolFromProvider(shared.PG)
		if poolErr != nil {
			return nil, nil, fmt.Errorf("auditcore: %w", poolErr)
		}
		txMgr := shared.PG.TxManager()
		relayStore, err := adapterpg.NewLedgerStore(db, txMgr, auditProtocol, shared.Clock)
		if err != nil {
			return nil, nil, fmt.Errorf("auditcore LedgerStore: %w", err)
		}
		bootstrapStore, err := adapterpg.NewLedgerStore(db, txMgr, bootstrapProtocol, shared.Clock)
		if err != nil {
			return nil, nil, fmt.Errorf("bootstrap LedgerStore: %w", err)
		}
		return relayStore, bootstrapStore, nil
	}
	relayStore, err := ledger.NewMemStore(auditProtocol, shared.Clock)
	if err != nil {
		return nil, nil, fmt.Errorf("auditcore MemStore: %w", err)
	}
	bootstrapStore, err := ledger.NewMemStore(bootstrapProtocol, shared.Clock)
	if err != nil {
		return nil, nil, fmt.Errorf("bootstrap MemStore: %w", err)
	}
	return relayStore, bootstrapStore, nil
}

var _ composition.CellModule = module{}
