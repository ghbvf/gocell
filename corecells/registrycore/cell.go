// Package registrycore implements the registrycore Cell: the runtime contract
// registry's carrier cell (303-US4, #2235), a platform control-plane cell beside
// accesscore / auditcore / configcore (dogfooding — the registry is itself a
// Cell). Its two slices serve the submit and list HTTP contracts over the durable
// ports.Registry store (303-US5, #2236): registrywrite serves
// http.registry.contract.submit.v1 (POST) — it decodes a full contract
// declaration, runs the US3 governance gate (gate.Check), and on success persists
// via ports.Registry; registryread serves http.registry.contract.list.v1 (GET) —
// a tenant-scoped, HMAC-cursor-paginated view. Both are gated by a registry:*
// permission.
//
// US6 (#2237) wires the durable store + governance gate into the submit/list MVP.
// This slice-level wiring uses the in-mem ports.Registry (mem.NewRegistry) and a
// development cursor key, fully exercised by contract/service tests. Deferred to a
// follow-up composition issue: the cellmodule + corebundle composition (PG
// topology, real cursor key via cellsecrets with postgres fail-closed, PDP
// baseline grant) and activation-time declaration persistence. RLS-safe scoped
// reads are wired (#2392): registryread.List runs through scopedread.Do so the PG
// serving role reads under SET LOCAL app.tenant_id; audit/events are US8.
// registrycore never imports a sibling cell.
package registrycore

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/corecells/registrycore/internal/mem"
	"github.com/ghbvf/gocell/corecells/registrycore/slices/registryread"
	"github.com/ghbvf/gocell/corecells/registrycore/slices/registrywrite"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/governance"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/query"
)

// Option configures a RegistryCore cell.
type Option func(*RegistryCore)

// WithTxManager sets the CellTxManager for transactional guarantees (L1
// atomicity). Composition roots construct via persistence.WrapForCell.
func WithTxManager(tx persistence.CellTxManager) Option {
	return func(c *RegistryCore) { c.txManager = tx }
}

// WithCursorCodec sets the HMAC cursor codec for list pagination.
// Required in durable mode — initInternal() fails fast if nil.
// Composition roots construct via cellsecrets.BuildCursorCodec with a
// secret key; demo topology falls back to the built-in dev key (see
// registryCursorDevKey).
func WithCursorCodec(codec *query.CursorCodec) Option {
	return func(c *RegistryCore) { c.cursorCodec = codec }
}

// WithLogger sets an optional structured logger. When omitted the cell uses
// slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(c *RegistryCore) {
		if l != nil {
			c.logger = l
		}
	}
}

// RegistryCore is the platform runtime contract-registry cell. It embeds BaseCell
// and owns the two slice handlers; Init is generated in cell_gen.go and calls
// initInternal. The cell owns the /api/v1/registry prefix (distinct per-cell route
// ownership, like auditcore=/api/v1/audit and accesscore=/api/v1/access).
// +cell:listener:ref=cell.PrimaryListener,prefix=/api/v1/registry
type RegistryCore struct {
	*cell.BaseCell

	// clk stamps registration timestamps in the durable store and the gate.
	clk clock.Clock

	// txManager is the optional L1 transaction manager. When nil, demo mode
	// falls back to outbox.DemoCellTxManager(); durable mode fails fast.
	txManager persistence.CellTxManager
	// cursorCodec is the HMAC codec for list cursors. When nil, demo mode
	// falls back to the dev key; durable mode fails fast.
	cursorCodec *query.CursorCodec
	// logger is the structured logger; defaults to slog.Default().
	logger *slog.Logger

	// +slice:route:slice=registrywrite,subPath=/contracts
	writeHandler *registrywrite.Handler
	// +slice:route:slice=registryread,subPath=/contracts
	readHandler *registryread.Handler
}

