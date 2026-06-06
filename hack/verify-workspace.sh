#!/usr/bin/env bash
# verify-workspace asserts the go.work workspace is consistent and every member
# module builds against its own pinned module graph (GOWORK=off). The
# module-enumeration funnel (hack/lib/modules.sh, sourced from go.work) is the
# single source for cross-module traversal. Per-module `go test` is intentionally
# NOT run here (see step d) — that would duplicate the build-test matrix and
# slow `make verify`; test traversal reuses the same funnel in CI / a future
# workspace-test gate.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

# shellcheck source=lib/util.sh
source hack/lib/util.sh
# shellcheck source=lib/modules.sh
source hack/lib/modules.sh

# (a) Assert go.work exists and parses (fast-fail before enumeration + sync).
gocell::log::status "Checking go.work exists and is valid"
if [[ ! -f go.work ]]; then
    gocell::log::error "go.work not found in repo root"
    exit 1
fi
if ! go work edit -json >/dev/null; then
    gocell::log::error "go work edit -json failed — go.work is malformed"
    exit 1
fi

# (b) Enumerate + validate workspace modules. Command substitution propagates a
# path-validation failure from the funnel (a `while read < <(...)` would swallow
# the non-zero return because the helper runs in a subshell).
gocell::log::status "Enumerating workspace modules"
if ! modules_raw="$(gocell::modules::dirs)"; then
    gocell::log::error "workspace module enumeration failed"
    exit 1
fi
module_dirs=()
while IFS= read -r dir; do
    [[ -n "${dir}" ]] && module_dirs+=("${dir}")
done <<< "${modules_raw}"

if [[ ${#module_dirs[@]} -eq 0 ]]; then
    gocell::log::error "no workspace modules enumerated — go.work has no 'use' entries"
    exit 1
fi
found_root=0
for dir in "${module_dirs[@]}"; do
    [[ "${dir}" == "." ]] && found_root=1
done
if [[ "${found_root}" -eq 0 ]]; then
    gocell::log::error "workspace module list does not contain '.' (root module) — anti-vacuity check failed"
    exit 1
fi
gocell::log::status "Workspace modules: ${module_dirs[*]}"

# (c) Drift check. `go work sync` rewrites the workspace build list back into
# go.work AND each member module's go.mod / go.sum, so diff all of them — a
# go.work-only diff would miss member go.mod/go.sum rewrites. go.work.sum is
# intentionally excluded (gitignored: cross-module sums are path-dependent; a
# single-module workspace produces none).
gocell::log::status "Running go work sync (drift check)"
drift_paths=(go.work)
for dir in "${module_dirs[@]}"; do
    drift_paths+=("${dir%/}/go.mod" "${dir%/}/go.sum")
done
go work sync
if ! git diff --exit-code -- "${drift_paths[@]}"; then
    gocell::log::error "workspace drift after 'go work sync' — commit the updated go.work / member go.mod / go.sum"
    exit 1
fi

# (d) Build each module with GOWORK=off so it resolves against its OWN pinned
# go.mod (release-consistent, Plan D §5.6), not the workspace-stitched graph.
# This is the cross-module BUILD traversal extension point; per-module `go test`
# is deliberately not run here (see file header).
build_out="$(mktemp -d)"
trap 'rm -rf "${build_out}"' EXIT
for dir in "${module_dirs[@]}"; do
    gocell::log::status "Building module (GOWORK=off): ${dir}"
    if ! GOWORK=off go -C "${dir}" build -o "${build_out}/" ./...; then
        gocell::log::error "go build ./... failed in module '${dir}' (GOWORK=off)"
        exit 1
    fi
done

gocell::log::status "verify-workspace: all checks passed"
