// Package devicecell implements the devicecell Cell for the iotdevice example.
// It demonstrates the L4 DeviceLatent consistency model: commands are enqueued
// by the server and polled by devices on their own schedule.
package devicecell

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/devicecmd"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	dto "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/dto"
	devicebootstrap "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/slices/devicebootstrap"
	devicecertcompletion "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/slices/devicecertcompletion"
	devicecertrenewal "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/slices/devicecertrenewal"
	devicecommand "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/slices/devicecommand"
	devicecommandinternal "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/slices/devicecommandinternal"
	devicecommandrpc "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/slices/devicecommandrpc"
	devicelist "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/slices/devicelist"
	deviceregister "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/slices/deviceregister"
	devicestatus "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/slices/devicestatus"
	cmdenqueue "github.com/ghbvf/gocell/generated/contracts/command/devicecommand/enqueue/v1"
	listcontract "github.com/ghbvf/gocell/generated/contracts/http/device/list/v1"
	registercontract "github.com/ghbvf/gocell/generated/contracts/http/device/register/v1"
	statuscontract "github.com/ghbvf/gocell/generated/contracts/http/device/status/v1"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	kcommand "github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/kernel/reconcile"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	commandruntime "github.com/ghbvf/gocell/runtime/command"
)

// Role constants re-exported from internal/dto for use by the assembly root
// (main.go). The internal package is not importable from outside the
// examples/iotdevice/cells/devicecell subtree per Go's internal package rule.
const (
	RoleAdmin    = dto.RoleAdmin
	RoleOperator = dto.RoleOperator
	RoleDevice   = dto.RoleDevice
)

// Compile-time interface check lives in cell_gen.go (DO NOT EDIT).

type commandQueueStore interface {
	kcommand.Queue
	kcommand.ActiveScanner
}

// Option configures a DeviceCell.
type Option func(*DeviceCell)

// WithDeviceRepository sets the device repository.
func WithDeviceRepository(r domain.DeviceRepository) Option {
	return func(c *DeviceCell) { c.deviceRepo = r }
}

// WithDirectPublisher wires the sealed outbox CellPublisher for event publishing.
// devicecell is L4 DeviceLatent — the direct-publish path is the source
// of truth. There is no transactional outbox writer at L4.
//
// Accumulative: a nil pub leaves the previously-set value in place.
// Demo mode: from the composition root, pass
// outbox.WrapPublisherForCell(&outbox.DiscardPublisher{}) to swallow events.
//
// ref: docs/architecture/202605101900-adr-cell-raw-infra-sealed-marker.md §D1
func WithDirectPublisher(pub outbox.CellPublisher) Option {
	return func(c *DeviceCell) {
		if pub != nil {
			c.publisher = pub
		}
	}
}

// WithCursorCodec sets the cursor codec for pagination.
func WithCursorCodec(c *query.CursorCodec) Option {
	return func(dc *DeviceCell) { dc.cursorCodec = c }
}

// WithCommandRegistry wires the process command.Registry into which the cell
// registers its synchronous command-bus handlers (today:
// command.devicecommand.enqueue.v1, via cmdenqueue.Register in initSlices).
//
// REQUIRED, not optional: devicecell declares command handle contracts, so an
// assembly that omits this fails fast in Init (initSlices) rather than silently
// leaving the generated command funnel unregistered — the dead-but-compiles
// state #1580 fixes. Same "no soft fallback" rationale as WithDeviceRepository /
// RegisterCommandQueue. The composition root constructs it via
// command.NewRegistry().
//
// One-shot wiring option (like WithDeviceRepository), not accumulative: a nil
// registry is stored as-is and rejected by the initSlices fail-fast guard — it
// does not preserve a previously-set value. Intentional; the dependency is required.
func WithCommandRegistry(reg *commandruntime.Registry) Option {
	return func(c *DeviceCell) { c.commandRegistry = reg }
}

// WithBootstrapEmitter wires the writer-backed sealed CellEmitter the
// devicebootstrap reactive slice uses to emit command.devicecommand.enqueue.v1
// async command entries into the outbox store (where the relay polls them),
// instead of the cell's direct-publish emitter (which fans out to the broker/eb
// for device-registered events). Batch-3 (#1698): the command-relay subsystem
// requires the emitted command entry land in the same store the relay polls, so
// the composition root constructs a WriterEmitter over the mode's outbox Writer
// (demo: outboxtest.FakeStore; durable: adapterpg.OutboxWriter) and injects it
// here.
//
// This is the SECOND emitter on the cell: the device-registered direct publisher
// (WithDirectPublisher) is unchanged and keeps fanning out events to the bus.
// The two are deliberately separate sinks.
//
// One-shot wiring option: a nil emitter is stored as-is and rejected by the
// initSlices fail-fast guard; it does not preserve a previously-set value. The
// dependency is required once the command-relay subsystem is wired.
func WithBootstrapEmitter(e outbox.CellEmitter) Option {
	return func(c *DeviceCell) { c.bootstrapEmitter = e }
}

