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
# Blind spots (documented, not silently absent): only the edges enumerated below are
# locked. A test-only dep leaking into a DIFFERENT module, or a NEW heavy backend
# added to `tests/testutil`, is not auto-covered — extend the table when adding
# backends or adapters. The detector is path-exact (quoted full module path), so a
# vanity-renamed fork of the same dependency would not be caught.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

# shellcheck source=lib/util.sh
source hack/lib/util.sh

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

# #1908 — minio/rabbitmq testcontainer modules must not leak into adapters that only
# consume the generic (backend-agnostic) tests/testutil helpers.
for dir in adapters/postgres adapters/redis adapters/otel adapters/vault adapters/mqtt; do
    forbid "${dir}" "${MINIO}"
    forbid "${dir}" "${RABBITMQ}"
done

# #1908 (cross-backend) — each backend's own adapter must carry ONLY its backend, not
# the sibling one (s3 used minio yet inherited rabbitmq; rabbitmq used rabbitmq yet
# inherited minio — both via the shared tests/testutil package).
forbid adapters/s3 "${RABBITMQ}"
forbid adapters/rabbitmq "${MINIO}"

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
