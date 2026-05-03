// Package devtools provides framework-internal HTTP routes that expose
// project catalog metadata (cells, slices, contracts, journeys, assemblies,
// actors) plus optional cell-level and package-level dependency graphs to
// admin-authenticated clients.
//
// The catalog endpoint is wired by bootstrap during phase5 alongside the
// health probes; it is NOT a Cell-owned route and intentionally has no
// contract.yaml — same precedent as runtime/http/health (framework
// introspection routes, FMT-18 exempt because the spec ID prefix
// "http.framework.devtools." identifies it as runtime-internal).
//
// Roadmap reference: docs/plans/202605011500-029-master-roadmap.md
//   - Track J · DevTools Platform (J1 PR-A37 + J2 absorb)
//
// ref: backstage/backstage packages/catalog-model wire format
package devtools
