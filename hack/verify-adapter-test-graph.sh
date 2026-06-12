#!/usr/bin/env bash
# verify-bucket: workspace
#
# INVARIANT: ADAPTER-MODULE-GRAPH-TEST-EDGE-01 (Medium): a test-only dependency
# must not leak into the published module graph of an adapter that never uses it.
#
# Background: per-adapter `go mod tidy` resolves the FULL (all-build-tags) import
# closure of every package an adapter imports, so a test-only backend reachable
# through a shared fixture package silently becomes an `// indirect` require in
# every consuming adapter's go.mod — visible to external consumers of that module
# even though no production (or even test) code in the adapter uses it. #1908 split
# the minio/rabbitmq testcontainer helpers out of `tests/testutil` so postgres/
# redis/otel/vault/mqtt stop inheriting them; #1909 gave vault an adapter-local
# recording metrics.Provider so it stops requiring `adapters/prometheus`. This gate
# locks both wins against regression: a future re-merge would re-introduce the edge
# and pass `verify-workspace.sh` (drift is self-consistent) — only an explicit
# forbidden-edge assertion catches it.
#
# Detection uses `go mod edit -json` (official tooling, not hand-rolled regex) and
# checks whether a forbidden module path appears as a require/replace in the target
# module's own go.mod. Anti-vacuity positive controls assert the REAL backend users
# still declare the module, proving the parser reads actual require data rather than
# passing because a go.mod was misread or empty.
#
# AI-robust grade: Medium. go.mod content is build metadata, not Go-type-expressible,
# so the Hard ceiling (violation un-expressible / compile-or-golden break) is
# unreachable here — a require line can always be hand-added; CI catches it.
#
# Blind spots (documented, not silently absent): all adapter modules declared in
# go.work are now auto-enumerated via gocell::modules::dirs, so adding a new adapter
# to go.work automatically brings it under the minio/rabbitmq forbid checks with zero
# hardcoded-list maintenance. Remaining blind spots: non-adapter modules (root,
# corecells, cellmodules, examples) are not covered; and a NEW heavy backend beyond
# minio/rabbitmq/prometheus would need a new forbid table entry. The detector is
# path-exact (quoted full module path), so a vanity-renamed fork of the same
# dependency would not be caught.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

# shellcheck source=lib/util.sh
source hack/lib/util.sh
# shellcheck source=lib/modules.sh
source hack/lib/modules.sh

fail=0

# mod_declares <module-dir> <module-path> — succeeds (0) when <module-path> appears
# as a require/replace in <module-dir>/go.mod. Fails fast if `go mod edit -json`
# errors (e.g. a mistyped module dir), so a broken probe can never read as "absent".
mod_declares() {
    local dir="$1" path="$2" json
    if ! json="$(go -C "${dir}" mod edit -json)"; then
        gocell::log::error "go mod edit -json failed in '${dir}' — cannot evaluate module graph"
        exit 1
    fi
    grep -qF -- "\"${path}\"" <<<"${json}"
}

# forbid <module-dir> <forbidden-path> — the edge MUST be absent.
forbid() {
    local dir="$1" path="$2"
    if mod_declares "${dir}" "${path}"; then
        gocell::log::error "${dir}/go.mod must NOT declare '${path}' — test-only edge leaked into the module graph (ADAPTER-MODULE-GRAPH-TEST-EDGE-01)"
        fail=1
    fi
}

# require_present <module-dir> <expected-path> — anti-vacuity positive control: a
# module that genuinely uses the backend MUST still declare it, proving the probe
# sees real require data.
require_present() {
    local dir="$1" path="$2"
    if ! mod_declares "${dir}" "${path}"; then
        gocell::log::error "anti-vacuity: ${dir}/go.mod is expected to declare '${path}' — detector sees no real require data, this gate may be vacuously green"
        fail=1
    fi
}

gocell::log::status "Checking adapter module graphs for leaked test-only edges"

readonly MINIO="github.com/testcontainers/testcontainers-go/modules/minio"
readonly RABBITMQ="github.com/testcontainers/testcontainers-go/modules/rabbitmq"

# #1908 — minio/rabbitmq testcontainer modules must not leak into any adapter that
# does not rightfully own that backend. Enumerate ALL adapter modules from the
# canonical workspace funnel (go.work via gocell::modules::dirs) so future adapters
# are covered automatically without extending this table.
if ! _all_modules="$(gocell::modules::dirs)"; then
    gocell::log::error "workspace module enumeration failed — cannot evaluate adapter module graphs"
    exit 1
fi
while IFS= read -r _mod; do
    # Keep only entries matching ./adapters/* (strip leading ./).
    [[ "${_mod}" == ./adapters/* ]] || continue
    dir="${_mod#./}"

    # Every adapter must NOT contain the sibling backend's testcontainer module,
    # unless it IS that backend's rightful owner.
    [[ "${dir}" == "adapters/s3" ]] || forbid "${dir}" "${MINIO}"
    [[ "${dir}" == "adapters/rabbitmq" ]] || forbid "${dir}" "${RABBITMQ}"
done <<< "${_all_modules}"

# #1909 — vault tests must not pull the prometheus adapter or its client into the
# vault module graph (replaced by an adapter-local recording metrics.Provider).
forbid adapters/vault "github.com/ghbvf/gocell/adapters/prometheus"
forbid adapters/vault "github.com/prometheus/client_golang"
forbid adapters/vault "github.com/prometheus/client_model"

# Anti-vacuity positive controls — the real backend users keep their direct require.
require_present adapters/s3 "${MINIO}"
require_present adapters/rabbitmq "${RABBITMQ}"
require_present adapters/prometheus "github.com/prometheus/client_golang"

if [[ "${fail}" -ne 0 ]]; then
    gocell::log::error "verify-adapter-test-graph: forbidden test-only module edge present (or positive control missing)"
    exit 1
fi

gocell::log::status "verify-adapter-test-graph: all checks passed"
