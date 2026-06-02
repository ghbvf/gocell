package cell

// ADR: kernel/cell depends on net/http (standard library)
//
// Status: Accepted (carried forward from registrar.go)
//
// Decision: kernel/cell uses net/http types (http.Handler, http.ResponseWriter,
// http.Request) in the RouteMux interface.
//
// Rationale: net/http is part of the Go standard library. The project's
// layering rules (CLAUDE.md) state "kernel/ only depends on stdlib + pkg/",
// so net/http is an allowed dependency. The Go 1.22+ enhanced ServeMux
// pattern syntax ("METHOD /path/{param}") gives kernel a powerful routing
// abstraction without importing any third-party router.

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/contractspec"
	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/webhook"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
	"github.com/ghbvf/gocell/pkg/validation"
)

// ---------------------------------------------------------------------------
// Registrar interface — the single registration surface for a Cell.
//
// A Cell calls methods on Registrar inside its Init implementation to declare
// all capabilities: routes, subscriptions, health probes, lifecycle hooks,
// and config-reload callbacks. The concrete implementation (RegistryRecorder)
// accumulates declarations and returns them as a RegistrySnapshot once
// Snapshot() is called by the bootstrap layer.
//
// ref: go-kratos/kratos registry/registry.go@main — Registrar (verb) ≠ Registry
//      (storage noun); registration-action interfaces take the agent-noun form.
// ref: uber-go/fx lifecycle.go@master:L33-L116 — Lifecycle.Append builder
// ref: kubernetes-sigs/controller-runtime pkg/manager/manager.go@main:L70-L78 — AddHealthzCheck independent method
// ref: go-kratos/kratos transport/http/server.go@main:L143-L224 — route spec accumulation
// ---------------------------------------------------------------------------

