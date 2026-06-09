// Package auditcore implements the auditcore Cell: tamper-evident audit log
// with hash chain (via runtime/audit/ledger framework), event consumption,
// and query.
package auditcore

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/corecells/auditcore/internal/appender"
	"github.com/ghbvf/gocell/corecells/auditcore/slices/auditappendbootstrap"
	"github.com/ghbvf/gocell/corecells/auditcore/slices/auditappendconfig"
	"github.com/ghbvf/gocell/corecells/auditcore/slices/auditappendrole"
	"github.com/ghbvf/gocell/corecells/auditcore/slices/auditappendsession"
	"github.com/ghbvf/gocell/corecells/auditcore/slices/auditappenduser"
	"github.com/ghbvf/gocell/corecells/auditcore/slices/auditquery"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/audit"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

// Compile-time interface check lives in cell_gen.go (DO NOT EDIT).

// tailVerifyStartupTimeout caps strictTailVerifyOnStartup so that a slow or
// hung store cannot stall k8s readiness indefinitely (F-04).
const tailVerifyStartupTimeout = 30 * time.Second

// Option configures an AuditCore Cell.
type Option func(*AuditCore)

// WithLedgerProtocol injects the *ledger.Protocol into the Cell.
//
// Both bare-nil and typed-nil are rejected at Init() time (ledgerProtocolNil
// sentinel sticky). Pattern mirrors runtime/auth/session WithFingerprint
// (strong-dependency wiring option — runtime-api.md §Option 范式分层).
func WithLedgerProtocol(p *ledger.Protocol) Option {
	return func(c *AuditCore) {
		if p == nil {
			c.ledgerProtocolNil = true
			return
		}
		c.ledgerProtocol = p
	}
}

// WithLedgerStore injects the ledger.Store into the Cell. Used by the
// appender slices for writes and by strict tail verify at startup.
//
// Both bare-nil and typed-nil are rejected at Init() time (ledgerStoreNil
// sentinel sticky). Pattern mirrors WithLedgerProtocol above.
func WithLedgerStore(s ledger.Store) Option {
	return func(c *AuditCore) {
		if validation.IsNilInterface(s) {
			c.ledgerStoreNil = true
			return
		}
		c.ledgerStore = s
	}
}

// WithQueryStore injects a narrow QueryStore for the auditquery slice. When
// not supplied, the slice falls back to the ledger.Store wired via
// WithLedgerStore (which satisfies QueryStore by structural typing).
//
// The composition root uses this option to inject a ledger.MultiStore that
// fans out reads across multiple chains (issue #1121 / ADR 202605270230 —
// the auditcore relay chain and the bootstrap chain). Aggregator types that
// implement only QueryStore (e.g. *ledger.MultiStore) are deliberately not
// accepted by WithLedgerStore — a compile error prevents routing writes
// through a read-side fan-out.
//
// Builder-style noop on typed-nil — final validation happens in Init when
// the slice services are constructed.
func WithQueryStore(s ledger.QueryStore) Option {
	return func(c *AuditCore) {
		if validation.IsNilInterface(s) {
			return
		}
		c.queryStore = s
	}
}

// WithBootstrapStore injects the sealed *audit.BootstrapLedgerStore into the
// Cell so the auditappendbootstrap subscriber slice can write bootstrap-chain
// entries. This is an internal wiring option — it is only callable from the
// cellmodules/auditcore composition-root layer.
//
// Bare-nil inputs are silently ignored (builder-option semantics). The final
// validation happens in initSlices: DurabilityDurable mode with a nil store
// fails fast at Init() (ErrCellMissingBootstrapStore); demo/test mode tolerates
// nil — the auditappendbootstrap.Service then permanently Rejects any consumed
// event to DLX rather than Requeueing.
func WithBootstrapStore(s *audit.BootstrapLedgerStore) Option {
	return func(c *AuditCore) {
		if s != nil {
			c.bootstrapStore = s
		}
	}
}

// WithEmitter injects a pre-composed outbox.CellEmitter directly into the Cell.
// Preferred path for tests and for composition roots that have already built
// an Emitter.
//
// Mutually exclusive with WithOutboxDeps — setting both causes Init() to
// fail fast with ErrCellInvalidConfig. Durability for L2 slice decisions is
// derived from the injected emitter's Durable() method (DurabilityReporter).
//
// ref: kubernetes/client-go rest.RESTClientFor — factory composes the typed
// client; resulting struct does not retain raw config fields.
func WithEmitter(e outbox.CellEmitter) Option {
	return func(c *AuditCore) { c.emitter = e }
}

