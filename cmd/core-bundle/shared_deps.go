package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
)

// SharedDeps holds cross-cutting dependencies required by every Cell module.
// Cell-specific dependencies (KeyProvider, PGResource, cellOpts) are managed
// by the corresponding *_module.go file.
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

	// CursorCodecs holds the audit and config cursor codecs.
	CursorCodecs cursorCodecs

	// HMACKey is the HMAC secret for audit-core chain authentication.
	HMACKey []byte

	// EventBus is the in-process event bus used for both publish and subscribe.
	EventBus *eventbus.InMemoryEventBus

	// InternalGuard is the service-token middleware protecting /internal/v1/*.
	// Required when Topology.RequireProductionControlPlane() is true; nil in
	// dev mode (empty GOCELL_SERVICE_SECRET).
	InternalGuard func(http.Handler) http.Handler

	// MetricsToken is the token guarding /metrics. Required in production
	// topology; may be empty in dev mode.
	MetricsToken string

	// VerboseToken is the token guarding /readyz?verbose. Required in
	// production topology; may be empty in dev mode.
	VerboseToken string

	// metricsHandler is the Prometheus HTTP handler built once in
	// LoadSharedDepsFromEnv and reused by defaultRuntimeOptions.
	metricsHandler http.Handler
}

// Validate is the startup invariant check for all cross-cutting dependencies.
// Storage-specific invariants (PGResource) are checked inside
// ConfigCoreModule.Provide, not here.
//
// ref: kubernetes/kubernetes cmd/kube-apiserver/app/options/validation.go —
// validates all fields before any component is constructed.
func (d *SharedDeps) Validate() error {
	if d == nil {
		return errcode.New(errcode.ErrValidationFailed, "SharedDeps: nil receiver")
	}
	errs := d.validateCore()
	errs = append(errs, d.validateControlPlane()...)
	return errors.Join(errs...)
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
	if d.CursorCodecs.audit == nil {
		missing("CursorCodecs.audit")
	}
	if d.CursorCodecs.config == nil {
		missing("CursorCodecs.config")
	}
	if len(d.HMACKey) == 0 {
		missing("HMACKey")
	}
	if d.EventBus == nil {
		missing("EventBus")
	}
	if d.Topology.StorageBackend == "postgres" && os.Getenv("GOCELL_KEY_PROVIDER") == "" {
		errs = append(errs, errcode.New(errcode.ErrValidationFailed,
			"SharedDeps: GOCELL_KEY_PROVIDER must be set when StorageBackend=postgres "+
				"(defense-in-depth; config-core module also validates this)"))
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
	if d.VerboseToken == "" {
		errs = append(errs, errcode.New(errcode.ErrValidationFailed,
			"GOCELL_READYZ_VERBOSE_TOKEN must be set in adapter mode \"real\" "+
				"to prevent anonymous topology exposure via /readyz?verbose"))
	}
	if d.MetricsToken == "" {
		errs = append(errs, errcode.New(errcode.ErrValidationFailed,
			"GOCELL_METRICS_TOKEN must be set in adapter mode \"real\" "+
				"to prevent anonymous /metrics exposure; scrapers must send X-Metrics-Token header"))
	}
	if d.InternalGuard == nil {
		errs = append(errs, errcode.New(errcode.ErrValidationFailed,
			"GOCELL_SERVICE_SECRET must be set in adapter mode \"real\" "+
				"to protect /internal/v1/*"))
	}
	return errs
}

// LoadSharedDepsFromEnv reads all environment variables and builds a fully
// populated SharedDeps for cross-cutting concerns. Cell-specific dependencies
// are constructed in each CellModule.Provide.
//
// ref: go-zero serviceconf.MustLoad — single parse-validate call at startup.
func LoadSharedDepsFromEnv(ctx context.Context) (*SharedDeps, error) {
	topo, err := bootstrap.TopologyFromEnv()
	if err != nil {
		return nil, err
	}
	adapterMode := topo.AdapterMode

	hmacKey, err := loadSecret("GOCELL_HMAC_KEY", "dev-hmac-key-replace-in-prod!!!!", adapterMode)
	if err != nil {
		return nil, fmt.Errorf("HMAC key: %w", err)
	}
	if err := rejectDemoKey(adapterMode, "GOCELL_HMAC_KEY", hmacKey); err != nil {
		return nil, err
	}

	jwt, err := buildJWTDeps(adapterMode)
	if err != nil {
		return nil, err
	}

	codecs, err := loadAllCursorCodecs(adapterMode)
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
	if verboseToken == "" && !topo.RequireProductionControlPlane() {
		slog.Warn("GOCELL_READYZ_VERBOSE_TOKEN not set; /readyz?verbose exposes internal topology without authentication (dev mode only)")
	}

	metricsToken := os.Getenv("GOCELL_METRICS_TOKEN")
	metricsHandler, err := buildMetricsHandler(metricsToken, ps.registry)
	if err != nil {
		return nil, err
	}

	slog.Info("adapter mode",
		slog.String("requested", adapterMode),
		slog.String("effective", topo.AdapterInfo()["mode"]))

	deps := &SharedDeps{
		Topology:       topo,
		JWTDeps:        jwt,
		PromStack:      ps,
		CursorCodecs:   codecs,
		HMACKey:        hmacKey,
		EventBus:       eb,
		InternalGuard:  internalGuard,
		MetricsToken:   metricsToken,
		VerboseToken:   verboseToken,
		metricsHandler: metricsHandler,
	}

	if err := deps.Validate(); err != nil {
		return nil, err
	}
	return deps, nil
}
