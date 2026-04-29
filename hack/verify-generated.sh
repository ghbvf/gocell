#!/usr/bin/env bash
# verify-generated fails when checked-in generated artifacts are not
# self-consistent with assembly metadata. The Go verifier derives the expected
# artifact manifest from metadata instead of trusting generator stdout to define
# the verification scope.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

# shellcheck source=hack/lib/util.sh
source "${ROOT}/hack/lib/util.sh"

gocell::log::status "Verifying generated artifacts"
go run ./cmd/gocell verify generated
