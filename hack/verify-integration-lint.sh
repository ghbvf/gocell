#!/usr/bin/env bash
# verify-bucket: lint
# verify-integration-lint.sh — lint integration-build-tagged helper packages
# whose invariants are invisible to the default, untagged golangci-lint lane.
#
# Discovered automatically by make verify via hack/verify-*.sh glob
# (governance.yml). Keep this list narrow: broad integration linting duplicates
# the service-bearing integration-test job and can surface unrelated existing
# integration debt.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

# shellcheck source=lib/golangci-lint.sh
source hack/lib/golangci-lint.sh

if ! golangci_lint="$(gocell::golangci_lint::ensure)"; then
    echo "verify-integration-lint: failed to bootstrap golangci-lint" >&2
    exit 1
fi
if [[ -z "${golangci_lint}" || ! -x "${golangci_lint}" ]]; then
    echo "verify-integration-lint: ensure returned empty / non-executable path: '${golangci_lint}'" >&2
    exit 1
fi

echo "verify-integration-lint: using ${golangci_lint}" >&2
"${golangci_lint}" --version >&2 || true

"${golangci_lint}" run --build-tags=integration ./adapters/postgres/pgtest/...

echo "verify-integration-lint: integration-tag lint targets passed"
