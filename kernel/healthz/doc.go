// Package healthz defines the kernel-level interface for readiness probes.
//
// Three concepts:
//
//   - Probe       — a named function returning healthy / degraded / down
//   - Aggregator  — write side (Register/Deregister) + read side (Evaluate)
//   - Snapshot    — immutable point-in-time aggregation result
//
// The interface lives in kernel/ so cells and adapters can declare probes
// without depending on a concrete implementation. The default in-memory
// implementation lives in runtime/observability/healthz; HTTP exposure lives
// in runtime/http/health.
//
// # Layering invariants
//
//   - kernel/healthz has zero runtime/ or adapters/ dependencies (stdlib + pkg/errcode).
//   - Implementations are registered through Aggregator.Register only — no
//     parallel back-channel maps. See HEALTHZ-WRITE-01.
//
// # INVARIANT: HEALTHZ-WRITE-01
//
// Production code in runtime/ and adapters/ must NOT register readiness
// probes by writing directly to a /healthz or /readyz HTTP handler. All
// probe registration funnels through Aggregator.Register, and the HTTP
// transport layer is the sole consumer of Snapshot wire serialization.
// archtest enforces:
//
//   - A1 path-literal funnel: ban (http|mux).HandleFunc(_, "/healthz"|"/readyz", _)
//     outside runtime/http/health
//   - A2 caller-identity allowlist: Aggregator.Register callsites limited to
//     the in-memory Aggregator helper, bootstrap drain, codegen cellgen output,
//     and adapter probe constructors
//   - A3 holder allowlist (Medium → backlog HEALTHZ-HOLDER-SEAL-01): structs
//     holding healthz.Aggregator are limited to the runtime aggregator impl
//     and runtime/http/health.Handler
//   - A4 reverse self-test: tools/archtest/testdata/healthz_violate/ fixture
//     proves A1/A2/A3 catch known violations
//
// # INVARIANT: HEALTHZ-TYPED-REGISTER-01
//
// Cells must NOT call Registry.Healthz() directly outside cellgen-generated
// healthz_gen.go files. Each cell receives a typed helper
// "<cellname>healthz.RegisterRepoReady(reg, prober)" from cellgen; cell_init.go,
// handler.go and other hand-written files must call that helper.
//
// ref: kubernetes/kubernetes staging/src/k8s.io/apiserver/pkg/server/healthz
// ref: spring-projects/spring-boot HealthIndicator
// ref: uber-go/fx lifecycle.go — write-side controller / read-side probe
package healthz