// WithBootstrapTxManager sets the CellTxManager injected into the devicebootstrap
// reactive slice. That slice wraps command.EmitAsync in txRunner.RunInTx so
// durable mode (PG outbox writer) gets a real transaction in ctx. The cell
// defaults the field to outbox.DemoCellTxManager() (no-op) in NewDeviceCell, so
// demo mode and tests work without wiring it.
//
// The cert-renewal reconcile loop does NOT use this txManager: its emitter is
// self-durable (the outbox writer commits atomically in its own internal logic),
// and buildCertRenewalSweeper does not pass bootstrapTxManager to NewReconciler.
//
// Accumulative: a nil tx leaves the previously-set (default) value in place. NOT
// required (no fail-fast guard): DemoCellTxManager is the safe default for
// assemblies that do not wire a real PG pool.
func WithBootstrapTxManager(tx persistence.CellTxManager) Option {
	return func(c *DeviceCell) {
		if tx != nil {
			c.bootstrapTxManager = tx
		}
	}
}

// WithLogger sets the structured logger.
func WithLogger(l *slog.Logger) Option {
	return func(c *DeviceCell) { c.logger = l }
}

// WithMetricsProvider sets the metrics.Provider used by the outbox emitter to
// record fail-open dropped counters. Defaults to metrics.NopProvider{} when not
// set (appropriate for demo/example deployments).
func WithMetricsProvider(mp metrics.Provider) Option {
	return func(c *DeviceCell) { c.metricsProvider = mp }
}

// DeviceCell is the devicecell Cell implementation.
// +cell:listener:ref=cell.PrimaryListener,prefix=
// +cell:listener:ref=cell.InternalListener,prefix=
type DeviceCell struct {
	*cell.BaseCell
	deviceRepo         domain.DeviceRepository
	publisher          outbox.CellPublisher
	emitter            outbox.CellEmitter        // set during initInternal; retained for Probes
	bootstrapEmitter   outbox.CellEmitter        // writer-backed; feeds devicebootstrap reactive command emit (#1698)
	bootstrapTxManager persistence.CellTxManager // wraps EmitAsync in tx for durable PG writer; defaults to DemoCellTxManager
	cursorCodec        *query.CursorCodec
	logger             *slog.Logger
	metricsProvider    metrics.Provider
	commandQueue       commandQueueStore
	// commandQueueTypeMismatch records that RegisterCommandQueue was called with a
	// queue that implements kcommand.Queue but NOT ActiveScanner. The QueueRegistrar
	// interface fixes the wide kcommand.Queue parameter, so the ActiveScanner
	// requirement cannot be a compile-time constraint here; this flag lets Init
	// fail fast with a precise message instead of a misleading nil-queue error (#1694 F11).
	commandQueueTypeMismatch bool
	commandRegistry          *commandruntime.Registry // required; sync command-bus handler registry (#1580)
	commandSweeper           *reconcile.Loop          // device-command expiry sweep, driven on a TickerTrigger cadence
	certRenewalSweeper       *reconcile.Loop          // cert-renewal producer (archetype ② reconcile→command, #1757); scans the device repo
	reconcileMetrics         reconcile.Metrics        // shared by both reconcile.Loops; registered once via reconcileLoopMetrics
	reconcileMetricsOK       bool                     // true once reconcileMetrics is registered (provider was wired)
	clk                      clock.Clock              // injected from reg.Config during initInternal

	// +slice:route:slice=deviceregister,subPath=/api/v1/devices
	registerHandler *registercontract.Handler

	// bootstrapSvc backs the devicebootstrap subscribe slice. It carries no route
	// marker: the event.device-registered.v1 subscription is derived by cellgen
	// from slice.yaml contractUsages[role=subscribe], which resolves this field by
	// "pointer-type package == sliceID" (devicebootstrap) and emits the
	// NewSubscription(...).Mount(reg) call into cell_gen.go.
	bootstrapSvc *devicebootstrap.Service

	// certCompletionSvc backs the devicecertcompletion slice: it both publishes
	// event.devicecert-rotation-resolved.v1 (via the OnCommandResolved hook wired
	// into the public devicecmd Service's ack path) and subscribes to it (the
	// cert-state writer, #1870). Like bootstrapSvc it carries no route marker; the
	// subscription is derived by cellgen from slice.yaml contractUsages[role=
	// subscribe], resolved by "pointer-type package == sliceID"
	// (devicecertcompletion) into a cell_gen.go NewSubscription(...).Mount(reg) call.
	certCompletionSvc *devicecertcompletion.Service

	// +slice:route:slice=devicecommand,subPath=/api/v1/devices
	commandHandler *devicecommand.Handler
	// +slice:route:slice=devicecommandinternal,listener=cell.InternalListener,subPath=
	commandInternalHandler *devicecommandinternal.Handler

	// +slice:route:slice=devicestatus,subPath=/api/v1/devices
	statusHandler *statuscontract.Handler

	// +slice:route:slice=devicelist,subPath=/api/v1/devices
	listHandler *listcontract.Handler

	// commandRPCServer serves the grpc.device.command.v1 contract (first
	// end-to-end grpc handler, #1151). It carries no +slice:route marker — grpc
	// serve is derived by cellgen from slice.yaml contractUsages[role=serve]
	// (#1601), which resolves this field by "pointer-type package == sliceID"
	// (devicecommandrpc) and emits the reg.GRPCService(...) call into cell_gen.go.
	commandRPCServer *devicecommandrpc.Server
}