// Registrar is the single registration surface a Cell uses inside Init to
// declare all capabilities. Each method appends to the RegistryRecorder's
// internal state. Calling any registration method after Snapshot() panics
// to prevent lazy-registration bugs.
type Registrar interface {
	// Config returns the per-cell config snapshot provided by the assembly.
	Config() map[string]any
	// DurabilityMode returns the assembly-level durability mode.
	DurabilityMode() outbox.DurabilityMode

	// RouteGroup declares an HTTP route group. Groups accumulate in
	// declaration order and are mounted by bootstrap during phase5.
	RouteGroup(g RouteGroup)

	// Subscribe registers an event subscription. Returns a non-nil error when:
	//   - handler is nil
	//   - consumerGroup is empty
	//   - cellID is empty
	//   - spec.Kind != "event"
	//
	// cellID is the cell that owns this subscription — distinct from
	// consumerGroup (which may include a role suffix like
	// "accesscore-rbac-session-sync"). It is a positional, mandatory string
	// parameter rather than a SubscriptionOption: codegen (contractgen
	// NewSubscription + cellgen cell.tmpl) injects it from cell metadata at
	// compile time, so a missing cellID is a compile failure at the
	// reg.Subscribe call site (HARD contract). This is the AI-robust gate for
	// "metric/log owner must trace to cell metadata, not to consumerGroup
	// drift" — making cellID an option would silently demote the contract
	// from compile-time to opt-in.
	//
	// Typical usage (consumerGroup == cellID):
	//
	//	reg.Subscribe(spec, handler, c.ID(), c.ID())
	//
	// Role-suffix usage (fanout consumer with sub-group, cellID stays the cell):
	//
	//	reg.Subscribe(spec, handler, "accesscore-rbac-session-sync", c.ID())
	//
	// Cell.Init should propagate the error via `if err := ...; err != nil { return err }`.
	//
	// The handler type outbox.EntryHandler is the canonical event handler in
	// kernel/; cell.Registrar consumes it directly rather than wrapping it in a
	// cell-local alias. This mirrors the industry pattern where a registry/router
	// depends on its event primitive's handler signature:
	//   ref: ThreeDotsLabs/watermill message/router.go AddHandler — handler func type
	//        defined in message/, consumed directly by router.AddHandler.
	//   ref: k8s.io/client-go/tools/cache SharedInformer.AddEventHandler — cache.ResourceEventHandler
	//        defined in cache/, passed directly to informer.AddEventHandler.
	//
	// The subscription is not started here; Bootstrap drains all registered
	// SubscriptionRequests in phase6 and hands them to the event router.
	// This two-phase pattern (register intent in Init, start goroutines in
	// bootstrap) mirrors ThreeDotsLabs/watermill router.AddHandler — handler
	// registration and router.Run are separate lifecycle steps.
	//
	// ref: ThreeDotsLabs/watermill message/router.go AddHandler (handler
	// registration decoupled from goroutine start).
	// ref: ADR docs/architecture/202605111000-adr-subscription-cellid-mandatory.md
	Subscribe(
		spec contractspec.ContractSpec,
		handler outbox.EntryHandler,
		consumerGroup string,
		cellID string,
		opts ...SubscriptionOption,
	) error

	// RegisterWebhookReceiver records an inbound-webhook receiver declaration.
	// Returns a non-nil error when handler is nil or spec.Validate() fails.
	//
	// RECORD-ONLY semantics (PR-2): like Subscribe, this method only appends a
	// WebhookReceiverRequest to the recorder's accumulator — it does NOT mount
	// an HTTP route, resolve a signing secret, or start any goroutine. The
	// receiver runtime drain (bootstrap reading RegistrySnapshot.WebhookReceivers
	// and wiring the HTTP receive endpoint + Claimer) lands in PR-3. This keeps
	// the same two-phase pattern as Subscribe (declare intent in Init, wire in
	// bootstrap) so the generated reg.RegisterWebhookReceiver(...) call compiles
	// today against a stable seam.
	//
	// The call is emitted by cellgen from slice.yaml
	// contractUsages[role=webhook-receive]; the [webhook.ReceiverSpec] literal
	// carries identifiers known at code-generation time (ContractID / SourceID /
	// CellID, the last injected from cell metadata exactly like Subscribe's
	// positional cellID). The handler type is [webhook.WebhookReceiveHandler],
	// defined in kernel/webhook alongside ReceiverSpec and consumed directly here
	// (same pattern as Subscribe consuming outbox.EntryHandler).
	//
	// Cell.Init should propagate the error via `if err := ...; err != nil { return err }`.
	RegisterWebhookReceiver(spec webhook.ReceiverSpec, handler webhook.WebhookReceiveHandler) error

	// RegisterWebhookDispatch records an outbound-webhook dispatcher declaration.
	// Returns a non-nil error when selector is nil or spec.Validate() fails.
	//
	// RECORD-ONLY semantics (PR-2): the dispatch counterpart of
	// RegisterWebhookReceiver. It only appends a WebhookDispatchRequest to the
	// recorder's accumulator — no dispatcher consumer is started and no signing
	// secret is resolved. The dispatcher runtime drain (bootstrap reading
	// RegistrySnapshot.WebhookDispatchers and wiring the outbound dispatcher
	// consumer) lands in PR-5.
	//
	// The call is emitted by cellgen from slice.yaml
	// contractUsages[role=webhook-dispatch]; the [webhook.DispatchSpec] literal
	// carries ContractID / SourceID / CellID. The selector type is
	// [webhook.WebhookDispatchSelector], defined in kernel/webhook alongside
	// DispatchSpec.
	//
	// Cell.Init should propagate the error via `if err := ...; err != nil { return err }`.
	RegisterWebhookDispatch(spec webhook.DispatchSpec, selector webhook.WebhookDispatchSelector) error

	// RegisterProjection records an L3 CQRS-projection declaration. Returns a
	// non-nil error when req fails validation (nil Apply, empty ProjectionID /
	// CellID, or a non-event Spec).
	//
	// RECORD-ONLY semantics: like Subscribe and RegisterWebhookReceiver, this
	// method only appends a ProjectionRequest to the recorder's accumulator. It
	// does NOT construct a projection.Coordinator, open a transaction, or start
	// consuming. The bootstrap projection drain reads
	// RegistrySnapshot.Projections, constructs one projection.Coordinator per
	// request from framework-owned dependencies (checkpoint store / tx runner /
	// cursor / replay source — wired via bootstrap options, never reachable by
	// cell code), and calls Coordinator.Subscribe. This two-phase split (declare
	// intent in Init, wire in bootstrap) is the same pattern as Subscribe and is
	// what keeps the raw framework infrastructure out of the cell package: a cell
	// could not call projection.NewCoordinator even if it wanted to, because it
	// holds no checkpoint store / tx runner.
	//
	// The call WILL be emitted by cellgen from slice.yaml contractUsages when the
	// kind:projection derivation lands (PR-04b, #1367); until then RegisterProjection
	// is only called from test files (PROJECTION-REGISTER-FUNNEL-01 enforces this).
	// The exact slice.yaml role/field that drives the derivation is decided in
	// PR-04b, not here. ProjectionRequest carries identifiers known at
	// code-generation time (CellID injected from cell metadata exactly like
	// Subscribe's positional cellID). The Apply / OnReset function types are
	// cell-local ([ProjectionApply] / [ProjectionResetHook]) rather than
	// kernel/projection types: kernel/projection imports kernel/cell (its
	// Coordinator's SubscribeRegistrar references cell.SubscriptionOption), so a
	// reverse import here would be a compile-time cycle. The bootstrap drain
	// converts these to the identical projection.Apply / projection.OnReset
	// signatures (a legal named-type conversion). This mirrors
	// SubscriptionRequest.Handler carrying the kernel primitive outbox.EntryHandler
	// rather than a runtime/eventrouter type.
	//
	// Cell.Init should propagate the error via `if err := ...; err != nil { return err }`.
	//
	// AI-robust: callers of RegisterProjection are locked to cellgen-derived
	// cell_gen.go + _test.go + kernel/ by archtest PROJECTION-REGISTER-FUNNEL-01
	// (Medium upstream + Medium downstream — Go cannot type-gate callers of a
	// public method; this is the same permanent ceiling as Subscribe /
	// RegisterWebhookReceiver). The Hard-ification path (cellgen-only sealed-token
	// parameter) is tracked as a #1176 follow-up (#1372).
	//
	// ref: tools/archtest/projection_register_funnel_test.go (PROJECTION-REGISTER-FUNNEL-01)
	// ref: ADR docs/architecture/202605261620-adr-cqrs-projection-lifecycle-harness.md §7 + §Amendment 2026-05-31
	RegisterProjection(req ProjectionRequest) error

	// RegisterReadiness registers a readiness probe under the typed
	// [healthz.ProbeName]. The probe runs against the runtime
	// healthz.Aggregator after bootstrap drains the snapshot. First-wins
	// duplicate semantics: [healthz.ErrDuplicateProbe] is returned on name
	// collision (the second registration is dropped, not the first).
	//
	// name is the single source of truth for the probe identity — the
	// recorder wraps prober.Check() under name even if prober itself
	// originated from a [healthz.Probe] with a different Name(); this makes
	// name/check drift structurally impossible at the entry point.
	//
	// This is the SOLE public probe-registration surface on Registrar; the
	// underlying healthz.Aggregator is no longer exposed (the legacy
	// Registrar.Healthz() method has been retired). Hand-written cell code
	// routes through the sanctioned typed funnels that themselves terminate here:
	//   - cellgen-generated `<cellpkg>.RegisterReadiness(reg, prober)` for
	//     cell-repo probes (declared in healthz_gen.go).
	//   - shared kernel `cell.RegisterEmitterHealthProbes(reg, emitter)`
	//     for outbox emitter probes.
	//
	// archtest PROBENAME-SEALED-FUNNEL-01 locks both directions: A1
	// declares the sanctioned ProbeName const set; A2 closes the callsite
	// via type resolution on the ProbeName parameter.
	//
	// Example (cell-repo probe — standard path):
	//
	//  1. Declare repo in cell.yaml.
	//  2. Run `gocell generate cell -all` to emit healthz_gen.go.
	//  3. Call the generated helper from cell Init:
	//     if err := mycell.RegisterReadiness(reg, c.repo); err != nil { ... }
	RegisterReadiness(name healthz.ProbeName, prober healthz.Prober) error

	// Lifecycle appends a lifecycle hook. Name must be non-empty; passing an
	// empty Name panics (programming error). Hooks run in declaration order
	// on startup and in reverse order on shutdown.
	Lifecycle(h LifecycleHook)

	// OnConfigReload registers a config-reload callback. prefixes==nil means
	// the callback is invoked on every reload. When non-nil, the callback is
	// invoked only when at least one changed key matches a prefix. An empty
	// string inside prefixes is a programming error and panics.
	OnConfigReload(prefixes []string, fn func(context.Context, ConfigChangeEvent) error)
}

