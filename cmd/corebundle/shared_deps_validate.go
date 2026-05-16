package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

// Validate is the startup invariant check for all cross-cutting dependencies.
// Storage-specific invariants (PoolResource, cursor codecs, HMAC key) are checked
// inside the corresponding CellModule.Provide, not here.
//
// ref: kubernetes/kubernetes cmd/kube-apiserver/app/options/validation.go —
// validates all fields before any component is constructed.
func (d *SharedDeps) Validate() error {
	if d == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "SharedDeps: nil receiver")
	}
	errs := d.validateCore()
	errs = append(errs, d.validateVerboseEndpoint()...)
	errs = append(errs, d.validateHealthReachability()...)
	errs = append(errs, d.validateInternalListenerGuard()...)
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
	return []error{errcode.New(errcode.KindInternal, errcode.ErrControlplaneVerboseTokenMissing,
		"GOCELL_READYZ_VERBOSE_TOKEN must be set (or "+
			"GOCELL_READYZ_VERBOSE_DISABLED=1 to waive the verbose endpoint) "+
			"so /readyz?verbose is never anonymous")}
}

// validateCore collects missing-field errors for dependencies required in
// every topology.
func (d *SharedDeps) validateCore() []error {
	var errs []error
	missing := func(field string) {
		errs = append(errs, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"SharedDeps field must be set",
			errcode.WithInternal(fmt.Sprintf("field=%s", field))))
	}
	// Clock is the single root clock instance threaded through every adapter,
	// service, and middleware. Required: a nil here would surface as a deeper
	// runtime panic (kernel/clock.MustHaveClock or constructors that re-validate).
	// Validate at startup so misconfiguration fails before any subsystem starts.
	if d.Clock == nil {
		missing("Clock")
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
	if d.ConfigEventCollector == nil {
		missing("ConfigEventCollector")
	}
	if d.EventbusCacheCollector == nil {
		missing("EventbusCacheCollector")
	}
	if d.ConsumerClaimer == nil {
		missing("ConsumerClaimer")
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
		errs = append(errs, errcode.New(errcode.KindInternal, errcode.ErrControlplaneVerboseTokenMissing,
			"GOCELL_READYZ_VERBOSE_DISABLED=1 is not allowed in adapter mode "+
				"\"real\"; production must keep the token-gated verbose endpoint "+
				"available for on-call diagnostics"))
	}
	if d.VerboseToken == SampleVerbosePlaceholder {
		errs = append(errs, errcode.New(errcode.KindInternal, errcode.ErrControlplaneVerboseTokenSample,
			"GOCELL_READYZ_VERBOSE_TOKEN is set to the .env.example placeholder; a production deploy must mint its own high-entropy secret",
			errcode.WithInternal(fmt.Sprintf("placeholder=%q", SampleVerbosePlaceholder))))
	}
	if d.MetricsToken == "" {
		errs = append(errs, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"GOCELL_METRICS_TOKEN must be set in adapter mode \"real\" to "+
				"prevent anonymous /metrics exposure; scrapers must send "+
				"X-Metrics-Token header"))
	}
	if d.InternalGuard == nil {
		errs = append(errs, errcode.New(errcode.KindInternal, errcode.ErrControlplaneServiceSecretMissing,
			"GOCELL_SERVICE_SECRET must be set in adapter mode \"real\" to protect /internal/v1/*"))
	} else if ns := d.InternalGuard.NonceStore(); ns == nil {
		errs = append(errs, errcode.New(errcode.KindInternal, errcode.ErrControlplaneNonceStoreMissing,
			"internalGuard.nonceStore is nil; guard constructed without WithServiceTokenNonceStore"))
	} else if kind := ns.Kind(); kind == auth.NonceStoreKindNoop {
		errs = append(errs, errcode.New(errcode.KindInternal, errcode.ErrControlplaneNonceStoreMissing,
			"control-plane NonceStore must be a replay-safe implementation in "+
				"adapter mode \"real\"; NoopNonceStore detected — inject "+
				"InMemoryNonceStore (single pod) or a shared store (multi-pod) "+
				"via WithServiceTokenNonceStore"))
	} else if kind == auth.NonceStoreKindInMemory && !d.Topology.SinglePodReplayProtection && d.Topology.RequireProductionControlPlane() {
		slog.Warn("controlplane: in-memory nonce store rejected for multi-pod deployment",
			slog.String("nonce_store_kind", string(kind)),
			slog.String("hint", "set GOCELL_SINGLE_POD=1 for single-pod deployments or configure a distributed NonceStore"))
		errs = append(errs, errcode.New(errcode.KindInternal, errcode.ErrControlplaneNonceStoreMissing,
			"in-memory nonce store requires GOCELL_SINGLE_POD=1 "+
				"(single-pod deployments) or a distributed store via "+
				"WithServiceTokenNonceStore (multi-pod); refuse fail-open"))
	}
	if requiresDistributedReplay(d.Topology) && d.ConsumerClaimerKind != consumerClaimerKindDistributed {
		errs = append(errs, errcode.New(errcode.KindInternal, errcode.ErrControlplaneClaimerNotDistributed,
			"ERR_CONTROLPLANE_CLAIMER_NOT_DISTRIBUTED: real multi-pod "+
				"deployments require Redis-backed outbox idempotency claimer; "+
				"set GOCELL_REDIS_ADDR or run with GOCELL_SINGLE_POD=1"))
	}
	return errs
}

func (d *SharedDeps) validateInternalListenerGuard() []error {
	if d.InternalHTTPAddr == "" {
		return []error{errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"SharedDeps.InternalHTTPAddr must be set; the internal listener is always enabled and protected by GOCELL_SERVICE_SECRET")}
	}
	if d.InternalGuard != nil {
		return nil
	}
	return []error{errcode.New(errcode.KindInternal, errcode.ErrControlplaneServiceSecretMissing,
		"SharedDeps.InternalGuard must be set to protect /internal/v1/*; set GOCELL_SERVICE_SECRET")}
}

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