// RegisterCommandQueue implements kernel/command.QueueRegistrar. The supplied
// queue must also implement ActiveScanner so the same runtime component can
// serve the device dequeue path, sweeper, and internal ops view.
//
// A queue that is not an ActiveScanner is a wiring mistake, not a degraded
// runtime mode: it is recorded here and rejected at Init with a precise message
// (#1694 F11). The interface signature is fixed to the wide kcommand.Queue, so
// this cannot be a compile-time constraint — Init fail-fast is the boundary's
// limit. Earlier this only Warn-and-ignored, which then surfaced as a confusing
// "requires a command queue" nil error even though a queue WAS registered.
//
// Last registration wins (not accumulative): a later call overwrites the queue
// and clears any prior type-mismatch flag. The composition root calls this once;
// the last-wins semantics just keep a corrected re-wire from being trumped by an
// earlier bad one.
func (c *DeviceCell) RegisterCommandQueue(q kcommand.Queue) {
	store, ok := q.(commandQueueStore)
	if !ok {
		c.commandQueueTypeMismatch = true
		return
	}
	c.commandQueueTypeMismatch = false
	c.commandQueue = store
}

// requireCommandQueue validates the registered command queue. A queue that is
// not an ActiveScanner is a precise wiring error (#1694 F11) — the device
// dequeue path, sweeper, and internal ops view all need ScanActive — and a nil
// queue (none registered) is the "no soft fallback" error. Extracted from
// initSlices so that function stays within its cyclomatic-complexity budget.
func (c *DeviceCell) requireCommandQueue() error {
	if c.commandQueueTypeMismatch {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"devicecell: the registered command queue does not implement ActiveScanner "+
				"(required for the device dequeue path, sweeper, and internal ops view); "+
				"use commandtest.NewInMemQueue() for demo mode or postgres.NewCommandQueue(...) for durable mode")
	}
	if c.commandQueue == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"devicecell requires a command queue; from the composition root, "+
				"call RegisterCommandQueue(commandtest.NewInMemQueue()) for demo mode or "+
				"RegisterCommandQueue(postgres.NewCommandQueue(...)) for durable mode")
	}
	return nil
}

