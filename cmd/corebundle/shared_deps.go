package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
)

// SharedDeps holds cross-cutting dependencies required by every Cell module.
// Cell-specific dependencies (KeyProvider, PGResource, cursor codecs, HMAC key)
// are managed by the corresponding *_module.go file.
//
// SharedDeps is passed directly to BuildApp and each CellModule.Provide,
// giving type-safe access to all cross-cutting fields without type-assertion.
//
// ref: uber-go/fx fx.Supply — shared values provided once to all modules.
// ref: kubernetes/kubernetes cmd/kube-apiserver/app/options/validation.go —
// all required fields validated in one place before startup.
type SharedDeps struct {
	// Topology is the resolved adapter-mode / storage-backend combination.
	Topology bootstrap.Topology

	// JWTDeps holds the JWT issuer and verifier.
	JWTDeps jwtDeps

	// PromStack holds the Prometheus registry, hook observer, and metric provider.
	PromStack promStack

	// EventBus is the in-process event bus used for both publish and subscribe.
	EventBus *eventbus.InMemoryEventBus

	// InternalGuard is the service-token guard protecting /internal/v1/*.
	// Required when Topology.RequireProductionControlPlane() is true; nil in
	// dev mode (empty GOCELL_SERVICE_SECRET).
	//
	// Held as a typed value (rather than a bare middleware closure) so
	// validateControlPlane can inspect the backing NonceStore and reject
	// Noop implementations in production — a middleware func would make the
	// replay-defense class invisible to SharedDeps.Validate.
	InternalGuard *internalGuard

	// PrimaryHTTPAddr is the bind address for the public HTTP listener
	// (/api/v1/*, infra endpoints). Env GOCELL_HTTP_PRIMARY_ADDR; default ":8080".
	PrimaryHTTPAddr string

	// InternalHTTPAddr is the bind address for the internal HTTP listener
	// (/internal/v1/* control-plane). Env GOCELL_HTTP_INTERNAL_ADDR;
	// default ":9090". Must be bound to an internal network segment in
	// production so service-token / mTLS enforcement is the primary defence.
	InternalHTTPAddr string

	// MetricsToken is the token guarding /metrics. Required in production
	// topology; may be empty in dev mode.
	MetricsToken string

	// VerboseToken is the token guarding /readyz?verbose. After PR-A35
	// Validate() requires a non-empty token in every adapter mode unless
	// VerboseDisabled is true — the previous "empty in dev mode = open
	// verbose" backward-compat path was removed so that an unset environment
	// variable never silently exposes internal topology.
	VerboseToken string

	// VerboseDisabled declares that /readyz?verbose must not be served on
	// this deployment. When true, Validate() no longer requires VerboseToken
	// and Bootstrap is wired with WithVerboseDisabled so the handler answers
	// every ?verbose request with the plain aggregate body. Set it via
	// GOCELL_READYZ_VERBOSE_DISABLED=1 for ephemeral deployments that waive
	// the debug channel.
	VerboseDisabled bool

	// metricsHandler is the Prometheus HTTP handler built once in
	// LoadSharedDepsFromEnv and reused by defaultRuntimeOptions.
	metricsHandler http.Handler
}

// Validate is the startup invariant check for all cross-cutting dependencies.
// Storage-specific invariants (PGResource, cursor codecs, HMAC key) are checked
// inside the corresponding CellModule.Provide, not here.
//
// ref: kubernetes/kubernetes cmd/kube-apiserver/app/options/validation.go —
// validates all fields before any component is constructed.
func (d *SharedDeps) Validate() error {
	if d == nil {
		return errcode.New(errcode.ErrValidationFailed, "SharedDeps: nil receiver")
	}
	errs := d.validateCore()
	errs = append(errs, d.validateVerboseEndpoint()...)
	errs = append(errs, d.validateControlPlane()...)
	return errors.Join(errs...)
}

