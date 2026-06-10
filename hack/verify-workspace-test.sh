#!/usr/bin/env bash
# verify-workspace-test runs `go test ./...` for every NON-root workspace member
# (the satellite modules). The root module's tests are already
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
# Only untagged tests run here — integration / examples_smoke / archtest tagged
# tests have their own lanes (_build-lint.yml integration-test + examples-smoke;
# hack/verify-archtest.sh + archtest-nightly.yml), which iterate this same
# modules.sh funnel or opt in via -tags. This matters for the tools module: when
# #1803 split tools/ into a workspace member, this gate's `go test ./...` started
# re-running the ~1200-test archtest leaf (+~4min/lane, defeating governance.yml's
# VERIFY_SKIP=archtest). The `//go:build archtest` leaf tag
# (ARCHTEST-LEAF-BUILD-TAG-01) keeps it compiled-out here — no special-casing in
# this script; the heavy suite simply cannot enter a bare `go test ./...`.

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
# silently "pass". That is legitimate only if no real in-repo satellite module
# exists. `./examples/x` (go.work DiskPath form) → `examples/x` so the membership
# set below compares apples-to-apples with the filesystem scan.
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
# silently from every gate iterating this funnel. Cross-check every real in-repo
# satellite go.mod against the workspace `use` set and fail on any missing member
# (mirrors the manifest ⊆ go.work reverse check in tools/workspace). Fixture
# modules under testdata are intentionally excluded: they are standalone broken or
# synthetic modules for tests, not production workspace members.
gocell::log::status "Discovering in-repo satellite modules on disk"
if ! disk_mods_raw="$(
    find . \
        -path './.git' -prune -o \
        -path './worktrees' -prune -o \
        -path '*/testdata/*' -prune -o \
        -name go.mod -print | sort
)"; then
    gocell::log::error "failed to discover go.mod files on disk"
    exit 1
fi
disk_satellite_mods=()
while IFS= read -r gomod; do
    [[ -n "${gomod}" ]] || continue
    mod="${gomod%/go.mod}"
    mod="${mod#./}"
    [[ "${mod}" == "." || "${mod}" == "go.mod" ]] && continue
    disk_satellite_mods+=("${mod}")
done <<< "${disk_mods_raw}"

missing_from_workspace=()
for mod in "${disk_satellite_mods[@]}"; do
    workspace_has "${mod}" || missing_from_workspace+=("${mod}")
done
if [[ ${#missing_from_workspace[@]} -gt 0 ]]; then
    gocell::log::error "satellite module(s) on disk but absent from go.work 'use' (workspace drift): ${missing_from_workspace[*]}"
    gocell::log::error "add each via 'go work use ./<dir>' so every gate iterating hack/lib/modules.sh covers it"
    exit 1
fi

if [[ ${#satellite_dirs[@]} -eq 0 ]]; then
    # Reached only when there are zero non-root workspace members AND zero
    # non-fixture satellite go.mod files on disk (the reverse check above would
    # have fired otherwise). Nothing to test.
    gocell::log::status "no satellite (non-root) workspace modules and no satellite go.mod on disk — nothing to test"
    exit 0
fi
gocell::log::status "Satellite modules: ${satellite_dirs[*]}"

# Test each satellite with GOWORK=off so it resolves against its own pinned
# go.mod (release-consistent), mirroring verify-workspace.sh's build traversal.
for dir in "${satellite_dirs[@]}"; do
    gocell::log::status "Testing module (GOWORK=off): ${dir}"
    # The tools/archtest leaf is compiled-out here by its `//go:build archtest`
    # tag (ARCHTEST-LEAF-BUILD-TAG-01) — no special-casing needed.
    if ! GOWORK=off go -C "${dir}" test ./... -count=1; then
        gocell::log::error "go test ./... failed in module '${dir}' (GOWORK=off)"
        exit 1
    fi
done

gocell::log::status "verify-workspace-test: all checks passed"
