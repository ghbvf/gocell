// Package devtools provides framework-internal HTTP routes that expose
// project catalog metadata (cells, slices, contracts, journeys, assemblies,
// actors) plus optional cell-level and package-level dependency graphs to
// admin-authenticated clients.
//
// The catalog endpoint is wired by bootstrap during phase5 alongside the
// health probes; it is NOT a Cell-owned route and intentionally has no
// contract.yaml — same precedent as runtime/http/health. The
// "http.framework.devtools." spec ID prefix marks it as runtime-internal,
// distinguishing it from cell-owned routes.
//
// Wire format note: catalog responses use the Backstage Catalog Entity
// envelope at top level (apiVersion/kind/metadata/spec). They do NOT wrap
// in {"data": ...} per api-versioning.md — that envelope rule applies to
// cell-owned business routes; framework-internal routes (this package +
// runtime/http/health) follow their own wire formats.
//
// Roadmap reference: docs/plans/202605011500-029-master-roadmap.md
//   - Track J · DevTools Platform (J1 PR-A37 + J2 absorb)
//
// ref: backstage/backstage packages/catalog-model wire format
package devtools
