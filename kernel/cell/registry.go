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

	"github.com/ghbvf/gocell/kernel/contractspec"
	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
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
// and config-reload callbacks. Health probes are no longer stored in the
// snapshot: they are registered directly on the healthz.Aggregator that was
// injected into the RegistryRecorder at construction time.
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
	routeGroups     []RouteGroup
	subscriptions   []SubscriptionRequest
	lifecycleHooks  []LifecycleHook
	configReloaders []ConfigReloadRequest
	probes          []healthz.Probe
	probeNames      map[healthz.ProbeName]struct{}

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
		cfg:        cfg,
		mode:       mode,
		log:        log,
		probeNames: make(map[healthz.ProbeName]struct{}),
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
	if spec.Kind != "event" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"registry Subscribe: spec.Kind must be \"event\"",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("got=%q", spec.Kind))))
	}
	if spec.Topic == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"registry Subscribe: spec.Topic must not be empty")
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
		return fmt.Errorf("%w: empty probe name", healthz.ErrInvalidProbeName)
	}
	if prober == nil {
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

	return RegistrySnapshot{
		RouteGroups:     rgs,
		Subscriptions:   subs,
		LifecycleHooks:  hooks,
		ConfigReloaders: reloaders,
		Probes:          probes,
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
