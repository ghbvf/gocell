package cell

// ADR: kernel/cell depends on net/http (standard library)
//
// Status: Accepted
//
// Decision: kernel/cell uses net/http types (http.Handler, http.ResponseWriter,
// http.Request) in the RouteMux interface.
//
// Rationale: net/http is part of the Go standard library. The project's
// layering rules (CLAUDE.md) state "kernel/ only depends on stdlib + pkg/",
// so net/http is an allowed dependency. The Go 1.22+ enhanced ServeMux
// pattern syntax ("METHOD /path/{param}") gives kernel a powerful routing
// abstraction without importing any third-party router.
//
// Alternatives considered:
//   - Define custom Handler/ResponseWriter/Request interfaces to abstract
//     away net/http entirely. Rejected: this would add complexity (type
//     conversions, adapter layers) for no practical benefit, since net/http
//     is guaranteed stable by the Go compatibility promise.
//
// Consequences: Cells implementing RouteGroupContributor receive an http.Handler-
// compatible interface via the RouteMux in RouteGroup.Register closures.
// Concrete routers (chi, gorilla) are provided by runtime/ or adapters/ and
// implement RouteMux, keeping kernel free of third-party dependencies.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/wrapper"
)

// ---------------------------------------------------------------------------
// Optional registration interfaces
// ---------------------------------------------------------------------------
// These interfaces are optionally implemented by Cells. During bootstrap,
// the Assembly (or any orchestrator) discovers them via type assertion:
//
//	if rgc, ok := cell.(RouteGroupContributor); ok {
//	    groups = append(groups, rgc.RouteGroups()...)
//	}
//
// This keeps the core Cell interface slim while allowing Cells to opt-in to
// HTTP serving and event consumption.

// RouteMux is a minimal route registration interface.
// kernel/ does not import any specific router (chi, gorilla, etc.);
// concrete implementations are provided by runtime/ or adapters/.
//
// For testing, use kernel/cell/celltest.TestMux.
type RouteMux interface {
	// Handle registers handler for the given pattern.
	// Pattern follows Go 1.22+ enhanced ServeMux syntax: "METHOD /path/{param}".
	// Path parameters are extracted by the underlying router implementation and
	// accessible via r.PathValue("param") in the handler.
	//
	// Examples:
	//   mux.Handle("GET /users/{id}", handler)
	//   mux.Handle("POST /", handler)
	//   mux.Handle("DELETE /sessions/{id}", handler)
	Handle(pattern string, handler http.Handler)

	// Route creates a sub-router under pattern with prefix stripping.
	// Use for GoCell native route registration — the sub-router participates
	// in the framework's pattern matching, PathValue binding, and test model.
	Route(pattern string, fn func(sub RouteMux))

	// Mount attaches an opaque http.Handler sub-tree under pattern with prefix
	// stripping. The mounted handler is a "black box" that does not need to
	// follow GoCell routing conventions. Use Route + RegisterRoutes instead
	// when the sub-tree is a GoCell cell/slice.
	Mount(pattern string, handler http.Handler)

	// Group creates a same-level scope sharing the parent prefix.
	// Useful for applying middleware to a subset of routes.
	Group(fn func(RouteMux))

	// With returns a new RouteMux that inherits all routes and middleware
	// from this scope, plus the additional middleware provided.
	// Unlike a mutable Use(), With is safe to call after routes are registered
	// and does not modify the receiver.
	//
	// ref: go-chi/chi Mux.With — returns an inline router sharing the parent tree.
	With(mw ...func(http.Handler) http.Handler) RouteMux
}