// WithOutboxDeps wires sealed outbox dependencies (CellPublisher +
// CellWriter). Composition roots construct each via
// outbox.WrapPublisherForCell / outbox.WrapWriterForCell. The framework
// composes them into an outbox.Emitter at Init() time via
// outbox.ResolveCellEmitter.
//
// Accumulative: a nil argument leaves the previously-set value in place;
// multiple calls combine their non-nil arguments. Does NOT clear previous
// state — `WithOutboxDeps(nil, nil)` is a no-op, not a reset. Mutually
// exclusive with WithEmitter; Init() fails fast if both are set.
//
// AI-HARD per ADR cell-raw-infra-sealed-marker: the option signature
// rejects raw outbox.Publisher / outbox.Writer at compile time.
func WithOutboxDeps(pub outbox.CellPublisher, writer outbox.CellWriter) Option {
	return func(c *AuditCore) {
		if pub != nil {
			c.pendingOutboxPub = pub
		}
		if writer != nil {
			c.pendingOutboxWriter = writer
		}
	}
}

// WithLogger sets the structured logger.
func WithLogger(l *slog.Logger) Option {
	return func(c *AuditCore) { c.logger = l }
}

// WithTxManager sets the CellTxManager for transactional guarantees (L2
// atomicity). Composition roots construct via persistence.WrapForCell.
func WithTxManager(tx persistence.CellTxManager) Option {
	return func(c *AuditCore) { c.txRunner = tx }
}

// WithMetricsProvider sets the metrics provider used by the DirectEmitter in
// demo mode. Required when WithOutboxDeps sets a publisher without a real
// outboxWriter. Pass metrics.NopProvider{} explicitly in tests.
func WithMetricsProvider(p metrics.Provider) Option {
	return func(c *AuditCore) { c.metricsProvider = p }
}

// WithCursorCodec sets the cursor codec for pagination.
func WithCursorCodec(codec *query.CursorCodec) Option {
	return func(c *AuditCore) { c.cursorCodec = codec }
}

// AuditCore is the auditcore Cell implementation.
// +cell:listener:ref=cell.PrimaryListener,prefix=/api/v1/audit
type AuditCore struct {
	*cell.BaseCell

	// ledger framework dependencies (injected by composition root).
	ledgerProtocol    *ledger.Protocol
	ledgerStore       ledger.Store      // appender writes + startup tail verify
	queryStore        ledger.QueryStore // auditquery reads; defaults to ledgerStore in Init when nil
	ledgerProtocolNil bool              // sentinel: WithLedgerProtocol received nil
	ledgerStoreNil    bool              // sentinel: WithLedgerStore received typed-nil/bare-nil

	// Outbox wiring (see WithEmitter / WithOutboxDeps godoc). Sealed marker
	// types prevent any cell.go public Option from accepting raw
	// outbox.Publisher / outbox.Writer at compile time (ADR
	// cell-raw-infra-sealed-marker §D1).
	emitter             outbox.CellEmitter
	pendingOutboxPub    outbox.CellPublisher
	pendingOutboxWriter outbox.CellWriter

	txRunner        persistence.CellTxManager
	cursorCodec     *query.CursorCodec
	logger          *slog.Logger
	metricsProvider metrics.Provider
	clk             clock.Clock

	appendSessionSvc *auditappendsession.Service

	appendUserSvc *auditappenduser.Service

	appendConfigSvc *auditappendconfig.Service

	appendRoleSvc *auditappendrole.Service

	// bootstrapStore is the sealed handle for the bootstrap audit chain,
	// injected via WithBootstrapStore from the cellmodules/auditcore composition
	// root. It feeds the auditappendbootstrap subscriber slice. Not exported —
	// this is an internal wiring detail.
	bootstrapStore *audit.BootstrapLedgerStore

	appendBootstrapSvc *auditappendbootstrap.Service

	// +slice:route:slice=auditquery,subPath=
	queryHandler *auditquery.Handler
}