// ---------------------------------------------------------------------------
// Types migrated from registrar.go / routegroup.go
// ---------------------------------------------------------------------------

// RouteMux is a minimal route registration interface.
// kernel/ does not import any specific router (chi, gorilla, etc.);
// concrete implementations are provided by runtime/ or adapters/.
//
// For testing, use kernel/cell/celltest.TestMux.
type RouteMux interface {
	// Handle registers handler for the given pattern.
	// Pattern follows Go 1.22+ enhanced ServeMux syntax: "METHOD /path/{param}".
	Handle(pattern string, handler http.Handler)

	// Route creates a sub-router under pattern with prefix stripping.
	Route(pattern string, fn func(sub RouteMux))

	// Mount attaches an opaque http.Handler sub-tree under pattern with prefix
	// stripping. The mounted handler does not need to follow GoCell routing
	// conventions.
	Mount(pattern string, handler http.Handler)

	// Group creates a same-level scope sharing the parent prefix.
	Group(fn func(RouteMux))

	// With returns a new RouteMux that inherits all routes and middleware
	// from this scope, plus the additional middleware provided.
	//
	// ref: go-chi/chi Mux.With — returns an inline router sharing the parent tree.
	With(mw ...func(http.Handler) http.Handler) RouteMux
}

// RouteHandler is the minimum route-registration surface shared by both the
// production RouteMux and stdlib *http.ServeMux.
type RouteHandler interface {
	Handle(pattern string, handler http.Handler)
}

// Prefixer is optionally implemented by RouteHandler values whose chi
// sub-router owns a mount prefix. auth.Mount type-asserts to this interface
// to compute the chi-relative registration path.
type Prefixer interface {
	Prefix() string
}

// AuthRouteMeta carries the auth-related attributes a slice declares when
// registering a route.
type AuthRouteMeta struct {
	Method              string
	Path                string
	Public              bool
	PasswordResetExempt bool
	// Bootstrap marks the route as protected by HTTP Basic Auth (env credentials).
	// The listener-level JWT middleware skips Bootstrap routes, just like Public
	// routes — the per-route bootstrap middleware authenticates instead. Bootstrap,
	// Public, and PasswordResetExempt are mutually exclusive.
	Bootstrap bool
}

// IsInternal reports whether this route lives on the internal listener.
// Delegates to metadata.IsInternalHTTPPath so the bare "/internal/v1"
// root and the trailing-slash form share one predicate across governance
// (REF-17 / FMT-28 / FMT-31), runtime routing, and admission.
func (m AuthRouteMeta) IsInternal() bool {
	return metadata.IsInternalHTTPPath(m.Path)
}

