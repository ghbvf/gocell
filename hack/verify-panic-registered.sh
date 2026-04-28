#!/usr/bin/env bash
# verify-panic-registered enforces PANIC-REGISTERED-01: production panic()
# calls must be either inside Must* functions or ADR-registered permanent
# exceptions.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

go test ./tools/archtest/... -run 'TestPanicRegistered' -count=1
