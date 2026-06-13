package composition

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"

	kauth "github.com/ghbvf/gocell/kernel/auth"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/capability"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
)

// SharedDeps holds the cross-cutting dependencies consumed by every CellModule.
// It is the public, interface-only equivalent of cmd/corebundle's internal
// SharedDeps: all adapter-concrete types have been replaced with their
// kernel/runtime interface counterparts so that this package never imports
// adapters/ or prometheus/client_golang.
//
// Sealed construction: SharedDeps carries an unexported validity marker (valid)
// that only [NewSharedDeps] stamps after running validate(). [Builder.Build]
// refuses any instance whose marker is unset, so a package-external struct
// literal — which can populate the exported fields but can never set the
// unexported marker — cannot be fed to Build. The single construction surface is
// NewSharedDeps, so an unvalidated dep set is unconstructable for the consumer.
// Bare literals remain legal for reads (e.g. unit tests that exercise a helper
// reading one field without ever calling Build); only Build gates on the marker.
//
// Fields are flat (no concern-grouped sub-structs): SharedDeps is a
// composition-root bag whose fields cross consumer boundaries. Forcing a
// sub-struct layout would make cross-cutting consumptions look like boundary
// violations when in fact they are the natural shape of a composition root.
//
// Fields are genuinely cross-cutting (consumed by multiple cells or the runtime
// itself). SharedDeps is now fully cell-agnostic: the former configcore-specific
// metric fields (EventbusCacheCollector, ConfigStaleCipherInc) were removed in
// #1413, and ConfigKeyProvider (the last configcore-specific field) was removed in
// #1413/#885 — configcore now self-builds its key provider from env +
// MetricsProvider, using adapters/vault.TransitMetrics which is client_golang-free
// in non-test code post-#885.
//
// The exported field set is frozen by archtest SHAREDDEPS-FIELDSET-FROZEN-01
// (reflect golden): adding a field requires editing that golden AND justifying
// the newcomer here as genuinely cross-cutting — a cell-specific dep belongs on
// that cell's module (constructor / self-build), not on this shared bag.
//
// ref: uber-go/fx fx.Supply — shared values provided once to all modules.
// ref: kubernetes/kubernetes cmd/kube-apiserver/app/options/validation.go —
// all required fields validated in one place before startup.
type SharedDeps struct {
	// valid is the sealed-construction marker. It is unexported, so only code in
	// this package (NewSharedDeps) can set it; Build rejects instances where it
	// is false. Frozen by SHAREDDEPS-SEALED-MARKER-01.
	valid bool

	// Clock is the single root clock instance threaded through every adapter,
	// service, and middleware. Tests inject clockmock.FakeClock; production
	// wires clock.Real() exactly once at the entry point.
	Clock clock.Clock

	// Topology is the resolved adapter-mode / storage-backend combination. It
	// must be obtained from bootstrap.NewTopology / TopologyFromEnv (it is a
	// sealed type); a zero Topology fails validation.
	Topology bootstrap.Topology

	// JWTIssuer signs access tokens for the accesscore cell.
	// Source: runtime/auth.NewJWTIssuer (via authconfig.NewJWTIssuerFromRegistry).
	JWTIssuer *auth.JWTIssuer

	// JWTVerifier validates inbound access tokens.
	// Source: runtime/auth.NewJWTVerifier (via authconfig.NewJWTVerifierFromRegistry).
	JWTVerifier *auth.JWTVerifier

	// MetricsProvider is the kernel-neutral metrics backend. Production
	// callers assign a *promadapter.MetricProvider (which satisfies
	// kernelmetrics.Provider); tests may use kernelmetrics.NopProvider{}.
	//
	// The field type is intentionally the interface — this package must not
	// import adapters/prometheus or github.com/prometheus/client_golang.
	MetricsProvider kernelmetrics.Provider

	// Publisher is the outbox event publisher — the sink the relay publishes
	// already-persisted outbox entries to. In demo topology it is the in-process
	// eventbus; in postgres topology a real broker (RabbitMQ). The type is the
	// kernel/outbox interface so this package never imports runtime/eventbus or
	// adapters/* (same layering reason as MetricsProvider). The composition root
	// resolves it via cellmodules/eventtransport.Resolve, which gates the choice
	// on Topology and fail-closes postgres mode onto a real broker (#1940).
	Publisher outbox.Publisher

	// Subscriber is the outbox event subscriber — the source consumers read from.
	// In demo topology it is the SAME in-process eventbus instance as Publisher
	// (so in-process publish is visible to in-process subscribers); in postgres
	// topology the same broker's subscriber. Interface-typed for the same layering
	// reason as Publisher.
	Subscriber outbox.Subscriber

	// ConfigEventCollector records config consumer process and settlement
	// metrics. It is genuinely cross-cell — consumed by accesscore, configcore,
	// AND the corebundle config-event consumer middleware — so it stays on the
	// shared bag (registered once against MetricsProvider, injected into all
	// consumers). It is NOT a configcore-specific field.
	ConfigEventCollector obmetrics.ConfigEventCollector

	// ConsumerClaimer coordinates outbox consumer idempotency.
	ConsumerClaimer idempotency.Claimer

	// PG is the assembly's single postgres capability provider. Nil in
	// non-postgres modes; cell modules take their in-memory path.
	PG capability.PGProvider

	// Redis is the assembly's shared redis capability provider. Nil in modes
	// without redis.
	Redis capability.RedisProvider

	// InternalHMACRing is the HMAC key ring for /internal/v1/* service-token
	// signing and verification. Promoted from cmd/corebundle's private
	// internalGuard struct so cell modules can sign outbound requests without
	// coupling to the cmd-private type.
	InternalHMACRing *auth.HMACKeyRing

	// NonceStore is the replay-defense store backing the /internal/v1/*
	// service-token guard. Promoted from cmd/corebundle's private internalGuard
	// struct (alongside InternalHMACRing) so that production control-plane
	// validation can introspect Kind() at the composition boundary: in adapter
	// mode "real" a NoopNonceStore is rejected, and an in-memory store requires
	// the single-pod acknowledgement. Nil is permitted in dev/test adapter
	// modes (the Kind checks are real-mode-only); see validateProductionControlPlane.
	//
	// Note: although validate() only checks NonceStore in real adapter mode, any
	// consumer that wires the internal-listener auth chain via
	// auth.NewAuthServiceToken must supply a non-nil NonceStore — that constructor
	// fail-fasts on nil regardless of adapter mode. In dev/test supply
	// auth.NewInMemoryNonceStore(...).
	NonceStore kauth.NonceStore

	// PrimaryHTTPAddr is the bind address for the public HTTP listener.
	PrimaryHTTPAddr string

	// InternalHTTPAddr is the bind address for the internal HTTP listener.
	InternalHTTPAddr string

	// HealthHTTPAddr is the bind address for the health+metrics listener.
	HealthHTTPAddr string

	// MetricsToken guards /metrics (X-Metrics-Token header).
	MetricsToken string

	// VerboseToken guards /readyz?verbose (X-Readyz-Token header).
	VerboseToken string

	// VerboseDisabled declares that /readyz?verbose must not be served.
	VerboseDisabled bool

	// HealthLocalOnly explicitly waives the loopback-only HealthHTTPAddr guard.
	// Must be false in production; set true only in tests or single-node
	// deployments where the health listener is deliberately bound to a
	// non-loopback address.
	HealthLocalOnly bool

	// ProjectRoot is the directory used by the devtools catalog endpoint.
	// Empty means devtools catalog is disabled; external callers that do not
	// need devtools functionality should leave this field unset.
	ProjectRoot string
}

