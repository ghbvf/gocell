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
	"os"

	"github.com/ghbvf/gocell/adapters/adapterutil"
	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/cellmodules/cellsecrets"
	auditcell "github.com/ghbvf/gocell/corecells/auditcore"
	"github.com/ghbvf/gocell/framework/kernel/healthz"
	kernellifecycle "github.com/ghbvf/gocell/framework/kernel/lifecycle"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/kernel/worker"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/runtime/audit"
	"github.com/ghbvf/gocell/framework/runtime/audit/ledger"
	"github.com/ghbvf/gocell/framework/runtime/composition"
)

// auditAdminPoolResource is the kernellifecycle.ManagedResource wrapping the
// OPTIONAL cross-tenant audit admin pool (#1810). It is Closed in LIFO order on
// graceful shutdown AND contributes ONE readiness probe:
// ProbeAuditAdminRestrictedReady (backed by Pool.AuditAdminReadyCheck). That
// probe asserts (1) the admin pool's current_user is NOBYPASSRLS / non-superuser
// (the gocell_audit_admin role reads cross-tenant via a role-scoped permissive
// RLS policy, NOT BYPASSRLS — ADR #1676) AND (2) the role has SELECT privilege
// on audit_entries (a missing GRANT would yield a pool that passes the role probe
// but fails every cross-tenant query at runtime — #1810 F4). A distinct probe
// name (vs the serving pool's postgres_app_role_restricted_ready) avoids a
// probe-name collision while still making admin-pool failure observable.
type auditAdminPoolResource struct {
	pool *adapterpg.Pool
}

func (r auditAdminPoolResource) Probes() []healthz.Probe {
	return []healthz.Probe{
		adapterutil.HealthToProbe(
			adapterpg.ProbeAuditAdminRestrictedReady,
			r.pool.AuditAdminReadyCheck,
			adapterutil.DefaultProbeTimeout,
		),
	}
}
func (auditAdminPoolResource) Worker() worker.Worker { return nil }
func (r auditAdminPoolResource) Close(ctx context.Context) error {
	return r.pool.Close(ctx)
}

type module struct{}

// Module returns a composition.CellModule that wires the auditcore Cell.
func Module() composition.CellModule { return module{} }

// ID returns the stable identifier used in error messages and logs.
// auditCellID is auditcore's stable cell id — the module's ID() and the key it
// resolves its pool provider by (shared.PG.ForCell). The Builder enforces
// module.ID() == cell.ID(); ForCell(auditCellID) must agree, so it is sourced once.
const auditCellID = "auditcore"

func (module) ID() string { return auditCellID }

// Provide resolves all auditcore-specific dependencies from the composition
// shared context and returns the constructed Cell.
//
// Reads from the environment:
//   - GOCELL_AUDITCORE_HMAC_KEY (relay chain HMAC, namespace="auditcore")
//   - GOCELL_AUDIT_BOOTSTRAP_HMAC_KEY (bootstrap chain HMAC, namespace="bootstrap")
//   - GOCELL_AUDITCORE_CURSOR_KEY / *_PREVIOUS_KEY (cursor codec)
//   - GOCELL_AUDIT_ADMIN_DSN (optional): when present in postgres topology,
//     constructs a second pool backed by the gocell_audit_admin role for
//     super-admin cross-tenant reads (#1810). When absent, super-admin reads
//     return HTTP 501 (graceful fail-closed, never fail-open).
func (m module) Provide(
	ctx context.Context, shared *composition.SharedDeps,
) (composition.ModuleResult, error) {
	cursorCodec, auditProtocol, bootstrapProtocol, err := buildAuditProtocols(shared)
	if err != nil {
		return composition.ModuleResult{}, err
	}

	// Build the two ledger.Store instances (PG or mem) plus their mem-typed
	// counterparts for cross-tenant wiring in demo mode.
	auditcoreStore, bootstrapInnerStore, relayMem, bootstrapMem, err := buildAuditStoresWithMem(shared, auditProtocol, bootstrapProtocol)
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

	// Storage-mode-specific serving + cross-tenant opts (postgres: per-cell pool +
	// optional admin pool; memory: mem cross-tenant store) plus any ManagedResources
	// the module opens itself (the optional admin cross-tenant pool, #1810). The
	// serving pool is owned by shared.PG, not the module.
	storageOpts, resources, err := auditStorageOpts(ctx, shared, relayMem, bootstrapMem)
	if err != nil {
		return composition.ModuleResult{}, err
	}
	auditOpts = append(auditOpts, storageOpts...)

	c := auditcell.NewAuditCore(shared.Clock, auditOpts...)

	return composition.ModuleResult{Cell: c, Resources: resources}, nil
}

