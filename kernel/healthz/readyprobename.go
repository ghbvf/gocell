package healthz

// ReadyProbeName is the named string type for adapter dependency-availability
// readiness-probe names (postgres_ready, redis_ready, s3_ready, rabbitmq_ready,
// vault_transit_ready, oidc_ready, websocket_hub_ready, …). Each owning adapter
// declares its probe name as a ReadyProbeName-typed const at its own package
// declaration site; the construction sites — a lifecycle.ManagedResource
// Checkers() map key (via string(<const>)) and adapterutil.HealthToCheckers's
// name argument — reference those consts rather than bare string literals.
//
// This makes probe-name drift unexpressible in static analysis: a bare literal
// at a construction site, or a const declared outside the sanctioned package
// set, fails archtest OPS-CONTRACT-STRING-FUNNEL-01 (the same string-typed
// concept funnel mechanism as kernel/governance.RuleCode). The full symbol
// inventory, double-lock rationale, and blind-spot self-checks live in that
// archtest's package godoc — see tools/archtest/ops_contract_string_funnel_test.go.
//
// Scope: ReadyProbeName covers ONLY adapter dependency-availability "_ready"
// probes. Framework probes registered through NewProbe / bootstrap.WithHealthChecker
// (config_watcher, outbox_failopen_rate_<cell>, …) and runtime/outbox.Relay
// budget keys (outbox-relay-poll/-reclaim/-cleanup) are intentionally NOT
// ReadyProbeName and remain bare strings. Cell-level repo probes
// (cells/*/healthz_gen.go) are funneled separately via the cellgen-generated
// RegisterRepoReady helper.
//
// There is deliberately NO runtime Validate() method: probe-name shape policy
// (snake_case + _ready suffix) is enforced statically by archtest, not at
// runtime — adding a runtime regex check would create a parallel governance
// surface (see Probe.Name godoc and .claude/rules/gocell/observability.md).
type ReadyProbeName string