// NewSharedDeps validates a populated SharedDeps and returns a sealed copy. The
// returned *SharedDeps carries the unexported validity marker that
// [Builder.Build] requires; a package-external SharedDeps literal cannot set it,
// so Build rejects any instance not produced here. This is the single
// construction surface for a Build-able SharedDeps.
//
// ref: kubernetes/kubernetes cmd/kube-apiserver/app/options/validation.go —
// validates all fields before any component is constructed.
func NewSharedDeps(d SharedDeps) (*SharedDeps, error) {
	if err := d.validate(); err != nil {
		return nil, err
	}
	d.valid = true
	return &d, nil
}

// validate checks that all required cross-cutting dependencies are present and
// runs every composition-contract startup guard. Guards apply in two groups:
//
//   - Always-required (every adapter mode): IL1 (InternalHTTPAddr must be set),
//     IL2 (InternalHMACRing must be set), V1/V2 (verbose endpoint must be
//     token-gated or explicitly disabled).
//   - Real-adapter-mode-only: CP1 (VerboseDisabled forbidden), CP3 (MetricsToken
//     required), CP5 (NonceStore must be set), CP6 (NoopNonceStore rejected),
//     CP7 (in-memory store rejected for multi-pod), CP8 (distributed claimer
//     required for multi-pod).
//
// These checks formerly lived in cmd/corebundle's validateCorebundleDeps and
// depended on cmd-private types (#1410); reading the now-promoted
// SharedDeps.NonceStore and the self-reporting ConsumerClaimer.Kind() lets
// every NewSharedDeps consumer — not just cmd/corebundle — inherit them
// fail-closed. The only residual cmd-side check is the .env.example sample-token
// guard (a cmd deployment artifact).
func (d *SharedDeps) validate() error {
	if d == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"SharedDeps: nil receiver")
	}

	var errs []error
	// The missing field name is carried in the Internal channel: this is a
	// composition-root STARTUP config error (never a wire 4xx), surfaced to
	// operators/logs and to library callers via err.Error() — which renders
	// the Internal "_" attr but not Public Details. Internal is therefore the
	// channel that actually makes the field name visible where it's read.
	missing := func(field string) {
		errs = append(errs, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"SharedDeps field must be set",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("field=%s", field)))))
	}

	if d.Clock == nil {
		missing("Clock")
	}
	// A zero-value Topology has an empty StorageBackend; a Topology obtained from
	// bootstrap.NewTopology / TopologyFromEnv always normalizes it to "memory" or
	// "postgres", so an empty backend means the caller forgot to set Topology.
	if d.Topology.StorageBackend() == "" {
		missing("Topology")
	}
	if d.JWTIssuer == nil {
		missing("JWTIssuer")
	}
	if d.JWTVerifier == nil {
		missing("JWTVerifier")
	}
	if validation.IsNilInterface(d.MetricsProvider) {
		missing("MetricsProvider")
	}
	if validation.IsNilInterface(d.Publisher) {
		missing("Publisher")
	}
	if validation.IsNilInterface(d.Subscriber) {
		missing("Subscriber")
	}
	if validation.IsNilInterface(d.ConfigEventCollector) {
		missing("ConfigEventCollector")
	}
	if validation.IsNilInterface(d.ConsumerClaimer) {
		missing("ConsumerClaimer")
	}

	errs = append(errs, d.validateHealthReachability()...)
	errs = append(errs, d.validateControlPlane()...)

	return errors.Join(errs...)
}

