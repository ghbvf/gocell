#!/usr/bin/env bash
# verify-scaffold-reject asserts that `gocell scaffold slice` rejects kebab-case
# slice names (FMT-16 enforced at scaffold time, not just at validate).
# Also asserts that `gocell scaffold assembly` rejects kebab-case assembly IDs.

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

# Verify that `gocell scaffold assembly` also rejects kebab-case --id values.
set +e
asm_output=$(go run ./cmd/gocell scaffold assembly --id=foo-bar --cells=examplecell --team=t --role=r 2>&1)
asm_exit_code=$?
set -e

if [[ ${asm_exit_code} -eq 0 ]]; then
    echo "FAIL: scaffold assembly exited 0 but should have rejected kebab assembly ID" >&2
    echo "${asm_output}" >&2
    exit 1
fi

if ! grep -q "ERR_SCAFFOLD_INVALID_OPTS" <<<"${asm_output}"; then
    echo "FAIL: scaffold assembly rejected (exit ${asm_exit_code}) but error did not surface 'ERR_SCAFFOLD_INVALID_OPTS' sentinel" >&2
    echo "${asm_output}" >&2
    exit 1
fi

echo "OK: scaffold slice and scaffold assembly both reject invalid IDs with ERR_SCAFFOLD_INVALID_OPTS"