// AuthRouteDeclarer is implemented by aggregators that want to receive the
// auth metadata a slice declares alongside a route.
type AuthRouteDeclarer interface {
	DeclareAuthMeta(meta AuthRouteMeta) error
}

// HTTPContractDeclarer is implemented by aggregators that want to receive the
// ContractSpec a slice declares alongside an HTTP route.
type HTTPContractDeclarer interface {
	DeclareHTTPContract(spec contractspec.ContractSpec) error
}

// RouteGroup declares where a batch of routes physically lives: which listener
// and what path prefix. The group inherits its listener's auth chain uniformly.
//
// ref: go-kratos/kratos transport/http/server.go@main:L143-L224 — route spec accumulation.
type RouteGroup struct {
	Listener   ListenerRef
	Prefix     string
	Middleware []func(http.Handler) http.Handler
	// Register is called by bootstrap to mount the cell's sub-tree on the
	// chosen mux. Required; a nil Register is a programmer error detected
	// at phase5 validation time.
	Register func(mux RouteMux) error
	// CellID is set automatically by bootstrap during phase5CollectRouteGroups.
	CellID string
}

// SingleGroup is a convenience constructor for the common single-listener,
// single-prefix case.
//
// DX-05: reduces boilerplate in cells that declare a single route group.
func SingleGroup(l ListenerRef, prefix string, fn func(RouteMux) error) RouteGroup {
	return RouteGroup{Listener: l, Prefix: prefix, Register: fn}
}

// SubscriptionRequest holds everything needed to register one event subscription.
// RegistryRecorder accumulates these; bootstrap drains them at phase6.
type SubscriptionRequest struct {
	Spec          contractspec.ContractSpec
	Handler       outbox.EntryHandler
	ConsumerGroup string
	SliceID       string

	// CellID is the cell that owns this subscription — observability owner,
	// distinct from ConsumerGroup (broker partition key + idempotency
	// namespace). The cell declares it explicitly during Init via the
	// positional cellID parameter on Registrar.Subscribe; codegen
	// (contractgen + cellgen) injects the value from cell metadata at
	// compile time. Bootstrap's drainCellSubscriptions cross-checks that
	// CellID equals the snapshot key (fail-fast on drift) and does NOT
	// silently populate it: a missing CellID is a compile failure at the
	// reg.Subscribe call site, not a runtime fallback.
	//
	// Example: accesscore registers subscriptions with
	//   ConsumerGroup = "accesscore-rbac-session-sync"
	//   CellID        = "accesscore"   (positional parameter, from cell metadata)
	// so that the resulting outbox.Subscription.CellID is "accesscore" — the
	// cell — without ever proxying ConsumerGroup as cell identity.
	//
	// ref: ThreeDotsLabs/watermill router.AddHandler handlerName / NATS subscription metadata.
	// ref: ADR docs/architecture/202605111000-adr-subscription-cellid-mandatory.md
	CellID string
}

// SubscriptionOption mutates a SubscriptionRequest to attach optional metadata.
type SubscriptionOption func(*SubscriptionRequest)

// WithSubscriptionSliceID declares the owning slice for subscription observability.
func WithSubscriptionSliceID(sliceID string) SubscriptionOption {
	return func(r *SubscriptionRequest) {
		r.SliceID = sliceID
	}
}

// WebhookReceiverRequest holds everything needed to register one inbound-webhook
// receiver. RegistryRecorder accumulates these via Registrar.RegisterWebhookReceiver;
// the bootstrap receiver runtime (PR-3) drains them. Mirrors SubscriptionRequest's
// exported-field shape.
type WebhookReceiverRequest struct {
	Spec    webhook.ReceiverSpec
	Handler webhook.WebhookReceiveHandler
}

// WebhookDispatchRequest holds everything needed to register one outbound-webhook
// dispatcher. RegistryRecorder accumulates these via Registrar.RegisterWebhookDispatch;
// the bootstrap dispatcher runtime (PR-5) drains them. Mirrors SubscriptionRequest's
// exported-field shape.
type WebhookDispatchRequest struct {
	Spec     webhook.DispatchSpec
	Selector webhook.WebhookDispatchSelector
}

// ProjectionApply is the cell-local mirror of the kernel/projection.Apply
// event→state hook signature. It is declared here (not imported from
// kernel/projection) because kernel/projection imports kernel/cell — its
// Coordinator's SubscribeRegistrar references cell.SubscriptionOption — so
// importing kernel/projection back into kernel/cell would be a compile-time
// cycle. The bootstrap projection drain
// performs the named-type conversion to projection.Apply (identical underlying
// signature), exactly as SubscriptionRequest.Handler carries the kernel
// primitive outbox.EntryHandler and is converted by the event-router drain.
type ProjectionApply func(ctx context.Context, event outbox.Entry) error

// ProjectionResetHook is the cell-local mirror of kernel/projection.OnReset,
// the optional rebuild Reset-phase hook. Same cycle-avoidance rationale as
// ProjectionApply; a nil value is valid (no read-model table to clear).
type ProjectionResetHook func(ctx context.Context) error