// New constructs the registrycore cell. clk is the positional clock dependency
// (clock.Clock convention) the durable store and governance gate use to stamp
// registration timestamps.
func New(clk clock.Clock, opts ...Option) *RegistryCore {
	clock.MustHaveClock(clk, "registrycore.New")
	c := &RegistryCore{
		BaseCell: cell.MustNewBaseCell(loadCellMetadata()),
		clk:      clk,
		logger:   slog.Default(),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// registryCursorDevKey is the development HMAC signing key for list cursors in
// the memory/demo topology. It is NOT a secret and MUST NOT be used in
// production.
//
// DEV/DEMO ONLY: this key is public in the source tree and is only safe
// because registrycore is not yet composed into corebundle for PG topology.
// When the cellmodule lands (follow-up composition issue), the PG topology MUST
// inject a real key via cellsecrets.BuildCursorCodec and MUST fail-closed when
// the env key is absent — never fall back to this dev key. 39 bytes ≥ the
// query.CursorCodec 32-byte minimum.
var registryCursorDevKey = []byte("registrycore-list-cursor-dev-key-0001!!")

// const messages — MESSAGE-CONST-LITERAL-01: errcode.New message must be a literal.
const msgCellMissingCodecDurable = "registrycore durable mode requires a cursor codec; " +
	"use WithCursorCodec(query.NewCursorCodec(secret)) — " +
	"the built-in demo key is public in the source tree"

// initInternal is the K#04 hand-written init hook invoked by the generated Init
// after BaseCell.Init and before the generated route-group mount. It builds the
// durable-store-backed slice services and the handlers the generated route group
// references. No external I/O, no goroutines, fail-fast (cell-patterns.md §Init
// fail-fast).
//
// DurabilityMode enforcement (F2): in demo mode tx and codec fall back to safe
// in-process defaults with a Warn log; in durable mode a nil tx or codec is a
// startup error (fail-closed, mirrors auditcore/configcore pattern).
//
// MVP wiring (303-US6, #2237): the in-mem ports.Registry (mem.NewRegistry) is the
// durable store both slices share — a submit persists a registration the list
// slice reads back. The submit service validates each declaration through the US3
// governance gate (gate.Check) before persisting; the gate is constructed over a
// throwaway in-mem ContractRegistrar because Check is side-effect-free — only
// gate.Submit (the sole writer of that registrar) would touch it, and gate.Submit
// is never called from the persistent store path (persistence goes through
// ports.Registry.Create); this binding is enforced by archtest
// REGISTRAR-SUBMIT-CALLER-01.
func (c *RegistryCore) initInternal(_ context.Context, reg cell.Registrar) error {
	durabilityMode := reg.DurabilityMode()

	// Resolve TxManager: nil → demo fallback (warn) in demo mode; fail-closed in durable.
	txMgr := c.txManager
	if txMgr == nil {
		c.logger.Warn("registrycore: using outbox.DemoCellTxManager (demo mode)",
			slog.String("durability_mode", durabilityMode.String()))
		txMgr = outbox.DemoCellTxManager()
	}
	// Guard: DemoTxRunner implements Nooper — reject it in DurabilityDurable mode
	// so that assemblies that forget to wire a real TxManager fail at Init() time.
	if err := outbox.CheckNotNoop(durabilityMode, "registrycore", txMgr); err != nil {
		return err
	}

	// Resolve cursor codec: nil → demo fallback (warn) in demo mode; fail-closed in durable.
	codec := c.cursorCodec
	if codec == nil {
		if durabilityMode == outbox.DurabilityDurable {
			return errcode.New(errcode.KindInternal, errcode.ErrCellMissingCodec,
				msgCellMissingCodecDurable)
		}
		// Each cell uses a distinct demo key to prevent cross-cell cursor reuse in demo mode.
		var err error
		codec, err = query.NewCursorCodec(registryCursorDevKey)
		if err != nil {
			return fmt.Errorf("registrycore: build list cursor codec: %w", err)
		}
		c.logger.Warn("registrycore: using default cursor codec (demo mode)",
			slog.String("cell", c.ID()))
	}

	// RunModeForDemo: stale cursors after process restart fail-open to page 1 in
	// demo/in-mem topology; durable topology uses RunModeProd (fail-closed cursor
	// validation). The PG cellmodule will pass RunModeForDemo(false).
	runMode := query.RunModeForDemo(durabilityMode == outbox.DurabilityDemo)

	store := mem.NewRegistry(c.clk)
	gate := governance.NewRegistrationGate(registry.NewContractRegistrar(c.clk), c.clk)

	writeSvc, err := registrywrite.NewService(store, gate, registrywrite.WithTxManager(txMgr))
	if err != nil {
		return fmt.Errorf("registrycore: build registrywrite service: %w", err)
	}
	readSvc, err := registryread.NewService(store, txMgr, codec, runMode, nil)
	if err != nil {
		return fmt.Errorf("registrycore: build registryread service: %w", err)
	}
	c.writeHandler = registrywrite.NewHandler(writeSvc)
	c.readHandler = registryread.NewHandler(readSvc)

	// Register the slices into the BaseCell inventory (OwnedSlices), mirroring
	// configcore/auditcore/accesscore: the generated slice_gen.go SliceMetadata()
	// is wired into runtime inventory rather than left dangling, so the declared
	// slice set and the cell's runtime inventory stay in sync.
	c.AddSlice(cell.MustNewBaseSliceFromMeta(registrywrite.SliceMetadata()))
	c.AddSlice(cell.MustNewBaseSliceFromMeta(registryread.SliceMetadata()))

	// Register the in-mem repo readiness probe via the cellgen-generated typed
	// funnel (PROBENAME-SEALED-FUNNEL-01). mem.Registry.RepoReady always returns
	// nil (in-memory store is always ready); the PG implementation will perform a
	// real connectivity check.
	return RegisterReadiness(reg, store)
}
