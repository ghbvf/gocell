#!/usr/bin/env bash
# verify-schema-lenient: asserts that response / event payload / event headers /
# examples response / examples command response schemas do NOT declare
# `additionalProperties: false`.
#
# Per ADR-202605031600 (G5 V1-RESPONSE-EVOLVE), v1 response and event schemas
# are intentionally lenient so producers can add optional fields without
# breaking clients/consumers. Shared error envelopes (contracts/shared/errors/
# and example mirrors) are exempt; they retain strict.
#
# Run via `make verify`.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

# shellcheck source=hack/lib/util.sh
source "${ROOT}/hack/lib/util.sh"

gocell::log::status "Verifying response / event schemas are lenient (ADR-202605031600)"

# Subtree + glob pairs that must be lenient (no additionalProperties:false anywhere).
PAIRS=(
    "contracts/http response.schema.json"
    "contracts/event payload.schema.json"
    "contracts/event headers.schema.json"
    "examples/iotdevice/contracts/http response.schema.json"
    "examples/iotdevice/contracts/command response.schema.json"
    "examples/iotdevice/contracts/event payload.schema.json"
    "examples/iotdevice/contracts/event headers.schema.json"
    "examples/todoorder/contracts/http response.schema.json"
    "examples/todoorder/contracts/event payload.schema.json"
    "examples/todoorder/contracts/event headers.schema.json"
)

violations=0
for pair in "${PAIRS[@]}"; do
    read -r root glob <<< "$pair"
    if [[ ! -d "$root" ]]; then
        continue
    fi
    while IFS= read -r f; do
        if grep -q '"additionalProperties": *false' "$f"; then
            gocell::log::error "VIOLATION: $f contains additionalProperties:false (forbidden by ADR-202605031600)"
            violations=$((violations + 1))
        fi
    done < <(find "$root" -type f -name "$glob")
done

if [[ "$violations" -gt 0 ]]; then
    gocell::log::error "Found $violations schema(s) with forbidden additionalProperties:false."
    echo "Run: bash hack/scripts/normalize-schema.sh <root-dir> <filename-glob>" >&2
    echo "See: docs/architecture/202605031600-adr-v1-schema-evolution.md" >&2
    exit 1
fi

gocell::log::status "PASS: response / event schemas are lenient"