// ProjectionRequest holds everything needed to register one L3 CQRS projection.
// RegistryRecorder accumulates these via Registrar.RegisterProjection; the
// bootstrap projection drain reads RegistrySnapshot.Projections, constructs a
// projection.Coordinator per request from framework-owned dependencies, and
// calls Coordinator.Subscribe. Mirrors SubscriptionRequest's exported-field
// shape; the framework deps (checkpoint store / tx runner / cursor / replay)
// are deliberately NOT fields here — they live in bootstrap and never reach
// cell code.
type ProjectionRequest struct {
	// Spec is the event-kind contract the projection consumes (its input
	// stream). Spec.Kind must be "event".
	Spec contractspec.ContractSpec
	// ProjectionID names the projection within its cell. It is half of the
	// checkpoint key (cellID, projectionID); the consumer group is derived as
	// cellID + "-" + projectionID, and the rebuild HTTP endpoint (PR-04e) keys
	// the Coordinator by cellID + "/" + projectionID. Must be a snake_case
	// probe-name identifier (NewCoordinator rejects "/" etc. via the probe-name
	// validator), so neither derived key is ambiguous.
	ProjectionID string
	// CellID is the owning cell — observability owner and checkpoint-key half.
	// Injected from cell metadata at code-generation time (same provenance as
	// SubscriptionRequest.CellID); the bootstrap drain cross-checks it against
	// the snapshot owner and fails fast on drift.
	CellID string
	// SliceID is the slice that owns this projection — the observability owner
	// at slice granularity, one level below CellID. Semantically mirrors
	// SubscriptionRequest.SliceID (see that field's godoc). Typically equal to
	// ProjectionID; injected from slice metadata at code-generation time in
	// PR-04b. During this PR-04a record-only seam there is no production fill
	// path, so SliceID may be empty — bootstrap falls back to ProjectionID
	// when SliceID is the empty string.
	SliceID string
	// Apply is the business event→state hook. Required (non-nil).
	Apply ProjectionApply
	// OnReset is the optional rebuild Reset-phase hook. May be nil.
	OnReset ProjectionResetHook
}

// SubscriptionValidator validates a Subscription at registration time.
//
// ref: opentelemetry-collector otelcol/config.go Validate() — declarative validation at config load time.
type SubscriptionValidator func(outbox.Subscription) error

// SubscriptionValidatorAdder lets composition roots inject registration-time
// validators. Implementations of an event router MAY also implement this interface.
type SubscriptionValidatorAdder interface {
	AddSubscriptionValidator(SubscriptionValidator)
}

// LifecycleHook mirrors bootstrap.Hook shape but lives in kernel/ so Cell
// interfaces never depend on runtime/. Bootstrap copies these fields into
// its own bootstrap.Hook at phase3b discovery time.
//
// ref: github.com/uber-go/fx internal/lifecycle/lifecycle.go Hook — adopted.
type LifecycleHook struct {
	// Name is a diagnostic identifier used in slog fields. Must be non-empty
	// when passed to Registrar.Lifecycle.
	Name string

	// OnStart is called by bootstrap during lifecycle.Start. The ctx parameter
	// is the long-lived owner ctx (controller-runtime Runnable.Start semantics):
	// it remains live for the entire assembly run and is canceled only when the
	// assembly begins shutdown. This supersedes ADR 202605102000 §D1, which
	// described ctx as a startup-deadline ctx.
	//
	// Expected OnStart shape:
	//   1. Spawn the long-running worker goroutine passing the owner ctx.
	//   2. Run a fast synchronous readiness probe (e.g. 50 ms timer).
	//   3. Return nil on success or a non-nil error to abort startup and trigger
	//      LIFO rollback. The failed hook's OnStop is NOT called by rollback.
	//
	// Returning without spawning a goroutine is fine for hooks that do all their
	// work synchronously. Blocking indefinitely in OnStart is not allowed — the
	// hook must return promptly so the startup sequence can continue.
	//
	// StartTimeout is no longer enforced as an OnStart runner deadline; it is
	// retained as a hook self-declared probe window budget (informational only).
	// Supersedes ADR 202605102000 §D1.
	//
	// ref: kubernetes-sigs/controller-runtime pkg/manager/internal.go — Runnable.Start
	//      receives the manager context (long-lived owner ctx); returns when ctx done.
	OnStart func(ctx context.Context) error

	// OnStop is called by bootstrap during lifecycle.Stop, with a context that
	// carries StopTimeout as deadline. OnStop cancels the worker (if not already
	// canceled via owner ctx) and waits for it to exit.
	OnStop       func(ctx context.Context) error
	StartTimeout time.Duration
	StopTimeout  time.Duration
}

// ConfigChangeEvent describes what changed during a config reload.
//
// ref: micro/go-micro config/watcher.go — checksum-based change dedup.
type ConfigChangeEvent struct {
	Added      []string
	Updated    []string
	Removed    []string
	Config     map[string]any
	Generation int64
}

// ConfigReloadRequest holds a registered config-reload callback with its
// optional key-prefix filter. Prefixes==nil means "all keys".
type ConfigReloadRequest struct {
	Prefixes []string
	Fn       func(context.Context, ConfigChangeEvent) error
}

// ---------------------------------------------------------------------------
// RegistrySnapshot — read-only view produced by RegistryRecorder.Snapshot()
// ---------------------------------------------------------------------------

