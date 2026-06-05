#!/usr/bin/env bash
# verify-workspace-test runs `go test ./...` for every NON-root workspace member
# (the satellite modules — examples/* today). The root module's tests are already
# covered by the _build-lint.yml build-test matrix; running them here too would
# duplicate that load and slow `make verify`. This is the test-traversal half of
# the go.work multi-module gate, complementing hack/verify-workspace.sh (which
# does the per-module build). Both source the SAME enumeration funnel
# (hack/lib/modules.sh, derived from go.work via `go work edit -json`) so a module
# added to go.work is automatically covered with zero hand-maintained list.
#
# GOWORK=off so each satellite resolves against its OWN pinned go.mod (the local
# `replace github.com/ghbvf/gocell => ../../` redirects the unpublished core to
# the repo root), matching the release-consistency build in verify-workspace.sh.
# Only untagged tests run here — integration / examples_smoke tagged tests have
# their own service-bearing CI lanes (_build-lint.yml integration-test +
# examples-smoke), which iterate this same modules.sh funnel.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

# shellcheck source=lib/util.sh
source hack/lib/util.sh
# shellcheck source=lib/modules.sh
source hack/lib/modules.sh

# Enumerate + validate workspace modules from go.work (single source of truth).
# Command substitution propagates a path-validation failure from the funnel.
gocell::log::status "Enumerating workspace modules"
if ! modules_raw="$(gocell::modules::dirs)"; then
    gocell::log::error "workspace module enumeration failed"
    exit 1
fi
module_dirs=()
while IFS= read -r dir; do
    [[ -n "${dir}" ]] && module_dirs+=("${dir}")
done <<< "${modules_raw}"

# Anti-vacuity: a workspace with only the root module would test nothing here and
# silently "pass". That is a legitimate state TODAY only if no satellite exists;
# once examples/* are extracted there is always ≥1 non-root member, so an empty
# satellite set after extraction signals go.work drift rather than a real pass.
# `./examples/x` (go.work DiskPath form) → `examples/x` so the membership set
# below compares apples-to-apples with the filesystem glob.
satellite_dirs=()
for dir in "${module_dirs[@]}"; do
    [[ "${dir}" == "." ]] && continue
    satellite_dirs+=("${dir}")
done

# workspace_has reports whether repo-relative dir $1 is a go.work satellite
# member. Normalizes the go.work DiskPath form (./examples/x) to the filesystem
# glob form (examples/x) so the two ground truths compare apples-to-apples.
# Linear scan over a ≤handful-of-entries set — kept assoc-array-free so the
# script runs under macOS system bash 3.2 as well as CI bash 5.
workspace_has() {
    local want="$1" have
    for have in "${satellite_dirs[@]}"; do
        [[ "${have#./}" == "${want}" ]] && return 0
    done
    return 1
}

# Reverse closure (disk → go.work): the filesystem is the ground truth for "a
# satellite module exists". The go.work-derived set above only knows about
# modules someone remembered to `go work use`; it CANNOT catch a module that
# exists on disk but was never added to go.work — that module then vanishes
# silently from every gate iterating this funnel. So cross-check every
# examples/*/go.mod against the workspace `use` set and fail on any that is
# missing (mirrors the manifest ⊆ go.work reverse check in tools/workspace).
# This also subsumes the old empty-set guard: with ≥1 examples/*/go.mod on
# disk, an empty satellite set fails here as N missing modules.
shopt -s nullglob
disk_example_mods=()
for gomod in examples/*/go.mod; do
    disk_example_mods+=("$(dirname "${gomod}")")
done
shopt -u nullglob

missing_from_workspace=()
for mod in "${disk_example_mods[@]}"; do
    workspace_has "${mod}" || missing_from_workspace+=("${mod}")
done
if [[ ${#missing_from_workspace[@]} -gt 0 ]]; then
    gocell::log::error "satellite example module(s) on disk but absent from go.work 'use' (workspace drift): ${missing_from_workspace[*]}"
    gocell::log::error "add each via 'go work use ./<dir>' so every gate iterating hack/lib/modules.sh covers it"
    exit 1
fi

if [[ ${#satellite_dirs[@]} -eq 0 ]]; then
    # Reached only in the genuine pre-extraction state: zero non-root workspace
    # members AND zero examples/*/go.mod on disk (the reverse check above would
    # have fired otherwise). Nothing to test.
    gocell::log::status "no satellite (non-root) workspace modules and no examples/*/go.mod on disk — nothing to test"
    exit 0
fi
gocell::log::status "Satellite modules: ${satellite_dirs[*]}"

# Test each satellite with GOWORK=off so it resolves against its own pinned
# go.mod (release-consistent), mirroring verify-workspace.sh's build traversal.
for dir in "${satellite_dirs[@]}"; do
    gocell::log::status "Testing module (GOWORK=off): ${dir}"
    if ! GOWORK=off go -C "${dir}" test ./... -count=1; then
        gocell::log::error "go test ./... failed in module '${dir}' (GOWORK=off)"
        exit 1
    fi
done

gocell::log::status "verify-workspace-test: all checks passed"