// auditStorageOpts builds the storage-mode-specific auditcore options + opened
// ManagedResources. Postgres: resolves THIS cell's pool provider (#2341 ForCell —
// colocated shares one pool, split gives auditcore its own) for the serving
// outbox/tx deps, plus the optional admin cross-tenant pool (#1810, gated on
// GOCELL_AUDIT_ADMIN_DSN; absent → super-admin reads stay 501, present-but-failed →
// fail-fast). Memory: a MemCrossTenantStore so cross-tenant reads work without an
// admin pool. Extracted from Provide to keep its cognitive complexity within bounds.
func auditStorageOpts(
	ctx context.Context, shared *composition.SharedDeps, relayMem, bootstrapMem *ledger.MemStore,
) ([]auditcell.Option, []kernellifecycle.ManagedResource, error) {
	if shared.Topology.StorageBackend() != "postgres" {
		// Demo/memory topology: both mem stores are guaranteed non-nil here
		// (buildAuditStoresWithMem populates relayMem and bootstrapMem).
		ctStore, ctErr := ledger.NewMemCrossTenantStore(relayMem, bootstrapMem)
		if ctErr != nil {
			return nil, nil, fmt.Errorf("auditcore: mem cross-tenant store: %w", ctErr)
		}
		return []auditcell.Option{auditcell.WithCrossTenantQueryStore(ctStore)}, nil, nil
	}

	pg, pgErr := shared.PG.ForCell(auditCellID)
	if pgErr != nil {
		return nil, nil, fmt.Errorf("auditcore: %w", pgErr)
	}
	opts := []auditcell.Option{
		auditcell.WithOutboxDeps(nil, outbox.WrapWriterForCell(pg.OutboxWriter())),
		auditcell.WithTxManager(persistence.WrapForCell(pg.TxManager())),
	}
	crossTenantStore, ctRes, ctErr := buildCrossTenantStore(ctx, shared)
	if ctErr != nil {
		return nil, nil, ctErr
	}
	if crossTenantStore == nil {
		return opts, nil, nil
	}
	opts = append(opts, auditcell.WithCrossTenantQueryStore(crossTenantStore))
	return opts, []kernellifecycle.ManagedResource{ctRes}, nil
}

// buildCrossTenantStore constructs the admin-pool-backed CrossTenantQueryStore
// for super-admin cross-tenant reads (#1810). Returns (nil, nil, nil) when
// GOCELL_AUDIT_ADMIN_DSN is not set — the caller treats nil as "not provisioned"
// and leaves super-admin reads fail-closed at HTTP 501. Returns (nil, nil, err)
// when the env var is set but the pool construction or ping fails — FAIL-FAST.
// On success it returns the store AND a poolCloseResource so the admin pool is
// Closed in LIFO order on graceful shutdown (registered via ModuleResult.Resources).
//
// The admin pool connects with the gocell_audit_admin role which has a
// role-scoped permissive RLS SELECT policy (migration 065 — USING(true)), so it
// can read every tenant's rows without BYPASSRLS, preserving ADR #1676.
// No localhost fallback; no default credentials.
func buildCrossTenantStore(
	ctx context.Context, shared *composition.SharedDeps,
) (ledger.CrossTenantQueryStore, kernellifecycle.ManagedResource, error) {
	adminDSN := os.Getenv("GOCELL_AUDIT_ADMIN_DSN")
	if adminDSN == "" {
		// Not provisioned — super-admin reads stay gracefully unavailable (501);
		// nil store + nil resource is the sanctioned "absent" sentinel (caller
		// checks crossTenantStore != nil before wiring).
		return nil, nil, nil //nolint:nilnil // nil-triple = "not provisioned" sentinel; see godoc
	}
	if shared.PG == nil {
		return nil, nil, fmt.Errorf("auditcore: GOCELL_AUDIT_ADMIN_DSN is set but postgres capability is nil " +
			"(composition root must provision the postgres capability on SharedDeps)")
	}
	// Build a dedicated admin pool. RequireRestrictedRole is false: the
	// gocell_audit_admin role is non-owner and non-BYPASSRLS — it reads cross-tenant
	// via a role-scoped permissive RLS SELECT policy (migration 065), NOT via
	// BYPASSRLS. The serving pool's ProbeAppRoleRestrictedReady probe only covers the
	// FORCE RLS enforcement check for the serving pool; the admin pool uses its own
	// AuditAdminReadyCheck (which covers both role-attributes AND SELECT on
	// audit_entries) registered via ProbeAuditAdminRestrictedReady (#1810 F3/F4).
	adminPool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: adminDSN})
	if err != nil {
		// DSN set but pool construction/ping failed → FAIL-FAST (no silent fallback).
		return nil, nil, fmt.Errorf("auditcore: admin cross-tenant pool: %w", err)
	}
	// Composition-time preflight (#1810 F3): assert the admin pool's role is
	// correctly provisioned — non-superuser, NOBYPASSRLS, and has SELECT on
	// audit_entries. A misconfigured role or missing GRANT must abort composition
	// (fail-fast), not surface at first request. Close the pool on failure to
	// avoid a leaked connection.
	if prefErr := adminPool.AuditAdminReadyCheck(ctx); prefErr != nil {
		_ = adminPool.Close(ctx)
		return nil, nil, fmt.Errorf("auditcore: admin pool role preflight failed "+
			"(GOCELL_AUDIT_ADMIN_DSN is set but the role is misconfigured — "+
			"ensure gocell_audit_admin is NOSUPERUSER NOBYPASSRLS with SELECT on audit_entries): %w",
			prefErr)
	}
	store, err := adapterpg.NewAuditCrossTenantStore(adminPool.DB())
	if err != nil {
		_ = adminPool.Close(ctx)
		return nil, nil, fmt.Errorf("auditcore: NewAuditCrossTenantStore: %w", err)
	}
	return store, auditAdminPoolResource{pool: adminPool}, nil
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

