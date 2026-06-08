# Workspace Development Guide

GoCell uses a `go.work`-based workspace foundation committed to the repository.
This guide explains what that means today, why it was done, and how to extend it
when new modules are added in the future.

## Current state

GoCell is a committed multi-module Go workspace. The root module remains
`github.com/ghbvf/gocell`, and `go.work` also lists first-party satellite
modules for composition bundles, examples, and adapter-consuming test surfaces.

```
go.work                         # committed workspace membership
go.mod                          # module github.com/ghbvf/gocell  (core)
cellmodules/go.mod              # module github.com/ghbvf/gocell/cellmodules
cmd/corebundle/go.mod           # module github.com/ghbvf/gocell/cmd/corebundle
cmd/gocell/go.mod               # module github.com/ghbvf/gocell/cmd/gocell
examples/*/go.mod               # example satellite modules
tests/integration/go.mod        # adapter-consuming integration-test satellite
tests/testutil/pgshare/go.mod   # compatibility testutil satellite
```

The test satellites are deliberate: they may depend on adapter packages without
forcing the root module to retain adapter `replace` entries. Root framework tests
must stay adapter-free; `hack/verify-root-no-adapter-import.sh` guards that
boundary.

The ~117 other `go.mod` files tracked under `tools/*/testdata/` are
**archtest/depgraph fixture-isolation modules**, not workspace members. They are
not listed in `go.work` and are not built during normal workspace traversal.

## Why `go.work` is committed (non-obvious)

The Go reference says workspaces are for "modules in a repository developed
exclusively with each other but not together with external modules"
(https://go.dev/ref/mod#workspaces). GoCell fits that description exactly: every
module in this repository is consumed only within the repository; there are no
external consumers.

For contributors, the committed workspace lets local commands resolve unpublished
first-party modules together. Release-consistency gates still build and test each
member with `GOWORK=off`, so each module must keep its own `go.mod` / `go.sum`
complete and reproducible.

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
`assemblies/*/assembly.yaml` are deleted), `gocell validate` **fails with an
error** — manifest mode is fail-closed on zero matches for explicitly-declared
`includes:` patterns (error is roughly: "manifest module include patterns for
kind=<kind> matched zero files"). You must remove the corresponding `includes:`
entry from `.gocell/manifest.yaml` to reflect the intentional removal; the
manifest will not silently produce an empty set.

## How to add a module

When a new module is ready:

1. Add `use ./newmod` to `go.work`.
2. Add a `modules:` entry to `.gocell/manifest.yaml` with the appropriate
   `includes:` for that module's cells / slices / contracts, if it owns
   metadata.
3. Add the module's own `go.mod`.

The `hack/verify-workspace.sh` gate (auto-discovered by `make verify` via glob)
enumerates workspace modules from `go.work` using `hack/lib/modules.sh` — which
calls `go work edit -json` and extracts `.Use[].DiskPath`. Adding `use ./newmod`
to `go.work` is sufficient for the new module to be picked up automatically;
`hack/lib/modules.sh` must not be edited to hardcode module paths.

After running `go work sync` with a second module, a local `go.work.sum` may be
generated. It is gitignored by design: cross-module sums are path-dependent on
the developer's local checkout and must not be committed. The CI drift check in
`hack/verify-workspace.sh` runs `go work sync` and diffs `go.work` **plus each
member module's `go.mod` / `go.sum`** (`go work sync` rewrites those too) — but
NOT `go.work.sum`, which stays gitignored (intentional).

## CI traversal chain

```
make verify
  └── hack/make-rules/verify.sh          (glob-discovers all hack/verify-*.sh)
        ├── hack/verify-workspace.sh          (go.work drift + per-module release build)
        ├── hack/verify-workspace-test.sh     (satellite module untagged tests)
        ├── hack/verify-root-no-adapter-import.sh
        ├── hack/verify-integration-lint.sh   (narrow integration-tag lint)
        └── hack/lib/modules.sh               (single-sourced from go.work; validated DiskPaths)
```

`make verify` uses a glob-discovery model: adding `hack/verify-workspace.sh` is
sufficient — no change to the driver script is needed.

`hack/verify-workspace.sh` builds each module with `GOWORK=off go -C "$dir" build
./...` so it resolves against the module's own pinned `go.mod` (release-consistent,
Plan D §5.6), not the workspace-stitched graph. `hack/verify-workspace-test.sh`
runs untagged tests for non-root workspace members through the same module
enumeration funnel. Tagged integration tests remain in the dedicated
service-bearing CI lane, while `hack/verify-integration-lint.sh` keeps the narrow
integration-only helper lint/dependency rules active at PR time.

Once multiple large modules exist and CI budget is a concern,
`VERIFY_SKIP=workspace make verify` skips the workspace build gate (the standard
make-verify skip mechanism). The `hack/lib/modules.sh` funnel fail-closed
validates every `go.work` DiskPath (rejects absolute / `..`-escaping / out-of-repo
paths) — see `MODULES-PATH-VALIDATION-01`.

## Reference

Full multi-module roadmap, Phase 1 schedule, and the decision not to create
real module splits before `core` v1.0 GA:
`docs/plans/product-roadmap/202604300950-plan-d-go-workspace-multimodule-migration.md`
(Plan D — the adopted multi-module roadmap; the real `mdm/` split is Phase 1 /
2027 Q1, this issue is only the foundation).
