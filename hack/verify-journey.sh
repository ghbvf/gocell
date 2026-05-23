#!/usr/bin/env bash
# verify-journey checks that all active journeys carry at least one auto check
# and that referenced check targets resolve to executable tests.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

# shellcheck source=hack/lib/gocell-bin.sh
source "${ROOT}/hack/lib/gocell-bin.sh"

gocell::cli verify journey --active
