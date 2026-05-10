// Package cellvocab is the single source of truth for the GoCell metadata
// vocabulary — the typed enums (CellType, ContractKind, ContractRole,
// Lifecycle, Level), their parsers/predicates, and the canonical consistency
// level ordering.
//
// cellvocab is a leaf with zero kernel→kernel dependencies. It is consumed by:
//
//   - kernel/cell — uses the vocabulary in BaseCell construction, registry,
//     consistency-mode resolution, and lifecycle hooks.
//   - kernel/governance — references vocabulary types in lint/validation rules
//     (cellvocab.ContractKind/Role/Lifecycle/CellType + their parsers); after
//     the G-04 refactor governance no longer imports kernel/cell.
//   - kernel/metadata — uses cellvocab.Levels/Rank/At for assembly derivation
//     before any kernel/cell type is bound; after G-04 metadata no longer
//     reaches into kernel/cell/levelrank (the levelrank sub-package was
//     absorbed into cellvocab).
//   - runtime/* — pulls vocabulary enums for HTTP/auth/router/eventrouter.
//
// History (G-04, 2026-05-10):
//
//   - kernel/cell.{CellType,ContractKind,ContractRole,Lifecycle,Level} +
//     all Parse* functions migrated to this leaf to break the
//     governance→cell and metadata→(cell/levelrank) reverse edges.
//   - kernel/cell/levelrank/ folded into kernel/cellvocab/levels.go so the
//     ordered string list and the typed enum live next to each other.
//   - kernel/cell.InternalPathPrefix const moved here (referenced by both
//     cell.AuthRouteMeta.IsInternal and contractspec.Validate).
//
// Boundary: cellvocab depends only on stdlib + pkg/errcode. It must not
// import any other kernel sub-module. Adding such an import would re-create
// the dependency cycle that this package was extracted to break.
package cellvocab
