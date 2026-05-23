#!/usr/bin/env bash
# verify-unconditional-skip rejects test files whose t.Skip is unconditional —
# any blanket skip is a hidden disabled test and must be either deleted or
# guarded with a runtime predicate.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

# shellcheck source=hack/lib/gocell-bin.sh
source "${ROOT}/hack/lib/gocell-bin.sh"

gocell::cli check unconditional-skip ./...