// requiresDistributedReplay reports whether the topology demands a distributed
// (multi-pod) replay-defense posture: real adapter mode without the single-pod
// acknowledgement. It delegates to bootstrap.Topology.RequiresDistributedReplay
// (the single source); cmd/corebundle/redis.go's pre-SharedDeps copy delegates to
// the same method, so the rule lives in exactly one place rather than three.
func (d *SharedDeps) requiresDistributedReplay() bool {
	return d.Topology.RequiresDistributedReplay()
}

// validateControlPlane runs the control-plane production guards promoted from
// cmd/corebundle (#1410). The internal-listener guard and verbose-endpoint
// gating apply in every adapter mode; the token / nonce-store-kind / claimer-kind
// checks apply only in real adapter mode.
func (d *SharedDeps) validateControlPlane() []error {
	var errs []error
	errs = append(errs, d.validateInternalListenerGuard()...)
	errs = append(errs, d.validateVerboseEndpoint()...)
	if d.Topology.RequireProductionControlPlane() {
		errs = append(errs, d.validateProductionControlPlane()...)
	}
	return errs
}

// validateInternalListenerGuard enforces that the always-enabled internal
// listener has a bind address and a service-token HMAC ring in every adapter
// mode (IL1 / IL2). The internal listener protects /internal/v1/* and is never
// optional.
func (d *SharedDeps) validateInternalListenerGuard() []error {
	var errs []error
	if d.InternalHTTPAddr == "" {
		errs = append(errs, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"SharedDeps.InternalHTTPAddr must be set; the internal listener is always "+
				"enabled and protected by the service-token HMAC ring"))
	}
	if d.InternalHMACRing == nil {
		errs = append(errs, errcode.New(errcode.KindInternal, errcode.ErrControlplaneServiceSecretMissing,
			"SharedDeps.InternalHMACRing must be set to protect /internal/v1/*; "+
				"build it via auth.NewHMACKeyRing (cmd/corebundle convention: from GOCELL_SERVICE_SECRET)"))
	}
	return errs
}