// validateVerboseEndpoint enforces that every adapter mode either configures
// a verbose token or explicitly waives the endpoint. The previous dev-mode
// fallback (unset env var => verbose open) was removed in PR-A35 so a
// forgotten GOCELL_READYZ_VERBOSE_TOKEN in dev cannot silently expose cell
// topology to anyone who can reach the port.
func (d *SharedDeps) validateVerboseEndpoint() []error {
	if d.VerboseDisabled {
		// Both set is not a hard validation failure — VerboseDisabled
		// wins, Handler will serve the plain aggregate body regardless of
		// the token. But it is almost certainly a misconfiguration: the
		// operator either wanted token-gated access (drop the DISABLED
		// flag) or wanted to waive verbose entirely (unset the TOKEN).
		// Surface it as a Warn so operators can spot it in startup logs.
		if d.VerboseToken != "" {
			slog.Warn("GOCELL_READYZ_VERBOSE_TOKEN is set but GOCELL_READYZ_VERBOSE_DISABLED=1 overrides it; " +
				"the token will not be enforced. Drop one of the two env vars to remove the ambiguity.")
		}
		return nil
	}
	if d.VerboseToken != "" {
		return nil
	}
	return []error{errcode.New(errcode.ErrControlplaneVerboseTokenMissing,
		"GOCELL_READYZ_VERBOSE_TOKEN must be set (or GOCELL_READYZ_VERBOSE_DISABLED=1 "+
			"to waive the verbose endpoint) so /readyz?verbose is never anonymous")}
}

// validateCore collects missing-field errors for dependencies required in
// every topology.
func (d *SharedDeps) validateCore() []error {
	var errs []error
	missing := func(field string) {
		errs = append(errs, errcode.New(errcode.ErrValidationFailed,
			"SharedDeps."+field+" must be set"))
	}
	if d.JWTDeps.issuer == nil {
		missing("JWTDeps.issuer")
	}
	if d.JWTDeps.verifier == nil {
		missing("JWTDeps.verifier")
	}
	if d.PromStack.registry == nil {
		missing("PromStack.registry")
	}
	if d.PromStack.hookObserver == nil {
		missing("PromStack.hookObserver")
	}
	if d.PromStack.metricProvider == nil {
		missing("PromStack.metricProvider")
	}
	if d.EventBus == nil {
		missing("EventBus")
	}
	return errs
}

// validateControlPlane collects errors for the production control-plane gate
// (tokens + guard required whenever real keys are in use).
func (d *SharedDeps) validateControlPlane() []error {
	if !d.Topology.RequireProductionControlPlane() {
		return nil
	}
	var errs []error
	// The unconditional /readyz?verbose invariant is now enforced by
	// validateVerboseEndpoint in every mode. Production additionally forbids
	// waiving the endpoint: a "real" deployment that still sets
	// GOCELL_READYZ_VERBOSE_DISABLED=1 is almost certainly a misconfiguration
	// and would leave operators without a token-gated diagnostic path.
	if d.VerboseDisabled {
		errs = append(errs, errcode.New(errcode.ErrControlplaneVerboseTokenMissing,
			"GOCELL_READYZ_VERBOSE_DISABLED=1 is not allowed in adapter mode \"real\"; "+
				"production must keep the token-gated verbose endpoint available for "+
				"on-call diagnostics"))
	}
	if d.MetricsToken == "" {
		errs = append(errs, errcode.New(errcode.ErrValidationFailed,
			"GOCELL_METRICS_TOKEN must be set in adapter mode \"real\" "+
				"to prevent anonymous /metrics exposure; scrapers must send X-Metrics-Token header"))
	}
	if d.InternalGuard == nil {
		errs = append(errs, errcode.New(errcode.ErrControlplaneServiceSecretMissing,
			"GOCELL_SERVICE_SECRET must be set in adapter mode \"real\" "+
				"to protect /internal/v1/*"))
	} else if ns := d.InternalGuard.NonceStore(); ns == nil {
		errs = append(errs, errcode.New(errcode.ErrControlplaneNonceStoreMissing,
			"internalGuard.nonceStore is nil; guard constructed without WithServiceTokenNonceStore"))
	} else if kind := ns.Kind(); kind == auth.NonceStoreKindNoop {
		errs = append(errs, errcode.New(errcode.ErrControlplaneNonceStoreMissing,
			"control-plane NonceStore must be a replay-safe implementation in "+
				"adapter mode \"real\"; NoopNonceStore detected — inject "+
				"InMemoryNonceStore (single pod) or a shared store (multi-pod) "+
				"via WithServiceTokenNonceStore"))
	} else if kind == auth.NonceStoreKindInMemory && !d.Topology.SinglePodReplayProtection && d.Topology.RequireProductionControlPlane() {
		errs = append(errs, errcode.New(errcode.ErrControlplaneNonceStoreMissing,
			"in-memory nonce store requires GOCELL_SINGLE_POD=1 (single-pod deployments) "+
				"or a distributed store via WithServiceTokenNonceStore (multi-pod); "+
				"refuse fail-open"))
	}
	return errs
}

