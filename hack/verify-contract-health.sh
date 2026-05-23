#!/usr/bin/env bash
# verify-contract-health runs `gocell check contract-health` to enforce
# contract metadata health rules (CH-*).

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

# shellcheck source=hack/lib/gocell-bin.sh
source "${ROOT}/hack/lib/gocell-bin.sh"

gocell::cli check contract-health
