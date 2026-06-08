#!/usr/bin/env bash
# ROOT-FRAMEWORK-TESTS-NO-ADAPTER-IMPORT-01
#
# runtime/bootstrap and root-owned tests are framework/base test surfaces. They
# must not directly import top-level adapters, because adapters will become
# satellite/per-adapter modules and the root go.mod must remain replace-free.
# Adapter-real tests belong in adapter packages or consumer satellite modules.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

tmp="$(mktemp)"
trap 'rm -f "${tmp}"' EXIT

scanned=0
while IFS= read -r -d '' file; do
    scanned=$((scanned + 1))
    if grep -nE '"github\.com/ghbvf/gocell/adapters(/|")' "${file}" >>"${tmp}"; then
        :
    fi
done < <(
    find runtime/bootstrap tests \
        -path tests/integration -prune -o \
        -path tests/testutil/pgshare -prune -o \
        -path '*/testdata/*' -prune -o \
        -type f -name '*.go' -print0
)

if [[ "${scanned}" -eq 0 ]]; then
    echo "ROOT-FRAMEWORK-TESTS-NO-ADAPTER-IMPORT-01: scanned zero files; check find roots" >&2
    exit 1
fi

if [[ -s "${tmp}" ]]; then
    echo "ROOT-FRAMEWORK-TESTS-NO-ADAPTER-IMPORT-01: root framework/tests must not import adapters:" >&2
    cat "${tmp}" >&2
    echo >&2
    echo "Move adapter-real coverage to adapter packages or consumer satellite modules." >&2
    exit 1
fi

echo "ROOT-FRAMEWORK-TESTS-NO-ADAPTER-IMPORT-01: scanned ${scanned} root framework/test files; no adapter imports found."
