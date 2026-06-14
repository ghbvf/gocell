#!/usr/bin/env bash
# verify-bucket: scaffold
# verify-unconditional-skip rejects test files whose t.Skip is unconditional —
# any blanket skip is a hidden disabled test and must be either deleted or
# guarded with a runtime predicate.
#
# Multi-module (#1556): examples/* are their own go.work modules; since #1565 the
# former root module (kernel/runtime/pkg) is itself the go.work `framework` member
# and the repo root is a pure go.work workspace with NO module. A root
# `gocell check unconditional-skip ./...` would therefore match no module at all,
# so there is no root scan — every module (framework included) is scanned from
# inside it with GOWORK=off, the workspace-mode combined load (packages.Load
# LoadAllSyntax across modules) trips a cross-module go.sum gap because go.work.sum
# is intentionally gitignored, whereas GOWORK=off resolves against each module's
# own complete go.sum. Module enumeration is the same go.work funnel
# (hack/lib/modules.sh) the build/test gates use.

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

# Every go.work module (framework — the former root module — plus corecells,
# adapters/*, cmd/*, examples/*, tests, tools) is scanned from inside it with
# GOWORK=off so packages.Load resolves against that module's own go.sum. There is
# no separate root scan: the repo root is a pure go.work workspace (#1565) with no
# module of its own. Command substitution (not `while read < <(...)`) so a
# path-validation failure in the funnel propagates under `set -e`.
module_dirs_raw="$(gocell::modules::dirs)"
while IFS= read -r dir; do
    [[ -n "${dir}" ]] || continue
    gocell::log::status "Checking unconditional skips (GOWORK=off): ${dir}"
    ( cd "${dir}" && GOWORK=off "${gocell_bin}" check unconditional-skip ./... )
done <<< "${module_dirs_raw}"