// RouteHandler is the minimum route-registration surface shared by both the
// production RouteMux and stdlib *http.ServeMux. Slices expose
// RegisterRoutes(RouteHandler) so a single declaration — routed through
// auth.Mount — is the source of truth for production wiring (called from
// Cell.RegisterRoutes), contract tests, and cell-level integration tests.
//
// Both cell.RouteMux and *http.ServeMux satisfy this interface structurally
// (each declares Handle(pattern string, handler http.Handler)), so slices do
// not need to know which one they receive at call time.
//
// Rationale: early designs let slices wrap handlers with handler-level auth
// helpers; cell.RegisterRoutes wiring raw HandlerFuncs on RouteMux allowed
// production to silently skip the wrapper, producing a policy-drift surface
// that passed contract tests but exposed unguarded routes in production.
// auth.Mount collapses the two paths into one.
//
// ref: kubernetes/kubernetes pkg/endpoints/installer.go — one installer type
// for all write handlers; authz chain is declared at registration time.
// ref: go-kratos/kratos transport/http/server.go — route + middleware pair
// declared once; both runtime and test paths consume the same registration.
type RouteHandler interface {
	Handle(pattern string, handler http.Handler)
}

// Prefixer is optionally implemented by RouteHandler values whose chi
// sub-router owns a mount prefix (i.e. `mux.Route("/api/v1/access", fn)`).
// auth.Mount type-asserts to this interface to compute the chi-relative
// registration path from a fully-qualified Contract.Path — the mount
// prefix is stripped so chi's own prefix-composition produces the correct
// external URL.
//
// runtime/http/router.Router and its nested chiRouterAdapter implement this
// interface; plain *http.ServeMux / test stubs do not, in which case
// auth.Mount uses Contract.Path as-is (fine because those paths are
// typically fully-qualified already and the mux has no prefix to strip).
type Prefixer interface {
	Prefix() string
}

// AuthRouteMeta carries the auth-related attributes a slice declares when
// registering a route. It is pure data — no Policy or Handler references —
// so kernel/cell stays decoupled from runtime/auth.
//
// Aggregators (Router, TestMux) collect instances during RegisterRoutes and
// compile them into matchers at finalize time.
type AuthRouteMeta struct {
	// Method is the HTTP verb (e.g. "POST"). Required and non-empty.
	Method string
	// Path is the Go 1.22 ServeMux pattern path (e.g. "/api/v1/access/sessions/{id}").
	// Must start with '/'.
	Path string
	// Public marks the route as JWT-exempt — the AuthMiddleware bypasses JWT
	// verification entirely. Mutually exclusive with any server-side Policy.
	Public bool
	// PasswordResetExempt marks the route as allowed to pass through the
	// password-reset gate when an authenticated token carries
	// password_reset_required=true. Only meaningful when Public is false.
	PasswordResetExempt bool
}

// InternalPathPrefix is the URL prefix that designates an internal-listener route
// (service-token / mTLS auth, no JWT). Routes with this prefix are dispatched to
// cell.InternalListener; all other routes go to cell.PublicListener.
const InternalPathPrefix = "/internal/v1/"

// IsInternal reports whether this route lives on the internal listener,
// derived from the URL path prefix.
//
// The authoring invariant (only InternalListener routes may begin with
// InternalPathPrefix) is enforced by FinalizeAuth in runtime/http/router.
func (m AuthRouteMeta) IsInternal() bool {
	return strings.HasPrefix(m.Path, InternalPathPrefix)
}

// AuthRouteDeclarer is implemented by aggregators that want to receive the
// auth metadata a slice declares alongside a route. auth.Mount performs a
// type-assertion on the receiving mux — when implemented, it forwards the
// metadata via DeclareAuthMeta; otherwise only the route is registered.
//
// This keeps kernel/cell free of runtime/auth dependencies while still
// letting the Router and TestMux aggregate per-route auth context.
type AuthRouteDeclarer interface {
	DeclareAuthMeta(meta AuthRouteMeta)
}

// HTTPContractDeclarer is implemented by aggregators that want to receive the
// ContractSpec a slice declares alongside an HTTP route. auth.Mount forwards
// this metadata when available so outer runtime middleware can annotate spans
// even when pre-handler middleware short-circuits before wrapper.HTTPHandler.
type HTTPContractDeclarer interface {
	DeclareHTTPContract(spec wrapper.ContractSpec)
}