// NewAuditCore creates a new AuditCore Cell.
func NewAuditCore(clk clock.Clock, opts ...Option) *AuditCore {
	clock.MustHaveClock(clk, "auditcore.New")
	c := &AuditCore{
		BaseCell: cell.MustNewBaseCell(loadCellMetadata()),
		clk:      clk,
		logger:   slog.Default(),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// initInternal is the K#04 codegen escape hatch: business init that cannot
// be generated (emitter resolve, slice service construction, health probes).
// cell_gen.go::Init calls it after BaseCell.Init and before mounting the
// generated route-group and subscribe blocks. This is a permanent convention,
// not a transitional shim.
func (c *AuditCore) initInternal(ctx context.Context, reg cell.Registrar) error {
	// Validate injected ledger deps (strong-dependency wiring options).
	if c.ledgerProtocolNil || c.ledgerProtocol == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"auditcore: LedgerProtocol required; use WithLedgerProtocol (composition root must construct via ledger.NewProtocol)")
	}
	if c.ledgerStoreNil || validation.IsNilInterface(c.ledgerStore) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"auditcore: LedgerStore required; use WithLedgerStore")
	}

	durabilityMode := reg.DurabilityMode()

	if err := c.resolveEmitter(durabilityMode); err != nil {
		return err
	}
	// resolveEmitter enforces the (OutboxWriter, TxRunner) pairing invariant
	// using the original c.txRunner; only after it succeeds do we install the
	// demoTxRunner fallback so slice constructors see a non-nil TxRunner.
	if c.txRunner == nil {
		c.logger.Warn("auditcore: using outbox.DemoCellTxManager (demo mode)",
			slog.String("durability_mode", durabilityMode.String()))
		c.txRunner = outbox.DemoCellTxManager()
	}
	// Guard: DemoTxRunner implements Nooper — reject it in DurabilityDurable mode
	// so that assemblies that forget to wire a real TxRunner fail at Init() time.
	if err := outbox.CheckNotNoop(durabilityMode, "auditcore", c.txRunner); err != nil {
		return err
	}

	// F1: RestartRecoveryStrictTailVerify — verify hash chain integrity before
	// accepting new entries. Runs before initSlices so a tampered or corrupted
	// chain surfaces at startup rather than at the first consumer HandleEvent.
	//
	// ref: google/trillian log/sequencer.go IntegrateBatch — verifies tree
	// integrity before accepting new leaves (same fail-fast invariant).
	if _, ok := c.ledgerProtocol.RestartRecovery().(ledger.RestartRecoveryStrictTailVerify); ok {
		if err := c.strictTailVerifyOnStartup(ctx); err != nil {
			return err
		}
	}

	if err := c.initSlices(durabilityMode); err != nil {
		return err
	}
	// Default cursor codec for pagination if not injected. Durable mode
	// refuses the public demo-key fallback — an assembly that forgets to
	// wire a production codec must fail closed, not silently sign cursors
	// with a key that ships in the source tree.
	// ref: zeromicro/go-zero MustSetUp — fatal on insecure default config.
	if err := c.initCursorCodec(durabilityMode); err != nil {
		return err
	}
	if err := c.initQuerySlice(durabilityMode); err != nil {
		return err
	}

	return c.registerHealthProbes(reg)
}

// registerHealthProbes registers all health probes from the emitter and the
// ledger store. Extracted from initInternal to keep cognitive complexity ≤ 15.
//
// Two semantic categories are registered here:
//   - Emitter fail-open-rate probe (healthz.ProbeSet): checks the ratio of
//     dropped outbox publishes. Only present when the emitter is a DirectEmitter.
//   - Ledger store readiness probe (healthz.RepoProber): checks audit_entries
//     connectivity via RegisterReadiness typed funnel. ledger.Store
//     always satisfies RepoProber — MemStore returns nil (always ready),
//     PG-backed store issues a Tail query against the relation.
func (c *AuditCore) registerHealthProbes(reg cell.Registrar) error {
	// Register emitter health probes (fail-open rate checker) via the shared
	// kernel funnel.
	if err := cell.RegisterEmitterHealthProbes(reg, c.emitter); err != nil {
		return err
	}
	// Register ledger store readiness probe via the cellgen-generated typed funnel.
	// ledger.Store satisfies healthz.RepoProber (RepoReady method).
	return RegisterReadiness(reg, c.ledgerStore)
}

