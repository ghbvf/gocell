#!/usr/bin/env bash
# verify-bucket: scaffold
# verify-unconditional-skip rejects test files whose t.Skip is unconditional —
# any blanket skip is a hidden disabled test and must be either deleted or
# guarded with a runtime predicate.
#
# Multi-module (#1556): examples/* are their own go.work modules, so a single
# root `gocell check unconditional-skip ./...` stops at the nested-module
# boundary and would SILENTLY stop scanning example test files. Build the gocell
# binary once, scan the root module from the repo root, then scan each satellite
# module from inside it with GOWORK=off — the workspace-mode combined load
# (packages.Load LoadAllSyntax across modules) trips a cross-module go.sum gap
# because go.work.sum is intentionally gitignored, whereas GOWORK=off resolves
# against each module's own complete go.sum. Module enumeration is the same
# go.work funnel (hack/lib/modules.sh) the build/test gates use.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

# shellcheck source=lib/util.sh
source hack/lib/util.sh
# shellcheck source=lib/modules.sh
source hack/lib/modules.sh

bindir="$(mktemp -d)"
trap 'rm -rf "${bindir}"' EXIT
gocell_bin="${bindir}/gocell"
go build -o "${gocell_bin}" ./cmd/gocell

# Root module: examples/* are their own go.work satellites (#1556, incl. demo
# since #1644), naturally excluded by the ./... module boundary here and scanned
# in the GOWORK=off satellite loop below.
"${gocell_bin}" check unconditional-skip ./...

# Satellite modules: enumerate from the go.work funnel and scan each from inside
# it with GOWORK=off so packages.Load resolves against that module's own go.sum.
# Command substitution (not `while read < <(...)`) so a path-validation failure
# in the funnel propagates under `set -e`.
module_dirs_raw="$(gocell::modules::dirs)"
while IFS= read -r dir; do
    [[ -n "${dir}" ]] || continue
    [[ "${dir}" == "." ]] && continue
    gocell::log::status "Checking unconditional skips (GOWORK=off): ${dir}"
    ( cd "${dir}" && GOWORK=off "${gocell_bin}" check unconditional-skip ./... )
done <<< "${module_dirs_raw}"
