#!/usr/bin/env bash
# verify-schema-policy: single jq-based tool that enforces the three schema
# policies declared by ADR-202605031600 (G5 V1-RESPONSE-EVOLVE), replacing
# verify-schema-lenient.sh + scripts/normalize-schema.sh.
#
# Policies:
#   lenient   — response / event payload / headers (also examples/* mirrors):
#               MUST NOT declare `additionalProperties: false` anywhere
#               (allows v1 to grow optional fields without breaking clients).
#   strict    — error envelope (contracts/shared/errors/error-response-v1.schema.json
#               + example mirrors): MUST declare `additionalProperties: false`
#               at top level (the envelope shape is stable).
#   metaonly  — metadata-only event payloads (currently event.config.*):
#               MUST declare `unevaluatedProperties: false` so a buggy producer
#               adding a state-bearing field (e.g. `value`) is rejected by
#               contracttest before merge. See ADR-202605031600 §4.
#
# Why a single tool: when policies were split across grep + jq scripts, three
# failure modes appeared: (a) grep missed multi-line formatted JSON, (b) error
# envelopes had no positive-strict gate, (c) the strip helper had no inverse.
# One jq tool with explicit per-target policy declarations resolves all three.
#
# Usage:
#   bash hack/verify-schema-policy.sh           # check every declared target
#   bash hack/verify-schema-policy.sh --fix     # check + auto-strip violations
#                                               # (lenient policy only — adding
#                                               # constraints to strict/metaonly
#                                               # is a human decision)
#
# Exit codes: 0 on PASS / fix complete; 1 on violations (check mode).
#
# Run via `make verify` (auto-discovered via hack/verify-*.sh glob).

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

# shellcheck source=hack/lib/util.sh
source "${ROOT}/hack/lib/util.sh"

if ! command -v jq >/dev/null 2>&1; then
    gocell::log::error "jq not found in PATH; install jq to run schema policy checks"
    exit 127
fi

mode="check"
if [[ "${1:-}" == "--fix" ]]; then
    mode="fix"
fi

# Policy targets: "<policy> <root> <filename-glob>".
# Order matters only for human-readable output; all targets are independently
# checked. Examples/* mirrors are listed explicitly to keep this file the
# single source of truth for "where does each policy apply" — the grep-based
# predecessor inferred this implicitly via PAIRS, which proved fragile when a
# new examples/* subtree was added without updating the script.
TARGETS=(
    # lenient: response / event payload / headers
    "lenient   contracts/http                                    response.schema.json"
    "lenient   contracts/event                                   payload.schema.json"
    "lenient   contracts/event                                   headers.schema.json"
    "lenient   examples/iotdevice/contracts/http                 response.schema.json"
    "lenient   examples/iotdevice/contracts/command              response.schema.json"
    "lenient   examples/iotdevice/contracts/event                payload.schema.json"
    "lenient   examples/iotdevice/contracts/event                headers.schema.json"
    "lenient   examples/todoorder/contracts/http                 response.schema.json"
    "lenient   examples/todoorder/contracts/event                payload.schema.json"
    "lenient   examples/todoorder/contracts/event                headers.schema.json"
    # strict: error envelope (top-level + nested error object both required —
    # checked via recursive jq below; declared in error-response-v1.schema.json)
    "strict    contracts/shared/errors                           error-response-v1.schema.json"
    "strict    examples/iotdevice/contracts/shared/errors        error-response-v1.schema.json"
    "strict    examples/todoorder/contracts/shared/errors        error-response-v1.schema.json"
    # metaonly: metadata-only event payloads — whitelist via unevaluatedProperties
    "metaonly  contracts/event/config/entry-upserted/v1          payload.schema.json"
    "metaonly  contracts/event/config/entry-deleted/v1           payload.schema.json"
    "metaonly  contracts/event/config/version-published/v1       payload.schema.json"
    "metaonly  contracts/event/config/rollback/v1                payload.schema.json"
)

# jq filters per policy.
#   lenient:  any object node with additionalProperties==false → violation.
#   strict:   top-level additionalProperties != false → violation. (Nested
#             error.additionalProperties is also asserted; the schema's
#             top-level `properties.error` re-declares it.)
#   metaonly: top-level unevaluatedProperties != false → violation.
JQ_LENIENT_FIND='[.. | objects | select(.additionalProperties == false)] | length > 0'
JQ_STRICT_OK='.additionalProperties == false'
JQ_METAONLY_OK='.unevaluatedProperties == false'

# fix filter: strip every additionalProperties:false at any depth.
JQ_FIX_LENIENT='walk(if type == "object" and .additionalProperties == false then del(.additionalProperties) else . end)'

gocell::log::status "Verifying schema policies (ADR-202605031600), mode=${mode}"

violations=0
fixed=0
checked=0
for target in "${TARGETS[@]}"; do
    # shellcheck disable=SC2086
    set -- $target
    policy="$1"
    root="$2"
    glob="$3"
    if [[ ! -d "$root" ]]; then
        continue
    fi
    while IFS= read -r -d '' f; do
        checked=$((checked + 1))
        case "$policy" in
            lenient)
                if [[ "$(jq -r "$JQ_LENIENT_FIND" "$f")" == "true" ]]; then
                    if [[ "$mode" == "fix" ]]; then
                        tmp="${f}.policyfix.tmp"
                        trap 'rm -f "$tmp"' ERR EXIT
                        jq "$JQ_FIX_LENIENT" "$f" > "$tmp"
                        mv "$tmp" "$f"
                        trap - ERR EXIT
                        gocell::log::status "FIXED  [lenient]  $f"
                        fixed=$((fixed + 1))
                    else
                        gocell::log::error "VIOLATION [lenient]  $f contains additionalProperties:false"
                        violations=$((violations + 1))
                    fi
                fi
                ;;
            strict)
                if [[ "$(jq -r "$JQ_STRICT_OK" "$f")" != "true" ]]; then
                    gocell::log::error "VIOLATION [strict]   $f missing top-level additionalProperties:false"
                    violations=$((violations + 1))
                fi
                ;;
            metaonly)
                if [[ "$(jq -r "$JQ_METAONLY_OK" "$f")" != "true" ]]; then
                    gocell::log::error "VIOLATION [metaonly] $f missing top-level unevaluatedProperties:false"
                    violations=$((violations + 1))
                fi
                ;;
            *)
                gocell::log::error "unknown policy: $policy"
                exit 2
                ;;
        esac
    done < <(find "$root" -type f -name "$glob" -print0)
done

if [[ "$violations" -gt 0 ]]; then
    gocell::log::error "Found $violations violation(s) across $checked file(s)."
    echo "Run: bash hack/verify-schema-policy.sh --fix    # auto-strip lenient violations only" >&2
    echo "See: docs/architecture/202605031600-adr-v1-schema-evolution.md" >&2
    exit 1
fi

if [[ "$mode" == "fix" ]]; then
    gocell::log::status "FIX complete: $fixed file(s) normalized, $checked checked"
else
    gocell::log::status "PASS: $checked schema file(s) match declared policies"
fi