// EventRouter declares event subscriptions. Cells call AddContractHandler
// during RegisterSubscriptions to declare intent; the caller
// (bootstrap/Router) is responsible for starting consumption.
//
// The minimal interface lives in kernel/cell so Cells can depend on it
// without importing runtime/. The concrete implementation is in
// runtime/eventrouter.
//
// ref: ThreeDotsLabs/watermill message/router.go — AddContractHandler registers
// intent; Router.Run starts consumption. GoCell simplifies to topic+handler
// (no publish side in the same call).
//
// ref: Kafka ConsumerGroup — consumerGroup isolates per-Cell consumption.
// Same group competes; different groups each get a full copy (fanout).
// consumerGroup MUST NOT be empty — Cells must declare their identity
// to ensure portable dispatch semantics across all backends.
//
// AddContractHandler mirrors the HTTP-side auth.Mount(Route{Contract, ...})
// shape for the consumer side: the ContractSpec is the source of truth for
// the topic + observability metadata.
type EventRouter interface {
	// AddContractHandler registers a contract-first subscription. The
	// concrete Router stores the contract metadata on outbox.Subscription;
	// bootstrap's ContractTracingMiddleware wraps the subscription so every
	// consumed entry emits a CONSUME span annotated with gocell.contract.id
	// / messaging.destination.
	//
	// Returns a non-nil error when handler is nil, consumerGroup is empty,
	// spec.Kind != "event", or spec.Validate() fails. Callers (typically
	// Cell.RegisterSubscriptions) should propagate the error.
	AddContractHandler(spec wrapper.ContractSpec, handler outbox.EntryHandler, consumerGroup string) error
}

// EventRegistrar is optionally implemented by Cells that subscribe to events.
// RegisterSubscriptions declares subscriptions by calling r.AddContractHandler
// for each contract. It MUST NOT start goroutines or block — the Router
// manages the subscription lifecycle.
type EventRegistrar interface {
	RegisterSubscriptions(r EventRouter) error
}

// ---------------------------------------------------------------------------
// Config hot-reload callback
// ---------------------------------------------------------------------------

// ConfigChangeEvent describes what changed during a config reload.
// The event is computed by the bootstrap layer and passed to ConfigReloader
// cells after a successful config file reload.
//
// ref: micro/go-micro config/watcher.go — checksum-based change dedup
// Adopted: explicit diff (Added/Updated/Removed) instead of opaque ChangeSet.
// Deviated from spf13/viper: includes key-level delta, not just a notification.
type ConfigChangeEvent struct {
	// Added contains keys present in the new config but absent in the old.
	Added []string
	// Updated contains keys present in both configs with different values.
	Updated []string
	// Removed contains keys present in the old config but absent in the new.
	Removed []string
	// Config is a deep copy of the reloaded config snapshot, isolated per
	// cell (same type as Dependencies.Config). Mutating it has no effect on
	// other cells or the framework.
	Config map[string]any
	// Generation is the monotonically increasing reload counter from the config
	// subsystem (source-of-truth version). Starts at 0 (initial load); increments
	// by 1 on each successful Reload.
	//
	// This is a desired-state indicator, NOT an applied-state indicator: it
	// reflects that the config source has been updated, not that all cells have
	// successfully applied the new values. Cells can compare with their last-seen
	// generation to detect drift (observer-only semantics).
	Generation int64
}

// HealthContributor is optionally implemented by Cells that expose internal
// component health probes. Bootstrap discovers HealthContributor cells via
// type assertion after assembly.Start and registers each returned checker
// in /readyz.
//
// HealthCheckers is called once during bootstrap startup (post-Init,
// post-Start, before HTTP listen). It returns a map of named probes
// (e.g. "session-store" → fn). A nil return or empty map means the cell
// has no additional probes beyond the base Cell.Health() status. Probe
// names MUST be unique across all cells; bootstrap fails fast on duplicates.
//
// Thread safety: the returned func(context.Context) error values are called
// on every /readyz HTTP request and MUST be safe for concurrent invocation.
// The context carries the /readyz deadline so probes can honour cancellation.
//
// ref: Kubernetes PodSpec — explicit readinessProbe/livenessProbe per container
// Adopted: explicit named probes. Deviated: returned as map, not declarative YAML.
//
// ref: uber-go/fx Lifecycle.Append — each module registers own health hooks
// Adopted: cell-owned probes. Deviated: discovery-based, not explicit registration.
type HealthContributor interface {
	HealthCheckers() map[string]func(context.Context) error
}