// RegistrySnapshot is the immutable result of a Cell's Init registration pass.
// Bootstrap reads these fields to wire routes, subscriptions, lifecycle hooks,
// config-reload callbacks, and readiness probes. Each field accumulates during
// Init via the Registrar interface (e.g. reg.RegisterReadiness for probes) and
// is drained by bootstrap into the runtime backbone after Init completes —
// see each field's godoc for the write-side-accumulate / read-side-drain
// pattern.
type RegistrySnapshot struct {
	RouteGroups     []RouteGroup
	Subscriptions   []SubscriptionRequest
	LifecycleHooks  []LifecycleHook
	ConfigReloaders []ConfigReloadRequest

	// Probes are the cell-level readiness probes declared during Init via
	// reg.RegisterReadiness(...). The bootstrap layer drains them onto the
	// runtime healthz.Aggregator after every cell has initialized — exactly
	// the same write-side-accumulate / read-side-drain split used for
	// RouteGroups, Subscriptions, and LifecycleHooks. The recorder does NOT
	// hold a live aggregator; it is a pure accumulator.
	Probes []healthz.Probe

	// WebhookReceivers are the inbound-webhook receivers declared during Init
	// via reg.RegisterWebhookReceiver(...). PR-2 only accumulates them; the
	// bootstrap receiver runtime drains this slice in PR-3 (no drain exists
	// yet) — same write-side-accumulate / read-side-drain split as Subscriptions.
	WebhookReceivers []WebhookReceiverRequest

	// WebhookDispatchers are the outbound-webhook dispatchers declared during
	// Init via reg.RegisterWebhookDispatch(...). PR-2 only accumulates them; the
	// bootstrap dispatcher runtime drains this slice in PR-5.
	WebhookDispatchers []WebhookDispatchRequest

	// Projections are the L3 CQRS projections declared during Init via
	// reg.RegisterProjection(...). The bootstrap projection drain constructs one
	// projection.Coordinator per request and calls Coordinator.Subscribe — same
	// write-side-accumulate / read-side-drain split as Subscriptions.
	Projections []ProjectionRequest
}

// ---------------------------------------------------------------------------
// RegistryRecorder — the concrete accumulator
// ---------------------------------------------------------------------------

// RegistryRecorder implements Registrar. It accumulates declarations during
// a Cell's Init call and returns them as a RegistrySnapshot.
// Once Snapshot() is called the recorder is finalized; any subsequent
// registration method panics to prevent lazy-registration bugs.
type RegistryRecorder struct {
	cfg  map[string]any
	mode outbox.DurabilityMode
	log  *slog.Logger

	// accumulators
	routeGroups        []RouteGroup
	subscriptions      []SubscriptionRequest
	lifecycleHooks     []LifecycleHook
	configReloaders    []ConfigReloadRequest
	probes             []healthz.Probe
	probeNames         map[healthz.ProbeName]struct{}
	webhookReceivers   []WebhookReceiverRequest
	webhookDispatchers []WebhookDispatchRequest
	projections        []ProjectionRequest
	projectionIDs      map[string]struct{}

	finalized bool
}

// Compile-time check: RegistryRecorder satisfies Registrar.
var _ Registrar = (*RegistryRecorder)(nil)

// NewRegistryRecorder constructs a RegistryRecorder with the given config
// snapshot and durability mode. The recorder is a pure write-side accumulator:
// probes registered via reg.RegisterReadiness(...) are collected into the
// RegistrySnapshot.Probes slice, which the bootstrap layer drains onto the
// runtime healthz.Aggregator after Init — the recorder never holds a live
// aggregator, mirroring how RouteGroups / Subscriptions are accumulated.
func NewRegistryRecorder(cfg map[string]any, mode outbox.DurabilityMode) *RegistryRecorder {
	return NewRegistryRecorderWithLogger(cfg, mode, slog.Default())
}

// NewRegistryRecorderWithLogger constructs a RegistryRecorder with a custom
// logger. Provided for testing so log output can be captured.
func NewRegistryRecorderWithLogger(cfg map[string]any, mode outbox.DurabilityMode, log *slog.Logger) *RegistryRecorder {
	return &RegistryRecorder{
		cfg:           cfg,
		mode:          mode,
		log:           log,
		probeNames:    make(map[healthz.ProbeName]struct{}),
		projectionIDs: make(map[string]struct{}),
	}
}

// Config returns the per-cell config snapshot.
func (r *RegistryRecorder) Config() map[string]any { return r.cfg }

// DurabilityMode returns the assembly-level durability mode.
func (r *RegistryRecorder) DurabilityMode() outbox.DurabilityMode { return r.mode }

// RouteGroup appends a RouteGroup declaration.
func (r *RegistryRecorder) RouteGroup(g RouteGroup) {
	r.mustNotBeFinalized("RouteGroup")
	r.routeGroups = append(r.routeGroups, g)
}

