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
	"github.com/ghbvf/gocell/runtime/audit"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// AuditCoreModule wires auditcore: ledger.Protocol + ledger.Store + EventBus + cursor codec.
// It reads auditcore-namespaced environment variables directly.
//
// Since issue #1121 (ADR 202605270230) the bootstrap auth-fail audit chain is
// physically isolated from the auditcore relay chain: two independent
// (Protocol, Store) pairs are constructed here, each with its own NamespaceID
// and HMAC key. The auditquery slice reads from both chains via
// ledger.MultiStore; the bootstrap observer writes only to the bootstrap chain
// via a typed *audit.BootstrapLedgerStore handle.
//
// ref: uber-go/fx fx.Module("auditcore", ...) — self-contained module.
// ref: google/trillian storage/log_storage.go — per-tree partition pattern.
// ref: k8s.io/apiserver/pkg/audit/union.go — read-side fan-out aggregator.
// ref: hashicorp/vault audit/broker.go — per-device Salt isolates HMAC state.
type AuditCoreModule struct{}

// ID returns the stable identifier used in error messages.
func (AuditCoreModule) ID() string { return "auditcore" }

// Provide resolves all auditcore-specific dependencies and returns the
// constructed cell. Two HMAC keys are injected — one per audit chain — so a
// compromise of either key cannot forge entries in the other chain (ADR
// 202605270230 §Threat model, mirroring Vault per-device Salt).
//
// Reads from the environment:
//   - GOCELL_AUDITCORE_HMAC_KEY (relay chain HMAC, namespace="auditcore")
//   - GOCELL_AUDIT_BOOTSTRAP_HMAC_KEY (bootstrap chain HMAC, namespace="bootstrap")
//   - GOCELL_AUDITCORE_CURSOR_KEY / *_PREVIOUS_KEY (cursor codec)
func (AuditCoreModule) Provide(
	ctx context.Context, shared *SharedDeps,
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

	// Build the auditcore relay protocol (HMAC key A, namespace="auditcore").
	auditNamespace, err := ledger.ParseNamespaceID("auditcore")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("auditcore namespace: %w", err)
	}
	auditProtocol, err := buildAuditProtocol(shared.Topology.AdapterMode,
		"GOCELL_AUDITCORE_HMAC_KEY",
		LoadCellHMACKey("AUDITCORE"),
		"dev-hmac-key-replace-in-prod!!!!",
		auditNamespace)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("auditcore HMAC key: %w", err)
	}

	// Build the bootstrap protocol (HMAC key B, namespace="bootstrap").
	// Independent HMAC key per chain (ref: hashicorp/vault audit/backend.go
	// per-device Salt) so compromise of one chain's key does not forge entries
	// in the other.
	bootstrapProtocol, err := buildAuditProtocol(shared.Topology.AdapterMode,
		"GOCELL_AUDIT_BOOTSTRAP_HMAC_KEY",
		LoadCellHMACKey("AUDIT_BOOTSTRAP"),
		"dev-hmac-bootstrap-replace-32b!!",
		audit.BootstrapNamespace())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("bootstrap audit HMAC key: %w", err)
	}

	// Build the two ledger.Store instances (PG or mem). They share the same
	// audit_entries table; the namespace column partitions chains (UNIQUE
	// (namespace, seq_no), UNIQUE (namespace, event_id) from migrations
	// 020/021). pg_advisory_xact_lock keys are hashtext(namespace), so
	// distinct namespaces never contend on the same lock slot.
	auditcoreStore, bootstrapInnerStore, err := buildAuditStores(shared, auditProtocol, bootstrapProtocol)
	if err != nil {
		return nil, nil, nil, err
	}

	// Run strict tail verify against the bootstrap chain at composition time
	// (the auditcore relay chain's tail verify is run by cells/auditcore.Init).
	// A corrupted bootstrap chain blocks startup rather than silently appending
	// to a forked chain at the first 401/429.
	bootstrapWrapped, err := audit.NewBootstrapLedgerStore(bootstrapInnerStore)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("wrap bootstrap audit store: %w", err)
	}
	if err := audit.VerifyBootstrapTailOnStartup(ctx, bootstrapWrapped, nil); err != nil {
		return nil, nil, nil, err
	}

	// Build the read-side aggregator. auditquery reads across both chains so
	// operators can list bootstrap.auth.fail entries via the existing
	// `/api/v1/audit/entries?eventType=bootstrap.auth.fail` filter without a
	// second endpoint.
	multiStore, err := ledger.NewMultiStore(auditcoreStore, bootstrapInnerStore)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("audit multi-store: %w", err)
	}

	auditOpts := []auditcore.Option{
		auditcore.WithLedgerProtocol(auditProtocol),
		auditcore.WithLedgerStore(auditcoreStore),
		auditcore.WithQueryStore(multiStore),
		// Publisher set unconditionally; outboxWriter set conditionally below
		// for postgres mode.
		auditcore.WithOutboxDeps(outbox.WrapPublisherForCell(shared.EventBus), nil),
		auditcore.WithCursorCodec(cursorCodec),
		auditcore.WithMetricsProvider(shared.PromStack.metricProvider),
	}

	if shared.Topology.StorageBackend == "postgres" {
		auditOpts = append(
			auditOpts,
			auditcore.WithOutboxDeps(nil, outbox.WrapWriterForCell(shared.PG.OutboxWriter())),
			auditcore.WithTxManager(persistence.WrapForCell(shared.PG.TxManager())),
		)
	}

	// Cross-module wiring (BOOTSTRAP-AUDIT-CHAIN-WIRING-01, plan 039 W1-2;
	// issue #1121 hardens this to a typed handle):
	//   - shared.BootstrapLedgerStore is consumed by AccessCoreModule.Provide
	//     to build the bootstrap auth-fail observer via
	//     audit.NewBootstrapAuthFailObserver.
	//   - Module order (auditcore before accesscore in
	//     assemblies/corebundle/assembly.yaml) is the happens-before contract;
	//     SharedDeps.Validate intentionally does NOT check this field because
	//     Validate runs before AuditCoreModule.Provide populates it (see
	//     shared_deps_validate.go:103).
	shared.BootstrapLedgerStore = bootstrapWrapped

	c := auditcore.NewAuditCore(shared.Clock, auditOpts...)
	return c, nil, nil, nil
}

