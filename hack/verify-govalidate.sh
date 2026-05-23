#!/usr/bin/env bash
# verify-govalidate runs `gocell validate --strict` to enforce metadata
# governance rules (FMT, ADV, REF, LAYER, VERIFY, CH, CONTRACT-CONSISTENCY).

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

# shellcheck source=hack/lib/gocell-bin.sh
source "${ROOT}/hack/lib/gocell-bin.sh"

gocell::cli validate --strict