// validateVerboseEndpoint enforces that every adapter mode either configures a
// verbose token or explicitly waives the endpoint (V1 / V2), so a forgotten
// GOCELL_READYZ_VERBOSE_TOKEN never silently exposes cell topology.
func (d *SharedDeps) validateVerboseEndpoint() []error {
	if d.VerboseDisabled {
		if d.VerboseToken != "" {
			// Both set: VerboseDisabled wins, but it is almost certainly a
			// misconfiguration. Surface it so operators spot it in startup logs.
			slog.Warn("controlplane: verbose endpoint config ambiguity",
				slog.String("hint", "SharedDeps.VerboseDisabled overrides a non-empty VerboseToken; "+
					"the token will not be enforced — clear one of the two fields"))
		}
		return nil
	}
	if d.VerboseToken != "" {
		return nil
	}
	return []error{errcode.New(errcode.KindInternal, errcode.ErrControlplaneVerboseTokenMissing,
		"GOCELL_READYZ_VERBOSE_TOKEN must be set (or GOCELL_READYZ_VERBOSE_DISABLED=1 to "+
			"waive the verbose endpoint) so /readyz?verbose is never anonymous "+
			"(these are SharedDeps.VerboseToken / VerboseDisabled fields; env-var names are cmd/corebundle convention)")}
}

// validateProductionControlPlane runs the real-adapter-mode gate: token-gated
// verbose + metrics endpoints (CP1 / CP3), a replay-safe nonce store of the
// right kind (CP5 / CP6 / CP7), and a distributed outbox idempotency claimer for
// multi-pod deployments (CP8). It is only invoked when
// Topology.RequireProductionControlPlane() is true.
func (d *SharedDeps) validateProductionControlPlane() []error {
	var errs []error
	if d.VerboseDisabled {
		errs = append(errs, errcode.New(errcode.KindInternal, errcode.ErrControlplaneVerboseTokenMissing,
			"SharedDeps.VerboseDisabled must not be set in adapter mode \"real\"; "+
				"production must keep the token-gated verbose endpoint available for "+
				"on-call diagnostics (cmd/corebundle convention: GOCELL_READYZ_VERBOSE_DISABLED)"))
	}
	if d.MetricsToken == "" {
		errs = append(errs, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"SharedDeps.MetricsToken must be set in adapter mode \"real\" to prevent anonymous "+
				"/metrics exposure; scrapers must send X-Metrics-Token header "+
				"(cmd/corebundle convention: GOCELL_METRICS_TOKEN)"))
	}
	errs = append(errs, d.validateProductionNonceStore()...)
	if d.requiresDistributedReplay() &&
		!validation.IsNilInterface(d.ConsumerClaimer) &&
		d.ConsumerClaimer.Kind() != idempotency.ClaimerKindDistributed {
		errs = append(errs, errcode.New(errcode.KindInternal, errcode.ErrControlplaneClaimerNotDistributed,
			"SharedDeps.ConsumerClaimer must report Kind() == ClaimerKindDistributed in real "+
				"multi-pod deployments; a single-process claimer cannot coordinate outbox "+
				"idempotency across pods. Inject a distributed (e.g. Redis-backed) claimer, or "+
				"acknowledge single-pod topology (cmd/corebundle convention: set GOCELL_REDIS_ADDR "+
				"for a distributed claimer, or GOCELL_SINGLE_POD=1)"))
	}
	return errs
}

