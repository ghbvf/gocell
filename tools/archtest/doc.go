// Package archtest enforces Go source-level import layering rules for the GoCell architecture.
//
// This complements kernel/governance which validates metadata-level dependencies
// (DEP-01 to DEP-03: cell ownership, cycle detection, L0 co-location) from YAML files.
// archtest operates on the typed Go package graph supplied by tools/depgraph
// (single packages.Load shared across LAYER-05/05T/06/06T/07/08/09/09T/10) to
// catch violations that metadata validation cannot see.
//
// Rules enforced (from CLAUDE.md):
//
//	LAYER-01: kernel/ may only import stdlib, pkg/, and kernel/ (allow-list)
//	          [moved to depguard (.golangci.yml linters.settings.depguard.rules)]
//	LAYER-02: cells/ must not import adapters/
//	          [moved to depguard (.golangci.yml linters.settings.depguard.rules)]
//	LAYER-03: runtime/ must not import cells/ or adapters/
//	          [moved to depguard (.golangci.yml linters.settings.depguard.rules)]
//	LAYER-04: adapters/ must not import cells/, cmd/, or examples/
//	          [moved to depguard (.golangci.yml linters.settings.depguard.rules)]
//	LAYER-05:  cells/A must not directly import cells/B/internal/ (cross-cell isolation)
//	LAYER-05T: cells/A must not transitively import cells/B/internal/ via any
//	           production-edge closure (T = transitive; depgraph.TransitiveImports)
//	LAYER-06:  cell-owned public subpackages (see cellOwnedSubpackages) may
//	           only be imported by their owning cell, cmd/, or examples/
//	LAYER-06T: same as LAYER-06 but checked against the transitive closure
//	LAYER-07:  cells/ must not import runtime/http/router directly
//	LAYER-08:  the legacy cell-level HTTP route registrar interface must remain
//	           absent — enforced at the type level by walking each module
//	           package's types.Scope() for a top-level TypeName matching the
//	           legacy name. String literals in comments / struct tags are
//	           accepted (type-level scope is precise where text scanning
//	           over-matches into prose)
//	LAYER-09:  cells/A must not directly import cells/B/events
//	LAYER-09T: cells/A must not transitively import cells/B/events
//	LAYER-10:  cells/<cell> root package exported APIs must not expose concrete
//	           adapter/driver types
//	PGQUERY-01: PostgreSQL SQL builder/keyset helpers must live in pkg/pgquery;
//	            pkg/query remains limited to generic pagination, cursor,
//	            runmode, and in-memory pagination helpers
//
// # Themed invariant files
//
// Beyond the LAYER-* / PGQUERY-01 rules above, this package hosts ~70
// additional invariant gates organized into per-theme `*_invariants_test.go`
// files. Each file contains the Test functions for one rule cluster; every
// rule has a `// INVARIANT: {ID}` anchor immediately above its Test func so
// `grep "INVARIANT: <ID>"` jumps to the asserting code, source code, ADR,
// and inventory in one shot.
//
//	assembly_invariants_test.go    ASSEMBLY-* / ASSEMBLYREF-*
//	clock_invariants_test.go       CLOCK-* / KERNEL-CLOCK-* / PROD-CLOCK-*
//	codegen_invariants_test.go     CODEGEN-* / SPEC-GEN-*
//	errcode_invariants_test.go     ERRCODE-KIND-LITERAL / MESSAGE-CONST-LITERAL /
//	                               ERROR-FIRST-* / DETAILS-SLOG-ATTR / EXPORTED-ERROR-NEW
//	handler_invariants_test.go     HANDLER-*
//	httputil_invariants_test.go    HTTPUTIL-*
//	outbox_invariants_test.go      OUTBOX-*
//	panic_invariants_test.go       PANIC-*
//	prod_invariants_test.go        PROD-DURATION-CONST-01
//	refresh_invariants_test.go     REFRESH-*
//	rmq_invariants_test.go         RMQ-*
//
// Single-rule files retain the `{rule}_test.go` naming (e.g.
// `adapter_returns_declared_types_test.go`); they convert to
// `{theme}_invariants_test.go` once the theme accumulates ≥ 3 rules — see
// CLAUDE.md `## 新增 invariant 决策原则` for the file-naming branch.
//
// Inventory of every invariant + its disposition:
// docs/audit/archtest-inventory.md.
package archtest
