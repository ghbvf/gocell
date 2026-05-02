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
package archtest