// Subscribe validates and appends a SubscriptionRequest.
func (r *RegistryRecorder) Subscribe(
	spec contractspec.ContractSpec,
	handler outbox.EntryHandler,
	consumerGroup string,
	cellID string,
	opts ...SubscriptionOption,
) error {
	r.mustNotBeFinalized("Subscribe")

	if handler == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"registry Subscribe: handler must not be nil")
	}
	if consumerGroup == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"registry Subscribe: consumerGroup must not be empty")
	}
	if cellID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"registry Subscribe: cellID must not be empty")
	}
	if spec.Kind != cellvocab.ContractEvent {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"registry Subscribe: spec.Kind must be \"event\"",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("got=%q", spec.Kind))))
	}
	if spec.Topic == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"registry Subscribe: spec.Topic must not be empty")
	}
	if err := spec.Validate(); err != nil {
		return err
	}

	req := SubscriptionRequest{
		Spec:          spec,
		Handler:       handler,
		ConsumerGroup: consumerGroup,
		CellID:        cellID,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&req)
		}
	}

	r.subscriptions = append(r.subscriptions, req)
	return nil
}

// RegisterWebhookReceiver validates and appends a WebhookReceiverRequest
// (record-only — see Registrar.RegisterWebhookReceiver godoc).
func (r *RegistryRecorder) RegisterWebhookReceiver(
	spec webhook.ReceiverSpec,
	handler webhook.WebhookReceiveHandler,
) error {
	r.mustNotBeFinalized("RegisterWebhookReceiver")

	if handler == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"registry RegisterWebhookReceiver: handler must not be nil")
	}
	if err := spec.Validate(); err != nil {
		return err
	}

	r.webhookReceivers = append(r.webhookReceivers, WebhookReceiverRequest{
		Spec:    spec,
		Handler: handler,
	})
	return nil
}

// RegisterWebhookDispatch validates and appends a WebhookDispatchRequest
// (record-only — see Registrar.RegisterWebhookDispatch godoc).
func (r *RegistryRecorder) RegisterWebhookDispatch(
	spec webhook.DispatchSpec,
	selector webhook.WebhookDispatchSelector,
) error {
	r.mustNotBeFinalized("RegisterWebhookDispatch")

	if selector == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"registry RegisterWebhookDispatch: selector must not be nil")
	}
	if err := spec.Validate(); err != nil {
		return err
	}

	r.webhookDispatchers = append(r.webhookDispatchers, WebhookDispatchRequest{
		Spec:     spec,
		Selector: selector,
	})
	return nil
}

// RegisterProjection validates and appends a ProjectionRequest (record-only —
// see Registrar.RegisterProjection godoc). Validation mirrors Subscribe (the
// projection becomes an event subscription once drained): non-nil Apply,
// non-empty ProjectionID / CellID, and an event-kind Spec with a non-empty
// Topic. OnReset is optional and not validated.
func (r *RegistryRecorder) RegisterProjection(req ProjectionRequest) error {
	r.mustNotBeFinalized("RegisterProjection")

	if req.Apply == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"registry RegisterProjection: Apply must not be nil")
	}
	if req.ProjectionID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"registry RegisterProjection: ProjectionID must not be empty")
	}
	if req.CellID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"registry RegisterProjection: CellID must not be empty")
	}
	if req.Spec.Kind != cellvocab.ContractEvent {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"registry RegisterProjection: Spec.Kind must be \"event\"; a projection consumes an event-kind input stream",
			errcode.WithInternal(errcode.InternalAttr("specKind", req.Spec.Kind)))
	}
	if req.Spec.Topic == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"registry RegisterProjection: Spec.Topic must not be empty")
	}
	if err := req.Spec.Validate(); err != nil {
		return err
	}
	// Single-projection-single-subscriber premise (#1369): the consumer group is
	// derived as "<cellID>-<projectionID>" and the checkpoint key is (cellID,
	// projectionID). A duplicate ProjectionID would register two competing
	// subscribers on one consumer group — voiding the strictly-serial in-order
	// delivery the projection checkpoint requires (the transport-level
	// SerialInOrderGuarantor guard is scoped to a SINGLE subscriber per group).
	// The recorder is per-cell, so ProjectionID uniqueness == per-cell uniqueness.
	if _, dup := r.projectionIDs[req.ProjectionID]; dup {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"registry RegisterProjection: duplicate ProjectionID; each projection must have a "+
				"unique ID within its cell (the consumer group and checkpoint key are derived from "+
				"cellID+projectionID — a duplicate registers two competing subscribers on one consumer "+
				"group, voiding the serial in-order delivery the projection checkpoint requires)",
			errcode.WithInternal(
				errcode.InternalAttr("projectionID", req.ProjectionID),
				errcode.InternalAttr("cellID", req.CellID)))
	}

	r.projectionIDs[req.ProjectionID] = struct{}{}
	r.projections = append(r.projections, req)
	return nil
}

