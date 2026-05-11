#!/usr/bin/env bash
# verify-panic-registered enforces PANIC-REGISTERED-01: production panic()
# calls must wrap their payload with panicregister.Approved(literal, value).

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

GOFLAGS='' go test ./tools/archtest -count=1 -run '^TestPanicRegistered($|ScannerFixtures$)'