// buildAuditProtocol assembles a ledger.Protocol with an isolated HMAC key.
// The key bytes are loaded once, defensively copied by WithChainHMAC, then
// zeroed in the caller's slice via the in-built clear inside WithChainHMAC —
// extracting this into a helper keeps two-protocol construction from
// accidentally aliasing key material.
func buildAuditProtocol(adapterMode, envName, primary, devDefault string, ns ledger.NamespaceID) (*ledger.Protocol, error) {
	hmacKey, err := buildHMACKey(hmacKeyConfig{
		AdapterMode: adapterMode,
		EnvName:     envName,
		Primary:     primary,
		// DEMO-KEY: registered in wellKnownDemoKeys; rejected by rejectDemoKey in real mode.
		DevDefault: devDefault,
	})
	if err != nil {
		return nil, err
	}
	// Belt-and-suspenders: zero the HMAC key on both success and error paths.
	// NewProtocol makes a defensive copy and zeroes the caller's slice on the
	// success path, but if validation fails before the copy is taken the bytes
	// would otherwise remain live on the stack.
	defer clear(hmacKey)

	return ledger.NewProtocol(
		ns,
		hmacKey,
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
}

// buildAuditStores constructs the two ledger.Store instances backing the
// auditcore relay chain and the bootstrap chain. Both use the same backend
// (postgres in production, mem in demo); the chain partition is the
// NamespaceID carried by each protocol.
func buildAuditStores(shared *SharedDeps, auditProtocol, bootstrapProtocol *ledger.Protocol) (ledger.Store, ledger.Store, error) {
	if shared.Topology.StorageBackend == "postgres" {
		if shared.PG == nil {
			return nil, nil, fmt.Errorf("AuditCoreModule: postgres mode requires the postgres capability provider " +
				"(provisionCapabilities must run before BuildApp)")
		}
		db, poolErr := pgxPoolFromProvider(shared.PG)
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

var _ CellModule = AuditCoreModule{}
