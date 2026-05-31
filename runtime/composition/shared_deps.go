package composition

import (
	"errors"
	"fmt"

	"github.com/ghbvf/gocell/kernel/clock"
	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	"github.com/ghbvf/gocell/kernel/idempotency"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/capability"
	"github.com/ghbvf/gocell/runtime/eventbus"
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

	// EventBus is the in-process event bus for publish and subscribe.
	EventBus *eventbus.InMemoryEventBus

	// ConfigEventCollector records config consumer process and settlement metrics.
	// Registered once against MetricsProvider and injected into accesscore and
	// configcore.
	ConfigEventCollector obmetrics.ConfigEventCollector

	// EventbusCacheCollector records configsubscribe subscriber-cache tombstone
	// GC evictions.
	EventbusCacheCollector obmetrics.EventbusCacheCollector

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

	// ConfigKeyProvider is the configcore value-encryption key provider.
	// Built in cmd/corebundle (which may import adapters/vault + prometheus),
	// passed to cellmodules/configcore.Module so that the composition module
	// layer never imports adapter-specific packages. Nil means no key provider;
	// in real adapter mode configcore rejects nil (NoopTransformer is dev-only).
	ConfigKeyProvider kcrypto.KeyProvider

	// ConfigStaleCipherInc is a zero-arg callback that increments the
	// stale-cipher counter once per config value read that is encrypted with a
	// non-current key version. Built in cmd/corebundle from a prometheus counter
	// so that runtime/composition never imports github.com/prometheus/client_golang.
	// Nil means no-op (acceptable in tests that do not exercise PG + encryption).
	ConfigStaleCipherInc func()
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

// validate checks that all required cross-cutting dependencies are present. It
// mirrors the field-presence half of cmd/corebundle's validateCore, but omits
// production-control-plane checks (nonce store kind, claimer kind, health
// reachability) that depend on cmd-private types.
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
	if d.EventBus == nil {
		missing("EventBus")
	}
	if validation.IsNilInterface(d.ConfigEventCollector) {
		missing("ConfigEventCollector")
	}
	if validation.IsNilInterface(d.EventbusCacheCollector) {
		missing("EventbusCacheCollector")
	}
	if validation.IsNilInterface(d.ConsumerClaimer) {
		missing("ConsumerClaimer")
	}

	return errors.Join(errs...)
}