// NewDeviceCell creates a new DeviceCell with the given options.
func NewDeviceCell(clk clock.Clock, opts ...Option) *DeviceCell {
	clock.MustHaveClock(clk, "devicecell.New")
	c := &DeviceCell{
		BaseCell: cell.MustNewBaseCell(loadCellMetadata()),
		clk:      clk,
		logger:   slog.Default(),
		// Demo default mirroring devicebootstrap's own txRunner default: the
		// cert-renewal reconciler requires a non-nil CellTxManager. Durable mode
		// overrides it via WithBootstrapTxManager (accumulative, nil-ignored).
		bootstrapTxManager: outbox.DemoCellTxManager(),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// buildCellEmitter constructs a sealed CellEmitter via NewDirectCellEmitter.
// devicecell is L4 DeviceLatent and uses a publisher-only (DirectEmitter) path
// in all modes — there is no outbox writer or txRunner (KG-07 decision). It
// deliberately does NOT route through ResolveCellEmitter: for L4 the absence of
// an outbox writer/txRunner is the intended production mode, so the
// ResolveCellEmitter cellvocab.L2 non-durable "demo mode" Warn would mislabel
// the designed path as a degradation. The durable-publisher guard
// (CheckNotNoop) is enforced by the caller (initDeps) before this function is
// reached.
//
// DirectPublishFailOpen is intentional — command persistence succeeds
// independently of event publish; missed events are operational follow-up, not
// request failures. ref: ADR 202605101800 §D6 + KG-07 decision.
func (c *DeviceCell) buildCellEmitter() (outbox.CellEmitter, error) {
	mp := c.metricsProvider
	if mp == nil {
		mp = metrics.NopProvider{}
	}
	return outbox.NewDirectCellEmitter(
		c.publisher, outbox.DirectPublishFailOpen, mp, c.clk, "devicecell", outbox.WithLogger(c.logger),
	)
}

// initInternal is the K#04 codegen escape hatch: business init that cannot
// be generated (emitter resolve, slice service construction, lifecycle hooks).
// cell_gen.go::Init calls it after BaseCell.Init and before mounting the
// generated route-group blocks. This is a permanent convention, not a
// transitional shim — slice/handler instantiation and adapter wiring stay
// hand-written.
//
// L4 Cells do not use outboxWriter (KG-07 decision). The Cell boundary
// adapts the publisher to a direct emitter for event publishing.
//
//nolint:unparam // ctx is part of the K#04 initInternal contract; unused here, used by other cells (configcore)
func (c *DeviceCell) initInternal(ctx context.Context, reg cell.Registrar) error {
	durabilityMode := reg.DurabilityMode()

	if err := c.initDeps(durabilityMode); err != nil {
		return err
	}
	if err := c.initSlices(durabilityMode); err != nil {
		return err
	}

	// Route groups removed: cell_gen.go owns Init and renders them.
	return c.registerHealthAndLifecycle(reg)
}

// initDeps validates and resolves publisher, emitter, and cursor codec.
func (c *DeviceCell) initDeps(durabilityMode outbox.DurabilityMode) error {
	// DeviceRepository is required in every mode. Demo callers MUST wire
	// mem.NewDeviceRepository() explicitly via WithDeviceRepository — the
	// cell never falls back silently. This matches "no soft fallback":
	// CLAUDE.md §"不做的事" (in-memory↔PG dual-run fallback forbidden) +
	// .claude/rules/gocell/cell-patterns.md §"Init() fail-fast".
	if c.deviceRepo == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"devicecell requires a device repository; from the composition root, "+
				"call WithDeviceRepository(mem.NewDeviceRepository()) for demo mode or "+
				"WithDeviceRepository(postgres.NewDeviceRepository(pool.DB(), txMgr, clk)) for durable mode")
	}

	// Publisher is required (NIL-PUB-P1). For demo mode, the composition
	// root must wrap a publisher via outbox.WrapPublisherForCell, e.g.
	//   WithDirectPublisher(outbox.WrapPublisherForCell(&outbox.DiscardPublisher{}))
	if c.publisher == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrCellMissingOutbox,
			"devicecell requires publisher; use "+
				"WithDirectPublisher(outbox.WrapPublisherForCell(&outbox.DiscardPublisher{})) "+
				"from composition root for demo mode")
	}

	// Durable mode still rejects noop publishers, but direct publish remains
	// fail-open here because this example path has no transactional outbox.
	// The request succeeds once persistence succeeds; publish misses are
	// operational follow-up, not create failure.
	if err := outbox.CheckNotNoop(durabilityMode, "devicecell", c.publisher); err != nil {
		return err
	}
	builtEmitter, err := c.buildCellEmitter()
	if err != nil {
		return err
	}
	c.emitter = builtEmitter

	// Default cursor codec for pagination if not injected. Durable mode
	// refuses the public demo-key fallback — an assembly that forgets to
	// wire a production codec must fail closed, not silently sign cursors
	// with a key that ships in the source tree.
	// ref: zeromicro/go-zero MustSetUp — fatal on insecure default config.
	if c.cursorCodec == nil {
		if durabilityMode == outbox.DurabilityDurable {
			return errcode.New(errcode.KindInternal, errcode.ErrCellMissingCodec,
				"devicecell durable mode requires a cursor codec; "+
					"use WithCursorCodec(query.NewCursorCodec(secret)) — "+
					"the built-in demo key is public in the source tree")
		}
		// Each cell uses a distinct demo key to prevent cross-cell cursor reuse in demo mode.
		codec, err := query.NewCursorCodec([]byte("gocell-demo-DEVICE-CELL-key-32!!"))
		if err != nil {
			return err
		}
		c.cursorCodec = codec
		c.logger.Warn("devicecell: using default cursor codec (demo mode)")
	}
	return nil
}

