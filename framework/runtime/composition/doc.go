// Package composition provides the public Composition Root abstraction for
// GoCell assemblies.
//
// # Overview
//
// A Composition Root is the single place in an application where all objects
// are assembled together.  Before this package existed, the assembly logic
// was trapped inside cmd/corebundle (package main, not importable from tests
// or alternative entry points).
//
// Package composition exposes three public types:
//
//   - [CellModule] — the per-Cell wiring contract.  Each Cell declares itself
//     to [Builder.Build] via one CellModule implementation.
//   - [SharedDeps] — cross-cutting dependencies shared by every CellModule.
//     Contains Clock, JWT, metrics, event bus and capability providers.
//   - [Builder] / [App] — assembly entry point and runner.
//
// # Layering constraint
//
// runtime/composition is governed by the runtime-isolation depguard rule in
// .golangci.yml: it may import only stdlib, kernel/…, pkg/…, and the curated
// runtime/… + external allowlist.  In particular:
//
//   - ZERO imports of adapters/…
//   - ZERO imports of github.com/prometheus/client_golang
//
// If the caller needs an adapter-specific type (e.g. *promadapter.MetricProvider),
// it must be assigned to the appropriate kernel/runtime interface declared
// in SharedDeps (MetricsProvider kernel/observability/metrics.Provider).
//
// # AUTH-PLAN-04 constraint
//
// runtime/composition is forbidden from constructing auth plans
// (auth.NewAuthJWT / auth.NewAuthServiceToken / etc.).  Listener and auth
// wiring must be supplied by the caller via the [RuntimeOptionsFunc] passed to
// [Builder.Build].  The composition root (cmd/…) owns that responsibility.
//
// See [RuntimeOptionsFunc] and examples/corebundlestarter for a runnable
// example of supplying listener/auth wiring.
//
// ref: uber-go/fx fx.App — single assembly entry point.
// ref: kubernetes-sigs/controller-runtime pkg/manager — Manager pattern.
package composition
