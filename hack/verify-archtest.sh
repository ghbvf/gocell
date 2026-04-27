#!/usr/bin/env bash
# verify-archtest runs the architectural unit-test suite (LAYER-*, AUTH-*,
# SEC-FAIL-CLOSED-*, ERROR-FIRST-API-01, META-QUERYPARAM-DRIFT, ADV-06, etc.).
# Race detector is enabled because several archtest helpers use packages.Load
# concurrently.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

go test ./tools/archtest/... -race -count=1