// initSlices constructs all 4 device slices and the command sweeper.
func (c *DeviceCell) initSlices(durabilityMode outbox.DurabilityMode) error {
	// device-register slice
	registerSvc, err := deviceregister.NewService(
		c.clk, c.deviceRepo, c.logger,
		deviceregister.WithEmitter(c.emitter),
	)
	if err != nil {
		return fmt.Errorf("device-register: %w", err)
	}
	c.registerHandler = registercontract.NewHandler(registerSvc)
	c.AddSlice(cell.MustNewBaseSliceFromMeta(deviceregister.SliceMetadata()))

	// device-bootstrap slice: event-reactive producer subscribing to
	// event.device-registered.v1 and emitting a command.devicecommand.enqueue.v1
	// async command for each new device. Batch-3 (#1698): the emitter is the
	// writer-backed CellEmitter (WithBootstrapEmitter) so the emitted command entry
	// lands in the outbox store the relay polls — NOT the cell's direct-publish
	// emitter (c.emitter), which fans device-registered events out to the bus.
	// Required (no soft fallback): an assembly that wires the command-relay
	// subsystem must inject this; absence is a dead-wiring bug, so fail fast.
	if c.bootstrapEmitter == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrCellMissingOutbox,
			"devicecell requires a bootstrap command emitter; from the composition root, "+
				"call WithBootstrapEmitter(outbox.WrapEmitterForCell(writerEmitter)) where "+
				"writerEmitter is an outbox.WriterEmitter over the mode's outbox Writer "+
				"(demo: outboxtest.FakeStore; durable: adapterpg.NewOutboxWriter(clk))")
	}
	bootstrapSvc, err := devicebootstrap.NewService(
		c.clk,
		devicebootstrap.WithEmitter(c.bootstrapEmitter),
		devicebootstrap.WithTxManager(c.bootstrapTxManager),
		devicebootstrap.WithLogger(c.logger),
	)
	if err != nil {
		return fmt.Errorf("device-bootstrap: %w", err)
	}
	c.bootstrapSvc = bootstrapSvc
	c.AddSlice(cell.MustNewBaseSliceFromMeta(devicebootstrap.SliceMetadata()))

	// device-command slice: a Queue + ActiveScanner is required in every mode.
	// Validation (nil queue + non-ActiveScanner queue) lives in requireCommandQueue
	// so this function stays within its cyclomatic budget.
	if err := c.requireCommandQueue(); err != nil {
		return err
	}
	// The sync command-bus registry is required: devicecell declares command
	// handle contracts (command.devicecommand.enqueue.v1), so the generated
	// funnel must be wired to a real handler. Fail fast rather than silently
	// leaving it unregistered (the dead-but-compiles state #1580 fixes) — same
	// "no soft fallback" rationale as the commandQueue guard above.
	if c.commandRegistry == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"devicecell requires a command registry; from the composition root, "+
				"call WithCommandRegistry(command.NewRegistry())")
	}
	cmdQueue := c.commandQueue
	runMode := query.RunModeForDemo(durabilityMode == outbox.DurabilityDemo)

	// device-cert-completion slice (#1870): closes the L4 cert-renewal loop. It
	// emits event.devicecert-rotation-resolved.v1 from the public ack path (via
	// the OnCommandResolved hook wired into pubSvc below) and consumes it to
	// advance the device cert state. Constructed BEFORE pubSvc because pubSvc must
	// receive its hook. It is a STRONG dependency of devicecell — without it a
	// successfully-rotated device would never leave the near-expiry candidate set
	// (the loop never closes) — so a construction failure fails Init fast (no
	// silent no-op). It publishes via c.emitter (the direct event emitter the
	// relay fans to the bus), the same sink deviceregister uses for events.
	certCompletionSvc, err := devicecertcompletion.NewService(
		c.clk, c.deviceRepo,
		devicecertcompletion.WithEmitter(c.emitter),
		devicecertcompletion.WithLogger(c.logger),
		devicecertcompletion.WithMetricsProvider(c.metricsProvider),
	)
	if err != nil {
		return fmt.Errorf("device-cert-completion: %w", err)
	}
	c.certCompletionSvc = certCompletionSvc
	c.AddSlice(cell.MustNewBaseSliceFromMeta(devicecertcompletion.SliceMetadata()))

	// Public slice service: sliceName "devicecommand" for observability labels.
	// It serves the device ack (回执) endpoint, so it carries the cert-completion
	// resolution hook: a terminal rotate-cert ack emits a rotation-resolved event.
	pubSvc, err := devicecmd.NewService(
		c.clk, cmdQueue, c.deviceRepo, c.cursorCodec, c.logger,
		runMode,
		devicecmd.WithSliceName("devicecommand"),
		devicecmd.WithOnCommandResolved(c.certCompletionSvc.OnCommandResolved),
		// #1610 cross-cell idempotency consumer: the async-commands HTTP endpoint
		// emits cmdenqueue through the same writer-backed emitter + tx manager the
		// reactive bootstrap slice uses, so the relay's Claimer wrap dedups the
		// dispatch by DeriveCommandKey across cells/pods.
		devicecmd.WithCommandEmitter(c.bootstrapEmitter),
		devicecmd.WithCommandTxManager(c.bootstrapTxManager),
	)
	if err != nil {
		return fmt.Errorf("device-command: %w", err)
	}
	// Internal slice service: sliceName "devicecommandinternal" for observability labels.
	intSvc, err := devicecmd.NewService(
		c.clk, cmdQueue, c.deviceRepo, c.cursorCodec, c.logger,
		runMode,
		devicecmd.WithSliceName("devicecommandinternal"),
	)
	if err != nil {
		return fmt.Errorf("device-command-internal: %w", err)
	}
	c.commandHandler = devicecommand.NewHandler(pubSvc)
	// device-command grpc slice: first end-to-end unary RPC (#1151). It reuses
	// the same devicecmd.Service.Enqueue domain path as the HTTP devicecommand
	// slice (its own Service instance for observability attribution) so a gRPC
	// IssueCommand actually enqueues an L4 command rather than only acking. The
	// cell_gen.go reg.GRPCService(...) call (derived from slice.yaml) registers
	// c.commandRPCServer with the gRPC listener.
	grpcSvc, err := devicecmd.NewService(
		c.clk, cmdQueue, c.deviceRepo, c.cursorCodec, c.logger,
		runMode,
		devicecmd.WithSliceName("devicecommandrpc"),
	)
	if err != nil {
		return fmt.Errorf("device-command-grpc: %w", err)
	}
	c.commandRPCServer = devicecommandrpc.NewServer(c.clk, grpcSvc)
	c.AddSlice(cell.MustNewBaseSliceFromMeta(devicecommandrpc.SliceMetadata()))
	// Register the sync command-bus enqueue handler into the process registry.
	// EnqueueCommandAdapter bridges the generated cmdenqueue.Handler to the same
	// devicecmd.Service.Enqueue logic the HTTP enqueue path uses; cmdenqueue.Register
	// is the sole sanctioned registration path (COMMAND-DISPATCH-REGISTER-CALLER-01).
	// This import is what makes the generated command funnel a live entry point
	// (#1580) rather than dead-but-compiles.
	if err := cmdenqueue.Register(c.commandRegistry, devicecommand.EnqueueCommandAdapter{S: pubSvc}); err != nil {
		return fmt.Errorf("device-command register (id=%s): %w", cmdenqueue.DispatchID, err)
	}
	// internallist: /internal/v1/ path; Clients=["devicecell"] auto-injects RequireCallerCell via auth.Mount.
	c.commandInternalHandler = devicecommandinternal.NewHandler(intSvc)
	if err := c.buildCommandSweeper(cmdQueue); err != nil {
		return err
	}
	if err := c.buildCertRenewalSweeper(); err != nil {
		return err
	}
	// devicecertrenewal slice: declaration-only owner of the cell's second
	// command.devicecommand.enqueue.v1 producer (the cert-renewal reconciler). It
	// has no routes/subscribers/grpc, so cell_gen.go does not reference it; the
	// reconcile.Loop is lifecycle-registered in registerHealthAndLifecycle.
	c.AddSlice(cell.MustNewBaseSliceFromMeta(devicecertrenewal.SliceMetadata()))
	c.AddSlice(cell.MustNewBaseSliceFromMeta(devicecommand.SliceMetadata()))
	c.AddSlice(cell.MustNewBaseSliceFromMeta(devicecommandinternal.SliceMetadata()))

	// device-status slice
	statusSvc, err := devicestatus.NewService(c.deviceRepo, c.logger)
	if err != nil {
		return fmt.Errorf("device-status: %w", err)
	}
	// status: admin and operator may read any device's status; a device may only
	// read its own status (path {id} must match the token subject).
	c.statusHandler = statuscontract.NewHandler(statusSvc, auth.SelfOr("id", dto.RoleAdmin, dto.RoleOperator))
	c.AddSlice(cell.MustNewBaseSliceFromMeta(devicestatus.SliceMetadata()))

	// device-list slice
	listSvc, err := devicelist.NewService(c.deviceRepo, c.cursorCodec, c.logger,
		query.RunModeForDemo(durabilityMode == outbox.DurabilityDemo))
	if err != nil {
		return fmt.Errorf("device-list: %w", err)
	}
	c.listHandler = listcontract.NewHandler(listSvc, auth.AnyRole("admin"))
	c.AddSlice(cell.MustNewBaseSliceFromMeta(devicelist.SliceMetadata()))
	return nil
}

