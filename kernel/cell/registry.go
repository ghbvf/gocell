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
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// ---------------------------------------------------------------------------
// Registry interface — the single registration surface for a Cell.
//
// A Cell calls methods on Registry inside its Init implementation to declare
// all capabilities: routes, subscriptions, health probes, lifecycle hooks,
// and config-reload callbacks. The concrete implementation (RegistryRecorder)
// accumulates declarations and returns them as a RegistrySnapshot once
// Snapshot() is called by the bootstrap layer.
//
// ref: uber-go/fx lifecycle.go@master:L33-L116 — Lifecycle.Append builder
// ref: kubernetes-sigs/controller-runtime pkg/manager/manager.go@main:L70-L78 — AddHealthzCheck independent method
// ref: go-kratos/kratos transport/http/server.go@main:L143-L224 — route spec accumulation
// ---------------------------------------------------------------------------

// Registry is the single registration surface a Cell uses inside Init to
// declare all capabilities. Each method appends to the RegistryRecorder's
// internal state. Calling any registration method after Snapshot() panics
// to prevent lazy-registration bugs.
type Registry interface {
	// Config returns the per-cell config snapshot provided by the assembly.
	Config() map[string]any
	// DurabilityMode returns the assembly-level durability mode.
	DurabilityMode() DurabilityMode

	// RouteGroup declares an HTTP route group. Groups accumulate in
	// declaration order and are mounted by bootstrap during phase5.
	RouteGroup(g RouteGroup)

	// Subscribe registers an event subscription. Returns a non-nil error when:
	//   - handler is nil
	//   - consumerGroup is empty
	//   - spec.Kind != "event"
	//
	// Cell.Init should propagate the error via `if err := ...; err != nil { return err }`.
	Subscribe(spec wrapper.ContractSpec, handler outbox.EntryHandler, consumerGroup string, opts ...SubscriptionOption) error

	// Health registers a named readiness probe. If name is already registered,
	// the duplicate is logged at slog.LevelError and silently dropped
	// (first-wins semantics). Probe functions must be safe for concurrent
	// invocation — they are called on every /readyz request.
	Health(name string, check func(context.Context) error)

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
}

// InternalPathPrefix is the URL prefix that designates an internal-listener route.
const InternalPathPrefix = "/internal/v1/"

// IsInternal reports whether this route lives on the internal listener.
func (m AuthRouteMeta) IsInternal() bool {
	return strings.HasPrefix(m.Path, InternalPathPrefix)
}

// AuthRouteDeclarer is implemented by aggregators that want to receive the
// auth metadata a slice declares alongside a route.
type AuthRouteDeclarer interface {
	DeclareAuthMeta(meta AuthRouteMeta) error
}

// HTTPContractDeclarer is implemented by aggregators that want to receive the
// ContractSpec a slice declares alongside an HTTP route.
type HTTPContractDeclarer interface {
	DeclareHTTPContract(spec wrapper.ContractSpec) error
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
	Spec          wrapper.ContractSpec
	Handler       outbox.EntryHandler
	ConsumerGroup string
	Options       SubscriptionOptions
	SliceID       string
}

// SubscriptionOptions carries optional event-subscription owner metadata.
type SubscriptionOptions struct {
	SliceID string
}

// SubscriptionOption configures optional event subscription metadata.
type SubscriptionOption func(*SubscriptionOptions)