// strictTailVerifyOnStartup implements the RestartRecoveryStrictTailVerify
// protocol: reads the tail and verifies the entire chain before accepting
// new entries. Returns ErrAuditChainBroken if any entry is tampered or
// the chain linkage is invalid.
//
// A 30 s timeout (tailVerifyStartupTimeout) is imposed so that a slow or
// hung store cannot stall k8s readiness indefinitely (F-04). The timeout
// applies to the entire Tail + Verify sequence.
//
// Scope note: covers only the ctx-scoped chain (the "" system chain when
// unscoped at startup). Per-tenant relay sub-chains are verified on-demand.
// Full admin enumeration of all per-tenant chains is tracked at gh #1755
// (blocked by NOBYPASSRLS serving role).
//
// Called from initInternal before initSlices; a failure prevents the cell
// from ever serving traffic, surfacing corruption at process startup.
func (c *AuditCore) strictTailVerifyOnStartup(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, tailVerifyStartupTimeout)
	defer cancel()

	tail, err := c.ledgerStore.Tail(ctx)
	if err != nil {
		return fmt.Errorf("auditcore: tail recovery failed: %w", err)
	}
	if tail.SeqNo == 0 {
		// Empty store — nothing to verify.
		c.logger.Info("auditcore: tail verify passed (empty store)")
		return nil
	}
	valid, firstInvalid, err := c.ledgerStore.Verify(ctx, 1, tail.SeqNo)
	if err != nil {
		return fmt.Errorf("auditcore: tail verify failed: %w", err)
	}
	if !valid {
		return errcode.New(errcode.KindInternal, errcode.ErrAuditChainBroken,
			"auditcore: chain integrity broken on startup",
			errcode.WithDetails(errcode.PublicInt("first_invalid_seq", firstInvalid)))
	}
	c.logger.Info("auditcore: tail verify passed", slog.Int64("seq_no", tail.SeqNo))
	return nil
}

// resolveEmitter delegates to outbox.ResolveCellEmitter (mutual exclusion +
// WithEmitter durable guard + ResolveEmitter delegation + L2 non-durable
// warn) and clears the pending outbox dep fields.
//
// auditcore uses DirectPublishFailClosed: audit.appended events are the source
// of truth for compliance; publisher failure must surface to the caller so ops
// notices outages instead of silently losing events. Opt-in fail-open is
// per-entry via outbox.Entry.FailurePolicy, and archtest
// OUTBOX-TOPIC-FAILOPEN-01 bans it for audit.* topics.
func (c *AuditCore) resolveEmitter(mode outbox.DurabilityMode) error {
	resolved, err := outbox.ResolveCellEmitter(c.clk, outbox.CellEmitterInputs{
		EmitterConfig: outbox.EmitterConfig{
			CellID:            "auditcore",
			Mode:              mode,
			Publisher:         c.pendingOutboxPub,
			OutboxWriter:      c.pendingOutboxWriter,
			TxRunner:          c.txRunner,
			Logger:            c.logger,
			DirectPublishMode: outbox.DirectPublishFailClosed,
			MetricsProvider:   c.metricsProvider,
		},
		PreResolved:      c.emitter,
		ConsistencyLevel: c.ConsistencyLevel(),
	})
	if err != nil {
		return err
	}
	c.emitter = resolved
	c.pendingOutboxPub = nil
	c.pendingOutboxWriter = nil
	return nil
}