// commandSweepInterval is the TickerTrigger cadence — the SINGLE periodic
// source driving the device-command expiry sweep. The Loop opts out of the
// default-tick self-requeue (WithoutDefaultRequeue below), so a successful sweep
// does NOT re-enqueue itself; the 30s ticker pulse is the only re-observation
// driver. (Keeping the default-tick requeue on would add a second, independent
// periodic source — the ticker pulse arrives via the Loop's work queue while the
// self-requeue lands in the delaying-queue heap; they do not coalesce, so the
// sweep would run ~twice per cycle.)
const commandSweepInterval = 30 * time.Second

// buildCommandSweeper constructs the device-command expiry reconcile.Loop. The
// kernel Sweeper implements reconcile.Reconciler; a TickerTrigger off the cell's
// business clock (c.clk) drives a resync-all pulse every commandSweepInterval,
// and the Loop's sealed real-only control-plane clock owns the startup probe /
// requeue timers. Business-plane "now" (expiry) comes from c.clk via the
// Sweeper, so deadlines stay consistent with command-creation time.
//
// WithoutDefaultRequeue makes the TickerTrigger the SOLE periodic source: a
// successful Reconcile returns the zero Result{} and the Loop does not
// self-requeue it (see commandSweepInterval). Transient sweep errors still
// back off and retry; only the redundant success default-tick is suppressed.
//
// Sweep outcomes are observable via the reconcile_total{reconciler,result}
// family (result=transient for scan/Ack failures) when a metrics provider is
// wired; this supersedes the old single sweep-error counter.
func (c *DeviceCell) buildCommandSweeper(cmdQueue commandQueueStore) error {
	sweeper, err := kcommand.NewSweeper(cmdQueue, cmdQueue, c.clk)
	if err != nil {
		return fmt.Errorf("device-command sweeper: %w", err)
	}
	b := reconcile.New(sweeper).
		WithTrigger(reconcile.TickerTrigger(c.clk, commandSweepInterval)).
		WithName("devicecommand.sweeper").
		WithReconcilerID("devicecommand_sweeper"). // label-safe: [a-z0-9_], no dots
		WithoutDefaultRequeue()                    // ticker is the sole periodic source
	m, ok, err := c.reconcileLoopMetrics()
	if err != nil {
		return fmt.Errorf("device-command reconcile metrics: %w", err)
	}
	if ok {
		b = b.WithMetrics(m)
	}
	loop, err := b.Build()
	if err != nil {
		return fmt.Errorf("device-command reconcile loop: %w", err)
	}
	c.commandSweeper = loop
	return nil
}