// WithSubscriptionSliceID declares the owning slice for subscription observability.
func WithSubscriptionSliceID(sliceID string) SubscriptionOption {
	return func(o *SubscriptionOptions) {
		o.SliceID = sliceID
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
	// when passed to Registry.Lifecycle.
	Name         string
	OnStart      func(ctx context.Context) error
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
// Bootstrap reads these fields to wire routes, subscriptions, health probes,
// lifecycle hooks, and config-reload callbacks.
type RegistrySnapshot struct {
	RouteGroups     []RouteGroup
	Subscriptions   []SubscriptionRequest
	HealthCheckers  map[string]func(context.Context) error
	LifecycleHooks  []LifecycleHook
	ConfigReloaders []ConfigReloadRequest
}

// ---------------------------------------------------------------------------
// RegistryRecorder — the concrete accumulator
// ---------------------------------------------------------------------------

// RegistryRecorder implements Registry. It accumulates declarations during
// a Cell's Init call and returns them as a RegistrySnapshot.
// Once Snapshot() is called the recorder is finalized; any subsequent
// registration method panics to prevent lazy-registration bugs.
type RegistryRecorder struct {
	cfg  map[string]any
	mode DurabilityMode
	log  *slog.Logger

	// accumulators
	routeGroups     []RouteGroup
	subscriptions   []SubscriptionRequest
	healthCheckers  map[string]func(context.Context) error
	lifecycleHooks  []LifecycleHook
	configReloaders []ConfigReloadRequest

	finalized bool
}

// Compile-time check: RegistryRecorder satisfies Registry.
var _ Registry = (*RegistryRecorder)(nil)

// NewRegistryRecorder constructs a RegistryRecorder with the given config
// snapshot and durability mode. Uses the default slog logger.
func NewRegistryRecorder(cfg map[string]any, mode DurabilityMode) *RegistryRecorder {
	return NewRegistryRecorderWithLogger(cfg, mode, slog.Default())
}

// NewRegistryRecorderWithLogger constructs a RegistryRecorder with a custom
// logger. Provided for testing so log output can be captured.
func NewRegistryRecorderWithLogger(cfg map[string]any, mode DurabilityMode, log *slog.Logger) *RegistryRecorder {
	return &RegistryRecorder{
		cfg:            cfg,
		mode:           mode,
		log:            log,
		healthCheckers: make(map[string]func(context.Context) error),
	}
}

// Config returns the per-cell config snapshot.
func (r *RegistryRecorder) Config() map[string]any { return r.cfg }

// DurabilityMode returns the assembly-level durability mode.
func (r *RegistryRecorder) DurabilityMode() DurabilityMode { return r.mode }

// RouteGroup appends a RouteGroup declaration.
func (r *RegistryRecorder) RouteGroup(g RouteGroup) {
	r.mustNotBeFinalized("RouteGroup")
	r.routeGroups = append(r.routeGroups, g)
}

// Subscribe validates and appends a SubscriptionRequest.
func (r *RegistryRecorder) Subscribe(
	spec wrapper.ContractSpec,
	handler outbox.EntryHandler,
	consumerGroup string,
	opts ...SubscriptionOption,
) error {
	r.mustNotBeFinalized("Subscribe")

	if handler == nil {
		return errcode.New(errcode.ErrValidationFailed,
			"registry Subscribe: handler must not be nil")
	}
	if consumerGroup == "" {
		return errcode.New(errcode.ErrValidationFailed,
			"registry Subscribe: consumerGroup must not be empty")
	}
	if spec.Kind != "event" {
		return errcode.New(errcode.ErrValidationFailed,
			"registry Subscribe: spec.Kind must be \"event\", got \""+spec.Kind+"\"")
	}

	var subOpts SubscriptionOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&subOpts)
		}
	}

	r.subscriptions = append(r.subscriptions, SubscriptionRequest{
		Spec:          spec,
		Handler:       handler,
		ConsumerGroup: consumerGroup,
		Options:       subOpts,
		SliceID:       subOpts.SliceID,
	})
	return nil
}

// Health registers a named readiness probe. Duplicate names are logged at
// Error level and the second registration is silently dropped (first-wins).
func (r *RegistryRecorder) Health(name string, check func(context.Context) error) {
	r.mustNotBeFinalized("Health")
	if _, exists := r.healthCheckers[name]; exists {
		r.log.Error("registry Health: duplicate checker name — second registration dropped",
			slog.String("checker_name", name))
		return
	}
	r.healthCheckers[name] = check
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
		panic("registry Lifecycle: hook Name must not be empty (programming error)")
	}
}

// OnConfigReload registers a config-reload callback. Panics when prefixes
// contains an empty string (programming error).
func (r *RegistryRecorder) OnConfigReload(
	prefixes []string,
	fn func(context.Context, ConfigChangeEvent) error,
) {
	r.mustNotBeFinalized("OnConfigReload")
	MustHaveNonEmptyConfigPrefixes(prefixes)
	r.configReloaders = append(r.configReloaders, ConfigReloadRequest{
		Prefixes: prefixes,
		Fn:       fn,
	})
}

// MustHaveNonEmptyConfigPrefixes panics when any prefix is an empty string (programming error).
func MustHaveNonEmptyConfigPrefixes(prefixes []string) {
	for _, p := range prefixes {
		if p == "" {
			panic("registry OnConfigReload: prefixes must not contain an empty string (programming error)")
		}
	}
}

// Snapshot finalizes the recorder and returns an immutable RegistrySnapshot.
// After Snapshot is called, any further registration method panics.
func (r *RegistryRecorder) Snapshot() RegistrySnapshot {
	r.finalized = true

	// Defensive copy of route groups.
	rgs := make([]RouteGroup, len(r.routeGroups))
	copy(rgs, r.routeGroups)

	subs := make([]SubscriptionRequest, len(r.subscriptions))
	copy(subs, r.subscriptions)

	checkers := make(map[string]func(context.Context) error, len(r.healthCheckers))
	for k, v := range r.healthCheckers {
		checkers[k] = v
	}

	hooks := make([]LifecycleHook, len(r.lifecycleHooks))
	copy(hooks, r.lifecycleHooks)

	reloaders := make([]ConfigReloadRequest, len(r.configReloaders))
	copy(reloaders, r.configReloaders)

	return RegistrySnapshot{
		RouteGroups:     rgs,
		Subscriptions:   subs,
		HealthCheckers:  checkers,
		LifecycleHooks:  hooks,
		ConfigReloaders: reloaders,
	}
}

// mustNotBeFinalized panics when the recorder has already been finalized.
func (r *RegistryRecorder) mustNotBeFinalized(method string) {
	MustNotBeRegistryFinalized(r.finalized, method)
}

// MustNotBeRegistryFinalized panics when finalized is true (programming error).
func MustNotBeRegistryFinalized(finalized bool, method string) {
	if finalized {
		panic("registry " + method + ": called after Snapshot() — registration must happen during Cell.Init (programming error)")
	}
}