// buildAuditStoresWithMem constructs the two ledger.Store instances backing the
// auditcore relay chain and the bootstrap chain. For the memory topology it
// additionally returns the concrete *ledger.MemStore pointers needed to build a
// MemCrossTenantStore for cross-tenant reads in demo mode; for the postgres
// topology both concrete pointers are nil (the cross-tenant store uses its own
// admin pool).
func buildAuditStoresWithMem(
	shared *composition.SharedDeps,
	auditProtocol, bootstrapProtocol *ledger.Protocol,
) (auditcoreStore ledger.Store, bootstrapStore ledger.Store, relayMem *ledger.MemStore, bootstrapMem *ledger.MemStore, err error) {
	if shared.Topology.StorageBackend() == "postgres" {
		if shared.PG == nil {
			return nil, nil, nil, nil, fmt.Errorf("AuditCoreModule: postgres mode requires the postgres capability provider " +
				"(the composition root must provision the postgres capability on SharedDeps before composition.Build)")
		}
		pg, pgErr := shared.PG.ForCell(auditCellID)
		if pgErr != nil {
			return nil, nil, nil, nil, fmt.Errorf("auditcore: %w", pgErr)
		}
		db, poolErr := cellsecrets.PgxPoolFromProvider(pg)
		if poolErr != nil {
			return nil, nil, nil, nil, fmt.Errorf("auditcore: %w", poolErr)
		}
		txMgr := pg.TxManager()
		relayStore, relayErr := adapterpg.NewLedgerStore(db, txMgr, auditProtocol, shared.Clock)
		if relayErr != nil {
			return nil, nil, nil, nil, fmt.Errorf("auditcore LedgerStore: %w", relayErr)
		}
		bsStore, bsErr := adapterpg.NewLedgerStore(db, txMgr, bootstrapProtocol, shared.Clock)
		if bsErr != nil {
			return nil, nil, nil, nil, fmt.Errorf("bootstrap LedgerStore: %w", bsErr)
		}
		// relayMem/bootstrapMem are nil for postgres — cross-tenant uses admin pool.
		return relayStore, bsStore, nil, nil, nil //nolint:nilnil // intentional: typed *ledger.MemStore nils; caller checks before use
	}
	relayStore, relayErr := ledger.NewMemStore(auditProtocol, shared.Clock)
	if relayErr != nil {
		return nil, nil, nil, nil, fmt.Errorf("auditcore MemStore: %w", relayErr)
	}
	bsStore, bsErr := ledger.NewMemStore(bootstrapProtocol, shared.Clock)
	if bsErr != nil {
		return nil, nil, nil, nil, fmt.Errorf("bootstrap MemStore: %w", bsErr)
	}
	return relayStore, bsStore, relayStore, bsStore, nil
}

var _ composition.CellModule = module{}
