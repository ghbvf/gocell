# ADR: assembly.yaml cross-module cell references + build-time cellID closed-set

- Status: Accepted
- Date: 2026-06-03
- Issues: #1086 (M5), #1093 M12a (bundled), epic #1081
- Supersedes/relates: depends on #1082 (M1 metadata locator), #1083 (M2 codegen module path)

## Context

GoCell only supported monorepo single-module development. `gocell generate
assembly` built each cellmodule import as `<current module>/cellmodules/<cellID>`
(one hardcoded module), and `assembly.yaml.cells` was a bare `[]string`. A cell
developed in an independent Go module (epic #1081: Operator-SDK / Workspace
modes) could not be referenced by an assembly.

M5 (#1086) makes `assembly.yaml` able to declare *which module* each cell comes
from. M12a (#1093, bundled here) freezes the resulting cell-id set into a
build-time closed set with fail-fast rejection of out-of-set cell identities.

## Decision

### D1 — `cells:` is a scalar-or-object union; per-cell `module`, no `version`

`assembly.yaml.cells[]` accepts two forms:

```yaml
cells:
  - configcore                               # same-module shorthand
  - id: payment
    module: github.com/acme/payment-cell     # cross-module (developed elsewhere)
```

`module` omitted = the assembly's own Go module. The Go type is
`metadata.AssemblyCellRef{ID, Module}` with a custom `UnmarshalYAML` (the
mapping branch enforces the `{id, module}` key set itself, since `KnownFields`
does not propagate into a custom Unmarshaler).

**No `version:` field, no pull semantics.** Version resolution is `go.mod` +
`go.sum` (MVS + hash). Duplicating it in a weaker YAML would create a
second-source-of-truth that necessarily drifts. This aligns with the
external-as-top composition frameworks (uber-go/fx, controller-runtime,
Backstage): the composition layer carries *identity only*; versions are the
package manager's job. (If platform-aggregates-third-party-cell-by-version is
ever needed, the correct analog is xcaddy `--with mod@ver` → go.mod require →
`go build` — a separate `gocell-build` tool, not assembly.yaml.) See #1086
comment "OSS 对标重排 (2026-06-02)".

### D2 — codegen is multi-module via the per-cell module funnel

`gocell generate assembly` resolves the import prefix per cell from
`AssemblyCellRef.Module` (empty → the assembly's own module) through a single
funnel, `kernel/assembly.cellModuleImportPath`. Cross-module assembly
composition requires `build.compositionAPI: true` (the `cellmodules/{cell}.Module()`
form, which carries a per-cell import path); the legacy local-CellModule-type
form has no import path and so fail-fasts on a cross-module ref.

**Scope (workspace-first):** external cell *metadata* (cell.yaml `requires` etc.)
is read through the existing M1 manifest locator (go.work multi-module local
discovery). Reading dependency cell.yaml from the Go module cache via
`go list -json` (Operator-SDK mode) is deferred until a real external module
exists (gated on M11 #1092); see Deferred.

### D3 — "module declared in go.mod" is enforced by the Go compiler, not governance

A referenced module that is not a declared `go.mod` require / `go.work` use makes
the generated `import "<module>/cellmodules/<id>"` unbuildable: `go build ./...`
fails. A `gocell validate` governance rule duplicating this would be a Medium
runtime check shadowing a Hard compiler check, and would force `golang.org/x/mod`
data into `kernel/` (a layering violation — kernel may only depend on stdlib +
pkg/ + yaml). We therefore do **not** add a metadata governance rule for module
declaration. The upstream regenerate-diff (`gocell verify codegen`) byte-locks
the generated import set to what the funnel produces from assembly.yaml.

### D4 — M12a: build-time cellID closed set in `composition.Builder.Build`

`composition.New(expectedCellIDs ...string)` seals the assembly's declared cell
set. `Build` validates a **bijection** before any module `Provide`: every
composed module ID ∈ set (no out-of-set cell), every declared cell provided by
some module (no missing cell), no duplicate module ID. Out-of-set cells are
**rejected outright, not degraded to the `_runtime` sentinel** — an external cell
registering a cellID outside the assembly is a configuration bug, and `_runtime`
is reserved for framework/unmatched traffic. Mirrors K8s `runtime.Scheme`:
registration-time enumeration + hard rejection of unregistered identities.

This **replaces** the hand-written `cmd/corebundle.assertModuleIDsMatch` (deleted
in this PR). The framework guard is inherited by every assembly **that composes
via `composition.Builder`** — today `cmd/corebundle` + `examples/corebundlestarter`
(compositionAPI form) and any future external one. The legacy-form example
assemblies (`examples/todoorder` / `iotdevice` / `orderfulfillment`, which use the
local-CellModule `generatedCellModules() []CellModule` form and do **not** go
through `composition.Builder`) retain their own per-assembly `assertModuleIDsMatch`;
migrating them to `composition.New` closed-set is deferred until they adopt the
compositionAPI/cellmodules form. The check is order-agnostic set membership
(slice order carries no runtime meaning — cross-module value handoff was removed
in #1423).

## Enforcement / AI-robust rating

| Carrier | Mechanism | Rating |
|---|---|---|
| module resolvability | `go build` of generated `cellmodules` import | **Hard** (compiler) |
| generated import ⊆ assembly.yaml | regenerate-diff byte-lock (modules_gen.go DO NOT EDIT) | **Hard** (codegen golden) |
| no rogue cellmodules import construction | archtest `ASSEMBLY-CROSS-MODULE-IMPORT-01` — `"/cellmodules/"` interpolation callsite-uniqueness ⊆ `cellModuleImportPath` body | **Hard** downstream (callsite/form uniqueness) + Hard upstream (codegen golden) |
| cellID closed set at compose time | `composition.Builder.Build` bijection guard (fail-closed, table-driven) | **Medium** (runtime invariant guard; cell IDs are runtime strings → Hard not reachable, same ceiling as `SAGA-CONSTRUCTOR-NIL-GUARD`) |
| AssemblyCellRef.ID typed-builder | `FIXTURE-CELLID-TYPED-BUILDER-01/A1` field position moved `AssemblyMeta.Cells` (slice elem) → `AssemblyCellRef.ID` (struct field) | unchanged rating (Hard downstream / upstream per its godoc) |

M12a's Medium is the honest ceiling, not a settle: `CellModule.ID()` is a runtime
string, so set membership is inherently a runtime guard.

## Deferred (backlog, not silent)

- **Operator-SDK module-cache metadata read** (`go list -json` to read a
  dependency module's cell.yaml): no in-repo consumer until examples/ split into
  modules (M11 #1092). Tracked as a backlog issue.
- **Scaffold `--module` flag** (cross-module assembly scaffolding): `gocell
  scaffold assembly` emits same-module string-form cells; cross-module entries
  are hand-authored. Backlog.
- **M12b** (metric-label closed-set defense at the write point +
  `CELL-ID-CLOSED-SET-01` archtest): mostly already present (`_runtime` sentinel +
  cardinality cap); remains in #1093.
- **CellMeta source-module tracking** (cross-check that assembly.yaml `module`
  matches the module a cell.yaml was discovered in): needs a `MetadataSource`
  module field; the compiler catches the non-existent-import case today. Backlog.

## References

- uber-go/fx module.go / app.go · kubernetes-sigs/controller-runtime manager.go ·
  operator-sdk init docs · helm.sh charts · backstage building-apps ·
  kubernetes/apimachinery scheme.go · prometheus/client_golang registry.go
  (#1086 + #1093 comments, 2026-06-02 OSS 对标).