// LoadSharedDepsFromEnv reads all environment variables and builds a fully
// populated SharedDeps for cross-cutting concerns. Cell-specific dependencies
// (cursor codecs, HMAC key, KeyProvider, PG config) are constructed in each
// CellModule.Provide.
//
// ref: go-zero serviceconf.MustLoad — single parse-validate call at startup.
func LoadSharedDepsFromEnv(ctx context.Context) (*SharedDeps, error) {
	topo, err := bootstrap.TopologyFromEnv()
	if err != nil {
		return nil, err
	}
	adapterMode := topo.AdapterMode

	jwt, err := buildJWTDeps(adapterMode)
	if err != nil {
		return nil, err
	}

	ps, err := buildPromStack()
	if err != nil {
		return nil, err
	}

	eb := eventbus.New()

	internalGuard, err := internalGuardFromEnv(adapterMode)
	if err != nil {
		return nil, err
	}

	verboseToken := os.Getenv("GOCELL_READYZ_VERBOSE_TOKEN")
	verboseDisabled := os.Getenv("GOCELL_READYZ_VERBOSE_DISABLED") == "1"

	metricsToken := os.Getenv("GOCELL_METRICS_TOKEN")
	metricsHandler, err := buildMetricsHandler(metricsToken, ps.registry)
	if err != nil {
		return nil, err
	}

	slog.Info("adapter mode",
		slog.String("requested", adapterMode),
		slog.String("effective", topo.AdapterInfo()["mode"]))

	// PR-A14a: surface the pre-PR-A14a env var rename so operators upgrading
	// from a single-listener binary see a clear signal if they have only the
	// old var set. Without this warn the addrs would silently fall through
	// to defaults, binding 8080/9090 instead of whatever the old
	// GOCELL_HTTP_ADDR pointed at.
	if legacy := os.Getenv("GOCELL_HTTP_ADDR"); legacy != "" {
		if os.Getenv("GOCELL_HTTP_PRIMARY_ADDR") == "" && os.Getenv("GOCELL_HTTP_INTERNAL_ADDR") == "" {
			slog.Warn("GOCELL_HTTP_ADDR is no longer consumed (PR-A14a dual-listener); set GOCELL_HTTP_PRIMARY_ADDR and GOCELL_HTTP_INTERNAL_ADDR instead",
				slog.String("legacy_value", legacy))
		}
	}

	primaryAddr := os.Getenv("GOCELL_HTTP_PRIMARY_ADDR")
	if primaryAddr == "" {
		primaryAddr = ":8080"
	}
	internalAddr := os.Getenv("GOCELL_HTTP_INTERNAL_ADDR")
	if internalAddr == "" {
		// Default to loopback so a dev-mode deployment without a
		// service-token guard is not trivially reachable across the
		// network. Operators binding to an internal VPC interface must set
		// GOCELL_HTTP_INTERNAL_ADDR explicitly.
		internalAddr = "127.0.0.1:9090"
	}

	deps := &SharedDeps{
		Topology:         topo,
		JWTDeps:          jwt,
		PromStack:        ps,
		EventBus:         eb,
		InternalGuard:    internalGuard,
		PrimaryHTTPAddr:  primaryAddr,
		InternalHTTPAddr: internalAddr,
		MetricsToken:     metricsToken,
		VerboseToken:     verboseToken,
		VerboseDisabled:  verboseDisabled,
		metricsHandler:   metricsHandler,
	}

	if err := deps.Validate(); err != nil {
		return nil, err
	}
	return deps, nil
}