// reconcileLoopMetrics registers the shared reconcile metric family once and
// caches it, so the cell's multiple reconcile.Loops (command sweeper + cert
// renewal) share ONE registration. Re-calling reconcile.RegisterMetrics per loop
// would re-register the same collectors and log a Warn per family on every reuse.
// Returns ok=false when no metrics provider is wired (loops then run unmetered).
func (c *DeviceCell) reconcileLoopMetrics() (reconcile.Metrics, bool, error) {
	if c.metricsProvider == nil {
		return reconcile.Metrics{}, false, nil
	}
	if !c.reconcileMetricsOK {
		m, err := reconcile.RegisterMetrics(c.metricsProvider)
		if err != nil {
			return reconcile.Metrics{}, false, err
		}
		c.reconcileMetrics = m
		c.reconcileMetricsOK = true
	}
	return c.reconcileMetrics, true, nil
}

const (
	// certRenewalSweepInterval is the TickerTrigger cadence for the cert-renewal
	// reconcile loop. Certificate expiry is slow-moving (days), so a 12h
	// re-observation is ample; like commandSweepInterval it is the SOLE periodic
	// source (WithoutDefaultRequeue below). Stateless producer: the sweep cadence
	// only drives observation frequency, not correctness — the queue active-
	// uniqueness owns dedup and the Sweeper owns expiry.
	certRenewalSweepInterval = 12 * time.Hour
	// certRenewalThreshold is the near-expiry window: a device whose certificate
	// expires within now+threshold is swept into a rotate-cert command.
	//
	// Co-tuning (correctness, not just policy): domain.CertValidity MUST exceed
	// this threshold. A freshly issued OR freshly rotated (#1870) cert lands at
	// now+CertValidity; if that were <= now+threshold it would be near-expiry
	// immediately and the completion write would never remove the device from the
	// candidate set — the convergence loop would not close. buildCertRenewalSweeper
	// asserts this at startup (fail-fast); the two values live in separate packages
	// (cell vs domain) to avoid a slice→cell import cycle.
	certRenewalThreshold = 30 * 24 * time.Hour
	// certRenewalAttemptTTL is the retry-granularity timer: how long one
	// rotate-cert command attempt stays active (non-terminal) before the Sweeper
	// expires it. After expiry the active-uniqueness slot is released and the next
	// reconcile tick re-enqueues a fresh attempt.
	//
	// Chosen at 36h — comfortably exceeding a healthy device's daily poll cycle
	// (worst-case poll+execute round-trip). Stateless producer: this is the ONLY
	// co-tuning needed (#1820 ADR-1822); no RetryInterval/Claimer-TTL relationship.
	certRenewalAttemptTTL = 36 * time.Hour
)

