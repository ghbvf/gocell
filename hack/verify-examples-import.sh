#!/usr/bin/env bash
# verify-examples-import enforces that examples/ never imports corecells/*/internal/
# or adapters/*/internal/ — example cells must consume their dependencies only
# through public APIs.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

fail=0
if grep -rn --include='*.go' "github.com/ghbvf/gocell/corecells/.*/internal" examples/ 2>/dev/null; then
    echo "FAIL: examples/ imports corecells/*/internal/" >&2
    fail=1
fi
if grep -rn --include='*.go' "github.com/ghbvf/gocell/adapters/.*/internal" examples/ 2>/dev/null; then
    echo "FAIL: examples/ imports adapters/*/internal/" >&2
    fail=1
fi
exit "${fail}"