// ConfigReloader is optionally implemented by Cells that need to react to
// configuration changes at runtime. Bootstrap discovers ConfigReloader cells
// via type assertion and calls OnConfigReload after each successful config
// file reload that produces at least one change.
//
// Consistency: L0 LocalOnly — in-process notification, no external side effects.
//
// OnConfigReload MUST NOT block for extended periods. If a cell needs to
// perform long-running reconfiguration, it should spawn a goroutine.
// Errors are logged but do not halt other cells' reload callbacks
// (best-effort, matching spf13/viper semantics).
//
// ref: spf13/viper viper.go — OnConfigChange callback after reload
// Adopted: callback-after-reload pattern.
// Deviated: typed ConfigChangeEvent with diff instead of raw fsnotify.Event.
//
// ref: go-kratos/kratos config/config.go — Observer func(string, Value)
// Adopted: typed change event. Deviated: one-to-many (Kratos is one-to-one).
type ConfigReloader interface {
	OnConfigReload(event ConfigChangeEvent) error
}

// ConfigKeyFilterer is optionally implemented by ConfigReloader cells that
// want notifications only for specific config key prefixes. If the changed
// keys don't match any prefix, the cell is skipped.
//
// An empty return (nil or []string{}) means receive ALL notifications (default).
//
// ref: kratos config/config.go — Watch(key, Observer) single-key registration
// ref: go-micro config/default.go — Watch(path...) key-scoped observation
type ConfigKeyFilterer interface {
	ConfigKeyPrefixes() []string
}

// LifecycleHook mirrors bootstrap.Hook shape but lives in kernel/ so Cell
// interfaces never depend on runtime/. Bootstrap copies these fields into
// its own bootstrap.Hook at phase3b discovery time.
//
// Field semantics:
//   - Name: diagnostic identifier used in slog fields (hook.start/hook.stop).
//     Empty string is accepted; bootstrap will still run the hook.
//   - OnStart / OnStop: nil is treated as no-op. A hook with both nil is
//     silently skipped by phase3b (it would never do anything).
//   - StartTimeout / StopTimeout: 0 → use bootstrap default (30s / 10s via
//     LifecycleConfig). Negative → no deadline applied. See
//     runtime/bootstrap/lifecycle.go applyTimeout().
//
// OnStart is expected to return promptly — do not block waiting on ctx.Done.
// Use OnStop for teardown. Long-running background work must be launched in a
// goroutine whose cancellation is triggered by OnStop.
//
// # Ordering semantics
//
// Within a single Cell, hooks returned by LifecycleHooks() are Appended in
// slice order (FIFO). Across Cells, phase3b iterates assembly.CellIDs() in
// registration order, so the first registered Cell's hooks run before the
// second Cell's. On shutdown, bootstrap.Lifecycle.Stop invokes OnStop in
// reverse-Append order (LIFO rollback), so the last Cell registered stops
// first. See runtime/bootstrap/lifecycle.go rollback().
//
// EXPERIMENTAL: contract is stable across the PR-A5a release but the blocking
// semantics (above) may be further formalized once a second Cell adopts the
// interface.
//
// ref: github.com/uber-go/fx internal/lifecycle/lifecycle.go Hook — adopted.
// ref: kernel/cell.HealthContributor — symmetric discovery pattern.
type LifecycleHook struct {
	Name         string
	OnStart      func(ctx context.Context) error
	OnStop       func(ctx context.Context) error
	StartTimeout time.Duration
	StopTimeout  time.Duration
}

// LifecycleContributor is auto-discovered by bootstrap at phase3b and its
// hooks appended to the bootstrap Lifecycle in Cell registration order
// (via assembly.CellIDs). Returning nil or an empty slice opts out — no
// hook is registered. Hooks with both OnStart and OnStop nil are silently
// skipped.
//
// Bootstrap calls LifecycleHooks once per cell, after Cell.Init completes
// and before bootstrap.Lifecycle.Start runs. OnStart closures may reference
// per-cell state populated during Init (e.g., injected repositories).
type LifecycleContributor interface {
	LifecycleHooks() []LifecycleHook
}
