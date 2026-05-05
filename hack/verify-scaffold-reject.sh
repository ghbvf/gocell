#!/usr/bin/env bash
# verify-scaffold-reject asserts that `gocell scaffold slice` rejects kebab-case
# slice names (FMT-16 enforced at scaffold time, not just at validate).

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

set +e
output=$(go run ./cmd/gocell scaffold slice --id=test-slice --cell=accesscore 2>&1)
exit_code=$?
set -e

if [[ ${exit_code} -eq 0 ]]; then
    echo "FAIL: scaffold exited 0 but should have rejected kebab slice name" >&2
    echo "${output}" >&2
    exit 1
fi

# K#08 PII-safe message: the public scaffold rejection message is the fixed
# sentinel code + structured detail (id="..." suggestion="..."), not a
# free-form English explanation. Assert on the canonical code prefix rather
# than the prose.
if ! grep -q "ERR_SCAFFOLD_INVALID_OPTS" <<<"${output}"; then
    echo "FAIL: scaffold rejected (exit ${exit_code}) but error did not surface 'ERR_SCAFFOLD_INVALID_OPTS' sentinel" >&2
    echo "${output}" >&2
    exit 1
fi