// certValidityExceedsThreshold reports whether the issued-cert validity exceeds
// the near-expiry renewal threshold — the convergence-loop correctness
// precondition (#1870). Extracted as a pure function so the invariant is
// unit-testable independently of the live consts (which must satisfy it); a
// const change that violates it turns the unit regression red.
func certValidityExceedsThreshold(validity, threshold time.Duration) bool {
	return validity > threshold
}

// buildCertRenewalSweeper constructs the certificate-renewal reconcile.Loop —
// the iotdevice archetype-② reference (reconcile → async command, #1757). It
// mirrors buildCommandSweeper: a TickerTrigger off the cell's business clock
// drives a resync-all pulse; on each pulse the devicecertrenewal.Reconciler scans
// the device repository for near-expiry certs (durable cert state on the devices
// row, #1819) and enqueues a deduplicated rotate-cert command per device, reusing
// the bootstrap command emitter + tx manager (the same writer-backed sinks the
// relay polls) and the already-active async-dispatch path (#1698). Reconciler
// construction validates its required deps (deviceRepo, bootstrapEmitter,
// bootstrapTxManager), all guaranteed non-nil by the initDeps / initSlices
// fail-fast guards reached before here.
func (c *DeviceCell) buildCertRenewalSweeper() error {
	// Convergence-loop correctness guard (#1870): a freshly rotated cert is
	// re-issued at resolvedAt+domain.CertValidity. If CertValidity did not exceed
	// the near-expiry threshold, that cert would still be a renewal candidate and
	// the completion write could never close the loop. Fail fast at startup rather
	// than silently busy-looping rotate-cert commands forever. This is the
	// machine-checked form of the cross-package co-tuning the const comments note.
	if !certValidityExceedsThreshold(domain.CertValidity, certRenewalThreshold) {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"devicecell: domain.CertValidity must exceed certRenewalThreshold; "+
				"otherwise a freshly rotated cert stays near-expiry and the cert-renewal "+
				"convergence loop never closes (#1870)")
	}
	reconciler, err := devicecertrenewal.NewReconciler(
		c.clk, c.deviceRepo, c.bootstrapEmitter,
		devicecertrenewal.Policy{
			Threshold:  certRenewalThreshold,
			AttemptTTL: certRenewalAttemptTTL,
		},
		c.logger,
	)
	if err != nil {
		return fmt.Errorf("device-cert renewal reconciler: %w", err)
	}
	b := reconcile.New(reconciler).
		WithTrigger(reconcile.TickerTrigger(c.clk, certRenewalSweepInterval)).
		WithName("devicecert.renewal").
		WithReconcilerID("devicecert_renewal"). // label-safe: [a-z0-9_], no dots
		// ticker is the sole periodic re-observation source; Reconcile always
		// returns Result{} (zero RequeueAfter) so no self-requeue occurs.
		WithoutDefaultRequeue()
	m, ok, err := c.reconcileLoopMetrics()
	if err != nil {
		return fmt.Errorf("device-cert reconcile metrics: %w", err)
	}
	if ok {
		b = b.WithMetrics(m)
	}
	loop, err := b.Build()
	if err != nil {
		return fmt.Errorf("device-cert reconcile loop: %w", err)
	}
	c.certRenewalSweeper = loop
	return nil
}

// registerHealthAndLifecycle registers health probes and the sweeper lifecycle hook.
func (c *DeviceCell) registerHealthAndLifecycle(reg cell.Registrar) error {
	if err := cell.RegisterEmitterHealthProbes(reg, c.emitter); err != nil {
		return err
	}
	// Cell-level repo readiness probes (observability.md §"Cell 级别 Repo Readiness Probe").
	// M1 cellgen emits exactly one RegisterReadiness helper per cell (the cell's
	// primary repo). Auxiliary repo probes (e.g. command_queue_ready) await M2
	// cellgen multi-probe support — tracked as backlog CELLGEN-MULTI-PROBE-01
	// (cap-13 §13.1, M1-OBSERVED follow-up).
	if prober, ok := c.deviceRepo.(healthz.RepoProber); ok {
		if err := RegisterReadiness(reg, prober); err != nil {
			return err
		}
	}
	// reconcile.Loop.Start/Stop have the exact cell.LifecycleHook OnStart/OnStop
	// shape (func(context.Context) error): Start spawns the worker pool + a fast
	// startup probe and returns; Stop drains. No adapter needed.
	reg.Lifecycle(cell.LifecycleHook{
		Name:    "devicecommand.sweeper",
		OnStart: c.commandSweeper.Start,
		OnStop:  c.commandSweeper.Stop,
	})
	reg.Lifecycle(cell.LifecycleHook{
		Name:    "devicecert.renewal",
		OnStart: c.certRenewalSweeper.Start,
		OnStop:  c.certRenewalSweeper.Stop,
	})
	return nil
}