// RegisterReadiness accumulates a probe under the typed name. First-wins
// duplicate semantics match the runtime aggregator: a second registration
// with the same name returns [healthz.ErrDuplicateProbe] and is not stored.
//
// name is the single source of truth: the recorder wraps prober.Check
// under name (via [healthz.NewProbe]) even when prober is itself a
// [healthz.Probe] with a different Name(), so the probe surfaced from
// RegistrySnapshot.Probes always reports the funnel-supplied name.
func (r *RegistryRecorder) RegisterReadiness(name healthz.ProbeName, prober healthz.Prober) error {
	r.mustNotBeFinalized("RegisterReadiness")
	if name == "" {
		const emptyMsg = "registry RegisterReadiness: probe name must not be empty; " +
			"use a typed const (e.g. postgres.ProbeReady) or healthz.MustProbeName in tests"
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, emptyMsg)
	}
	if validation.IsNilInterface(prober) {
		return fmt.Errorf("%w: nil prober for probe %q", healthz.ErrInvalidProbeName, name)
	}
	if _, dup := r.probeNames[name]; dup {
		return fmt.Errorf("%w: probe %q", healthz.ErrDuplicateProbe, name)
	}
	r.probeNames[name] = struct{}{}
	r.probes = append(r.probes, healthz.NewProbe(name, prober.Check))
	return nil
}

// Lifecycle appends a lifecycle hook. Panics when Name is empty (programming error).
func (r *RegistryRecorder) Lifecycle(h LifecycleHook) {
	r.mustNotBeFinalized("Lifecycle")
	MustHaveLifecycleHookName(h)
	r.lifecycleHooks = append(r.lifecycleHooks, h)
}

// MustHaveLifecycleHookName panics when the hook Name is empty (programming error).
func MustHaveLifecycleHookName(h LifecycleHook) {
	if h.Name == "" {
		panic(panicregister.Approved("registry-lifecycle-hook-name", errcode.Assertion("registry Lifecycle: hook Name must not be empty")))
	}
}

// OnConfigReload registers a config-reload callback. Panics when prefixes
// contains an empty string or fn is nil (programming errors).
func (r *RegistryRecorder) OnConfigReload(
	prefixes []string,
	fn func(context.Context, ConfigChangeEvent) error,
) {
	r.mustNotBeFinalized("OnConfigReload")
	MustHaveNonEmptyConfigPrefixes(prefixes)
	MustHaveNonNilConfigReloadFn(fn)
	r.configReloaders = append(r.configReloaders, ConfigReloadRequest{
		Prefixes: prefixes,
		Fn:       fn,
	})
}

// MustHaveNonEmptyConfigPrefixes panics when any prefix is an empty string (programming error).
func MustHaveNonEmptyConfigPrefixes(prefixes []string) {
	for _, p := range prefixes {
		if p == "" {
			panic(panicregister.Approved("registry-reload-prefix-empty",
				errcode.Assertion("registry OnConfigReload: prefixes must not contain an empty string")))
		}
	}
}

// MustHaveNonNilConfigReloadFn panics when fn is nil (programming error).
func MustHaveNonNilConfigReloadFn(fn func(context.Context, ConfigChangeEvent) error) {
	if fn == nil {
		panic(panicregister.Approved("registry-reload-fn-nil", errcode.Assertion("registry OnConfigReload: fn must not be nil")))
	}
}

// Snapshot finalizes the recorder and returns an immutable RegistrySnapshot.
// After Snapshot is called, any further registration method panics.
//
// Health probes ARE included in the snapshot (Probes), accumulated from
// reg.RegisterReadiness(...) calls during Init. The bootstrap layer drains
// them onto the runtime healthz.Aggregator after all cells initialize —
// identical to how RouteGroups / Subscriptions / LifecycleHooks are drained.
func (r *RegistryRecorder) Snapshot() RegistrySnapshot {
	r.finalized = true

	// Defensive copy of route groups.
	rgs := make([]RouteGroup, len(r.routeGroups))
	copy(rgs, r.routeGroups)

	subs := make([]SubscriptionRequest, len(r.subscriptions))
	copy(subs, r.subscriptions)

	hooks := make([]LifecycleHook, len(r.lifecycleHooks))
	copy(hooks, r.lifecycleHooks)

	reloaders := make([]ConfigReloadRequest, len(r.configReloaders))
	copy(reloaders, r.configReloaders)

	probes := make([]healthz.Probe, len(r.probes))
	copy(probes, r.probes)

	whRecv := make([]WebhookReceiverRequest, len(r.webhookReceivers))
	copy(whRecv, r.webhookReceivers)

	whDisp := make([]WebhookDispatchRequest, len(r.webhookDispatchers))
	copy(whDisp, r.webhookDispatchers)

	projs := make([]ProjectionRequest, len(r.projections))
	copy(projs, r.projections)

	return RegistrySnapshot{
		RouteGroups:        rgs,
		Subscriptions:      subs,
		LifecycleHooks:     hooks,
		ConfigReloaders:    reloaders,
		Probes:             probes,
		WebhookReceivers:   whRecv,
		WebhookDispatchers: whDisp,
		Projections:        projs,
	}
}

// mustNotBeFinalized panics when the recorder has already been finalized.
func (r *RegistryRecorder) mustNotBeFinalized(method string) {
	MustNotBeRegistryFinalized(r.finalized, method)
}

// MustNotBeRegistryFinalized panics when finalized is true (programming error).
func MustNotBeRegistryFinalized(finalized bool, method string) {
	if finalized {
		panic(panicregister.Approved("registry-post-snapshot-mutate",
			errcode.Assertion("registry %s: called after Snapshot() — registration must happen during Cell.Init", method)))
	}
}