// validateProductionNonceStore enforces the service-token replay-defense store
// kind in real adapter mode: present (CP5), not the no-op sentinel (CP6), not
// single-process in-memory for a multi-pod deployment (CP7), and — fail-closed —
// not an unrecognized kind (#1410 review F2).
//
// The accept/reject decision is single-sourced through
// kauth.NonceStoreKind.ReplaySafe — the SAME predicate runtime/bootstrap's phase0
// auth-plan check gates on (#1410 review F1), so the config-time check (this, on
// the declared SharedDeps.NonceStore) and the usage-time check (bootstrap, on the
// store that actually guards the listener) can never drift on "which kinds are
// safe". The switch below only chooses the diagnostic message for WHY an unsafe
// store was rejected; it does not re-decide safety.
func (d *SharedDeps) validateProductionNonceStore() []error {
	if validation.IsNilInterface(d.NonceStore) {
		return []error{errcode.New(errcode.KindInternal, errcode.ErrControlplaneNonceStoreMissing,
			"SharedDeps.NonceStore must be set in adapter mode \"real\" to protect "+
				"/internal/v1/* against replay; inject an InMemoryNonceStore (single pod) "+
				"or a shared store (multi-pod)")}
	}
	kind := d.NonceStore.Kind()
	if kind.ReplaySafe(d.requiresDistributedReplay()) {
		return nil
	}
	switch kind {
	case kauth.NonceStoreKindNoop:
		return []error{errcode.New(errcode.KindInternal, errcode.ErrControlplaneNonceStoreMissing,
			"control-plane NonceStore must be a replay-safe implementation in adapter mode "+
				"\"real\"; NoopNonceStore detected — inject InMemoryNonceStore (single pod) "+
				"or a shared store (multi-pod)")}
	case kauth.NonceStoreKindInMemory:
		// ReplaySafe(requireDistributed=false) is true, so reaching here means the
		// topology requires distributed replay (multi-pod).
		slog.Warn("controlplane: in-memory nonce store rejected for multi-pod deployment",
			slog.String("nonce_store_kind", string(kauth.NonceStoreKindInMemory)),
			slog.String("hint", "set GOCELL_SINGLE_POD=1 for single-pod deployments "+
				"or configure a distributed NonceStore"))
		return []error{errcode.New(errcode.KindInternal, errcode.ErrControlplaneNonceStoreMissing,
			"in-memory nonce store requires single-pod topology (Topology.SinglePodReplayProtection) "+
				"or a distributed store for multi-pod; refuse fail-open "+
				"(cmd/corebundle convention: GOCELL_SINGLE_POD=1)")}
	default:
		// Fail-closed: an unrecognized NonceStoreKind cannot be proven replay-safe.
		slog.Warn("controlplane: unrecognized nonce store kind rejected in adapter mode real",
			slog.String("nonce_store_kind", string(kind)),
			slog.String("hint", "use an in-memory (single-pod) or distributed nonce store; "+
				"a new replay-safe kind must be added to kauth.NonceStoreKind.ReplaySafe"))
		return []error{errcode.New(errcode.KindInternal, errcode.ErrControlplaneNonceStoreMissing,
			"control-plane NonceStore reports an unrecognized kind in adapter mode \"real\"; "+
				"only in-memory (single-pod) or distributed (any topology) replay-safe stores "+
				"are accepted — refuse fail-open")}
	}
}

// validateHealthReachability rejects a loopback-only health-listener bind address
// in production adapter mode, where kubelet HTTP probes and Prometheus PodIP /
// Service scrapes cannot reach container loopback. It reads only public SharedDeps
// fields (Topology, HealthHTTPAddr, HealthLocalOnly), so every composition
// consumer — not just cmd/corebundle — inherits the guard. Set HealthLocalOnly
// (GOCELL_HTTP_HEALTH_LOCAL_ONLY=1) only for same-pod sidecar / exec-probe
// deployments.
func (d *SharedDeps) validateHealthReachability() []error {
	if !d.Topology.RequireProductionControlPlane() || d.HealthLocalOnly {
		return nil
	}
	if d.HealthHTTPAddr == "" || !isLoopbackBindAddr(d.HealthHTTPAddr) {
		return nil
	}
	return []error{errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"GOCELL_HTTP_HEALTH_ADDR is loopback-only in adapter mode \"real\"; "+
			"kubelet HTTP probes and Prometheus PodIP/Service scrapes cannot "+
			"reach container loopback. Set GOCELL_HTTP_HEALTH_ADDR=:9091 "+
			"(or a Pod-reachable address), or set GOCELL_HTTP_HEALTH_LOCAL_ONLY=1 "+
			"only for same-pod sidecar or exec-probe deployments.")}
}

// isLoopbackBindAddr reports whether addr binds a loopback host (localhost or a
// loopback IP). A bare host with no port is treated as the host.
func isLoopbackBindAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