// initSlices constructs the 4 auditappend sub-slices plus the
// auditappendbootstrap subscriber slice.
// auditquery is initialized separately in initQuerySlice after cursor codec resolve.
//
// All 4 auditappend* slices share the same appender.Service implementation;
// each slice package contributes only its Spec (see slices/auditappendxxx/
// service.go). The single-source ext is enforced by AUDITCORE-APPENDER-
// SINGLE-SOURCE-01 archtest plus the type-system Hard defenses in
// corecells/auditcore/internal/appender (sealed Spec / sealed ActorMode /
// type alias forbidding methods on non-local types).
//
// L2: store.Append + emitter.Emit run inside the same txRunner.RunInTx block
// (OutboxFact pattern). Consumer receives cross-cell events (L3 source), but
// the write side is L2 atomic — F3 correction.
func (c *AuditCore) initSlices(mode outbox.DurabilityMode) error {
	appenders := []struct {
		spec     appender.Spec
		target   **appender.Service
		metadata func() *metadata.SliceMeta
	}{
		{auditappendsession.Spec, &c.appendSessionSvc, auditappendsession.SliceMetadata},
		{auditappenduser.Spec, &c.appendUserSvc, auditappenduser.SliceMetadata},
		{auditappendconfig.Spec, &c.appendConfigSvc, auditappendconfig.SliceMetadata},
		{auditappendrole.Spec, &c.appendRoleSvc, auditappendrole.SliceMetadata},
	}
	for _, a := range appenders {
		svc, err := appender.NewService(
			a.spec, c.ledgerStore, c.ledgerProtocol, c.logger, c.clk,
			appender.WithEmitter(c.emitter),
			appender.WithTxManager(c.txRunner),
		)
		if err != nil {
			return fmt.Errorf("%s: %w", a.spec.Name(), err)
		}
		*a.target = svc
		c.AddSlice(cell.MustNewBaseSliceFromMeta(a.metadata()))
	}

	// Bootstrap subscriber slice — always initialized. In DurabilityDurable mode
	// a nil bootstrapStore is a wiring mistake: the slice would consume
	// event.auth.bootstrap-failed.v1 and permanently Reject each one to DLX
	// (see auditappendbootstrap.Service.HandleEvent). Fail fast at Init() so the
	// misconfiguration surfaces at process startup, not on the first auth-fail
	// event. Demo/test mode tolerates nil (HandleEvent Rejects, but no real
	// bootstrap events flow). Mirrors the CheckNotNoop / initCursorCodec durable
	// guards above.
	if mode == outbox.DurabilityDurable && c.bootstrapStore == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrCellMissingBootstrapStore,
			"auditcore durable mode requires a bootstrap ledger store; "+
				"wire one via WithBootstrapStore from the cellmodules/auditcore composition root")
	}
	bootstrapSvc, err := auditappendbootstrap.NewService(c.clk,
		auditappendbootstrap.WithBootstrapStore(c.bootstrapStore),
	)
	if err != nil {
		return fmt.Errorf("auditappendbootstrap: %w", err)
	}
	c.appendBootstrapSvc = bootstrapSvc
	c.AddSlice(cell.MustNewBaseSliceFromMeta(auditappendbootstrap.SliceMetadata()))

	return nil
}

// initQuerySlice constructs the audit-query handler slice. Must be called after
// initCursorCodec so that c.cursorCodec is set.
//
// Read source: c.queryStore when set (composition root may inject a
// ledger.MultiStore aggregating multiple chains — issue #1121 /
// ADR 202605270230); falls back to c.ledgerStore for single-chain deployments
// (Store satisfies QueryStore by structural typing).
func (c *AuditCore) initQuerySlice(mode outbox.DurabilityMode) error {
	queryStore := c.queryStore
	if queryStore == nil {
		queryStore = c.ledgerStore
	}
	querySvc, err := auditquery.NewService(queryStore, c.cursorCodec, c.logger, c.txRunner,
		query.RunModeForDemo(mode == outbox.DurabilityDemo))
	if err != nil {
		return fmt.Errorf("audit-query: %w", err)
	}
	c.queryHandler = auditquery.NewHandler(querySvc)
	c.AddSlice(cell.MustNewBaseSliceFromMeta(auditquery.SliceMetadata()))
	return nil
}

// initCursorCodec initializes the cursor codec with a demo key if not
// injected. In DurabilityDurable mode the demo fallback is refused — callers
// must inject a production codec via WithCursorCodec.
func (c *AuditCore) initCursorCodec(mode outbox.DurabilityMode) error {
	if c.cursorCodec != nil {
		return nil
	}
	if mode == outbox.DurabilityDurable {
		return errcode.New(errcode.KindInternal, errcode.ErrCellMissingCodec,
			"auditcore durable mode requires a cursor codec; "+
				"use WithCursorCodec(query.NewCursorCodec(secret)) — "+
				"the built-in demo key is public in the source tree")
	}
	// Each cell uses a distinct demo key to prevent cross-cell cursor reuse in demo mode.
	codec, err := query.NewCursorCodec([]byte("gocell-demo-AUDIT--CORE-key-32!!"))
	if err != nil {
		return err
	}
	c.cursorCodec = codec
	c.logger.Warn("auditcore: using default cursor codec (demo mode)",
		slog.String("cell", c.ID()))
	return nil
}
