#!/usr/bin/env bash
# Normalize JSON schema files: strip `"additionalProperties": false` at all
# levels (top-level + nested) per ADR-202605031600 v1 schema evolution.
#
# Usage:
#   bash hack/scripts/normalize-schema.sh <root-dir> <filename-glob>
#
# Examples:
#   bash hack/scripts/normalize-schema.sh contracts/http   "response.schema.json"
#   bash hack/scripts/normalize-schema.sh contracts/event  "payload.schema.json"
#   bash hack/scripts/normalize-schema.sh contracts/event  "headers.schema.json"
#   bash hack/scripts/normalize-schema.sh examples/iotdevice/contracts/http     "response.schema.json"
#   bash hack/scripts/normalize-schema.sh examples/iotdevice/contracts/command  "response.schema.json"
#   bash hack/scripts/normalize-schema.sh examples/todoorder/contracts/http     "response.schema.json"
#
# Does NOT touch:
#   - request.schema.json (FMT-20 still enforces strict request)
#   - contracts/shared/errors/error-response-v1.schema.json
#     (and its examples/*/contracts/shared/errors/ mirrors)
#   - any *_test.go / testdata fixtures
#
# Exit codes: 0 on success; non-zero on jq/find failure (set -euo pipefail).

set -euo pipefail

if [[ $# -ne 2 ]]; then
    echo "usage: $0 <root-dir> <filename-glob>" >&2
    exit 2
fi

root="$1"
glob="$2"

if [[ ! -d "$root" ]]; then
    echo "error: root dir not found: $root" >&2
    exit 2
fi

# Recursively delete additionalProperties keys whose value is the literal
# boolean false. Preserves additionalProperties:true (explicit lenient) and
# any object-form additionalProperties (sub-schema constraints).
JQ_FILTER='walk(if type == "object" and .additionalProperties == false then del(.additionalProperties) else . end)'

count=0
while IFS= read -r -d '' f; do
    # Write to sibling .normalize.tmp then atomically mv (same filesystem
    # avoids cross-fs issues and the system TMPDIR being sandbox-restricted).
    tmp="${f}.normalize.tmp"
    jq "$JQ_FILTER" "$f" > "$tmp"
    mv "$tmp" "$f"
    count=$((count + 1))
done < <(find "$root" -type f -name "$glob" -print0)

echo "normalized $count file(s) under $root matching '$glob'"
