#!/usr/bin/env bash
# verify-bucket: workspace
#
# INVARIANT: MODULE-GRAPH-TEST-EDGE-01 (Medium): a test-only backend/observability
# dependency must be confined to the module(s) that genuinely use it, and must not
# leak into any other workspace module's published graph.
#
# Background: per-module `go mod tidy` resolves the FULL (all-build-tags) import
# closure of every package a module imports, so a test-only backend reachable through
# a shared fixture package silently becomes an `// indirect` require in every
# consuming module's go.mod — visible to external consumers even though no code in
# that module uses it. #1908 moved the minio/rabbitmq testcontainer helpers into their
# own modules (tests/testutil/{minioctr,rabbitmqctr}) so the root module and every
# adapter that only consumes generic tests/testutil helpers stop inheriting them;
# #1909 gave vault an adapter-local recording metrics.Provider so it stops requiring
# adapters/prometheus. This gate locks both wins against regression: a future re-merge
# would re-introduce the edge and pass verify-workspace.sh (drift is self-consistent)
# — only an explicit forbidden-edge assertion catches it.
#
# Detection uses `go mod edit -json` (official tooling, not hand-rolled regex) and
# checks whether a forbidden module path appears as a require/replace in the target
# module's own go.mod. EVERY workspace module (via gocell::modules::dirs, including
# root) is swept: the minio/rabbitmq testcontainer modules are forbidden everywhere
# EXCEPT their enumerated rightful owners (the helper module that wraps the container
# plus the adapter/test module whose own code directly drives it). Anti-vacuity
# positive controls assert those owners DO declare the module, proving the parser
# reads real require data rather than passing on a misread or empty go.mod.
#
# AI-robust grade: Medium. go.mod content is build metadata, not Go-type-expressible,
# so the Hard ceiling (violation un-expressible / compile-or-golden break) is
# unreachable here — a require line can always be hand-added; CI catches it.
#
# Blind spots (documented, not silently absent): the forbid sweep auto-covers every
# module in go.work, so a new module inherits the checks for free — but the OWNER
# allowlists (minio_is_owner / rabbitmq_is_owner) and the prometheus forbid table are
# hand-maintained: a NEW module that legitimately drives minio/rabbitmq must be added
# to the matching owner function (intentional friction), and a NEW heavy backend
# beyond minio/rabbitmq/prometheus needs a new table entry. The detector is path-exact
# (quoted full module path), so a vanity-renamed fork of the same dependency would not
# be caught.

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
        gocell::log::error "${dir}/go.mod must NOT declare '${path}' — test-only edge leaked into the module graph (MODULE-GRAPH-TEST-EDGE-01)"
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

gocell::log::status "Checking workspace module graphs for leaked test-only edges"

readonly MINIO="github.com/testcontainers/testcontainers-go/modules/minio"
readonly RABBITMQ="github.com/testcontainers/testcontainers-go/modules/rabbitmq"

# Rightful owners of each testcontainer backend module: the helper module that wraps
# the container plus the adapter/test module whose own code directly drives it. Any
# OTHER module declaring the backend is a leak. A new module that legitimately drives
# the backend must be added here (intentional friction; see Blind spots above).
minio_is_owner() { case "$1" in adapters/s3 | tests/testutil/minioctr) return 0 ;; *) return 1 ;; esac; }
rabbitmq_is_owner() { case "$1" in adapters/rabbitmq | tests/integration | tests/testutil/rabbitmqctr) return 0 ;; *) return 1 ;; esac; }

# #1908 — sweep EVERY workspace module; the minio/rabbitmq testcontainer modules must
# appear ONLY in their rightful owners, never leaked into root or any unrelated
# module. Enumerated from the canonical workspace funnel so new modules are covered
# automatically. Command substitution propagates an enumeration failure (a
# `while read < <(...)` would swallow the funnel's non-zero return in a subshell).
if ! _all_modules="$(gocell::modules::dirs)"; then
    gocell::log::error "workspace module enumeration failed — cannot evaluate module graphs"
    exit 1
fi
while IFS= read -r _mod; do
    [[ -n "${_mod}" ]] || continue
    dir="${_mod#./}" # './adapters/s3' -> 'adapters/s3'; '.' (root) stays '.'
    minio_is_owner "${dir}" || forbid "${dir}" "${MINIO}"
    rabbitmq_is_owner "${dir}" || forbid "${dir}" "${RABBITMQ}"
done <<<"${_all_modules}"

# #1909 — vault tests must not pull the prometheus adapter or its client into the
# vault module graph (replaced by an adapter-local recording metrics.Provider).
forbid adapters/vault "github.com/ghbvf/gocell/adapters/prometheus"
forbid adapters/vault "github.com/prometheus/client_golang"
forbid adapters/vault "github.com/prometheus/client_model"

# Anti-vacuity positive controls — the rightful owners MUST declare the backend, so a
# misread / empty go.mod cannot make the forbid sweep vacuously green. Covers both the
# new helper modules (#1908) and the adapters that directly drive the containers.
require_present tests/testutil/minioctr "${MINIO}"
require_present tests/testutil/rabbitmqctr "${RABBITMQ}"
require_present adapters/s3 "${MINIO}"
require_present adapters/rabbitmq "${RABBITMQ}"
require_present adapters/prometheus "github.com/prometheus/client_golang"

if [[ "${fail}" -ne 0 ]]; then
    gocell::log::error "verify-adapter-test-graph: forbidden test-only module edge present (or positive control missing)"
    exit 1
fi

gocell::log::status "verify-adapter-test-graph: all checks passed"
