# Workspace Development Guide

GoCell uses a `go.work`-based workspace foundation committed to the repository.
This guide explains what that means today, why it was done, and how to extend it
when new modules are added in the future.

## Current state

Single Go module: `github.com/ghbvf/gocell`, rooted at the repository top level.
The `go.work` file currently contains only `use .` — one module, one workspace.

```
go.work          # committed; contains: use .
go.mod           # module github.com/ghbvf/gocell  (core)
```

The ~117 other `go.mod` files tracked under `tools/*/testdata/` are
**archtest/depgraph fixture-isolation modules**, not workspace members. They are
not listed in `go.work` and are not built during normal workspace traversal.

## Why `go.work` is committed (non-obvious)

The Go reference says workspaces are for "modules in a repository developed
exclusively with each other but not together with external modules"
(https://go.dev/ref/mod#workspaces). GoCell fits that description exactly: every
module in this repository is consumed only within the repository; there are no
external consumers.

For a single `use .` entry the dependency-resolution behavior is **identical** to
having no `go.work` — the workspace adds zero semantic change to today's build.
The value is establishing the **extension point**: when `mdm/` or `zerotrust/`
modules are added (Phase 1 / 2027 Q1 per Plan D), the CI traversal and module
enumeration machinery is already in place; no retroactive rewiring is needed.

`go.work.sum` is gitignored. A single-module workspace produces no cross-module
sum entries, so the file stays empty and is correctly excluded.

## The `.gocell/manifest.yaml` role

`.gocell/manifest.yaml` declares the repo's metadata footprint for `gocell`
tooling. When present, `kernel/metadata/locator.go` auto-detects it and switches
all `gocell` commands to manifest mode.

The current manifest uses explicit `includes:` patterns that cover both the
top-level module **and** `examples/*/...`, keeping the governance scope identical
to what conventional auto-detection would find. An equivalence test
(`kernel/metadata/locator_equivalence_test.go`) guards this: if the two modes
diverge, CI fails.

**Fail-closed caveat**: if the last top-level file of a kind is removed (e.g. all
`assemblies/*/assembly.yaml` are deleted), the manifest's `includes:` entry must
be updated. Conventional mode would discover zero matches silently; manifest mode
makes the scope explicit and therefore visible to reviewers.

## How to add a future module

When a new module (e.g. `mdm/`) is ready:

1. Add `use ./newmod` to `go.work`.
2. Add a `modules:` entry to `.gocell/manifest.yaml` with the appropriate
   `includes:` for that module's cells / slices / contracts.
3. Add the module's own `go.mod`.

The `hack/verify-workspace.sh` gate (auto-discovered by `make verify` via glob)
enumerates workspace modules from `go.work` using `hack/lib/modules.sh` — which
calls `go work edit -json` and extracts `.Use[].DiskPath`. Adding `use ./newmod`
to `go.work` is sufficient for the new module to be picked up automatically;
`hack/lib/modules.sh` must not be edited to hardcode module paths.

## CI traversal chain

```
make verify
  └── hack/make-rules/verify.sh          (glob-discovers all hack/verify-*.sh)
        └── hack/verify-workspace.sh     (when present; enumerates + builds modules)
              └── hack/lib/modules.sh    (single-sourced from go.work)
```

`make verify` uses a glob-discovery model: adding `hack/verify-workspace.sh` is
sufficient — no change to the driver script is needed.

## Reference

Full multi-module roadmap, Phase 1 schedule, and the decision not to create
real module splits before `core` v1.0 GA:
`docs/plans/product-roadmap/202604300950-plan-d-go-workspace-multimodule-migration.md`
(Plan D — the adopted multi-module roadmap; the real `mdm/` split is Phase 1 /
2027 Q1, this issue is only the foundation).
