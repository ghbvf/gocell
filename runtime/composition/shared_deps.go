package composition

import (
	"errors"

	"github.com/ghbvf/gocell/kernel/clock"
	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	"github.com/ghbvf/gocell/kernel/idempotency"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/audit"
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
// Fields are flat (no concern-grouped sub-structs): SharedDeps is a
// composition-root bag whose fields cross consumer boundaries.  Forcing a
// sub-struct layout would make cross-cutting consumptions look like boundary
// violations when in fact they are the natural shape of a composition root.
// Per-concern file split is appropriate; the struct itself stays flat,
// matching runtime/bootstrap/bootstrap.go which adopted the same trade-off.
//
// ref: uber-go/fx fx.Supply — shared values provided once to all modules.
// ref: kubernetes/kubernetes cmd/kube-apiserver/app/options/validation.go —
// all required fields validated in one place before startup.
type SharedDeps struct {
	// Clock is the single root clock instance threaded through every adapter,
	// service, and middleware.  Tests inject clockmock.FakeClock; production
	// wires clock.Real() exactly once at the entry point.
	Clock clock.Clock

	// Topology is the resolved adapter-mode / storage-backend combination.
	Topology bootstrap.Topology

	// JWTIssuer signs access tokens for the accesscore cell.
	// Source: runtime/auth.NewJWTIssuer (via authconfig.NewJWTIssuerFromRegistry).
	JWTIssuer *auth.JWTIssuer

	// JWTVerifier validates inbound access tokens.
	// Source: runtime/auth.NewJWTVerifier (via authconfig.NewJWTVerifierFromRegistry).
	JWTVerifier *auth.JWTVerifier

	// MetricsProvider is the kernel-neutral metrics backend.  Production
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

	// BootstrapLedgerStore is the sealed handle to the bootstrap auth-fail
	// audit chain, wired by AuditCoreModule.Provide and consumed by
	// AccessCoreModule.Provide via audit.NewBootstrapAuthFailObserver.
	//
	// Set by the auditcore module during CellModule.Provide; auditcore MUST
	// appear before accesscore in Builder.With
	// (MODULE-ORDER-AUDITCORE-BEFORE-ACCESSCORE-01).
	//
	// Not checked by Validate() — it is populated during BuildApp (after
	// Validate has already run).  See cmd/corebundle shared_deps_validate.go
	// for the rationale.
	BootstrapLedgerStore *audit.BootstrapLedgerStore

	// PG is the assembly's single postgres capability provider.  Nil in
	// non-postgres modes; cell modules take their in-memory path.
	PG capability.PGProvider

	// Redis is the assembly's shared redis capability provider.  Nil in modes
	// without redis.
	Redis capability.RedisProvider

	// InternalHMACRing is the HMAC key ring for /internal/v1/* service-token
	// signing and verification.  Promoted from cmd/corebundle's private
	// internalGuard struct so cell modules can sign outbound requests without
	// coupling to the cmd-private type.
	InternalHMACRing *auth.HMACKeyRing

	// AssemblyID is the stable identifier for this assembly (from assembly.yaml).
	// Empty in test mode.
	AssemblyID string

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
	// passed to platform/configcore.Module so that the platform layer never
	// imports adapter-specific packages.
	// Nil means no key provider configured; configcore uses NoopTransformer.
	ConfigKeyProvider kcrypto.KeyProvider

	// ConfigStaleCipherInc is a zero-arg callback that increments the
	// stale-cipher counter once per config value read that is encrypted with a
	// non-current key version.  Built in cmd/corebundle from a prometheus counter
	// so that runtime/composition never imports github.com/prometheus/client_golang.
	// Nil means no-op (acceptable in tests that do not exercise PG + encryption).
	ConfigStaleCipherInc func()
}

// Validate checks that all required cross-cutting dependencies are present.
// It mirrors the field-presence half of cmd/corebundle's validateCore, but
// omits production-control-plane checks (nonce store kind, claimer kind,
// health reachability) that depend on cmd-private types.
//
// BootstrapLedgerStore is intentionally NOT checked here — it is populated
// during BuildApp after Validate has already run (same rationale as
// cmd/corebundle shared_deps_validate.go::validateCore).
//
// ref: kubernetes/kubernetes cmd/kube-apiserver/app/options/validation.go —
// validates all fields before any component is constructed.
func (d *SharedDeps) Validate() error {
	if d == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"SharedDeps: nil receiver")
	}

	var errs []error
	missing := func(field string) {
		errs = append(errs, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"SharedDeps field must be set",
			errcode.WithDetails(errcode.PublicString("field", field))))
	}

	if d.Clock == nil {
		missing("Clock")
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
