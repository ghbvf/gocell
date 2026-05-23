#!/usr/bin/env bash
# verify-journey checks that all active journeys carry at least one auto check
# and that referenced check targets resolve to executable tests.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

go run ./cmd/gocell verify journey --active
