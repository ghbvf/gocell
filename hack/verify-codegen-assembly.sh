#!/usr/bin/env bash
# K#10 ASSEMBLY-YAML-MINIMAL gate.
#
# Thin wrapper around `gocell verify codegen-assembly`. The Go subcommand owns
# the worktree sandbox + drift detection logic so bash and Go don't drift
# out of sync.
#
# Modes:
#   ./hack/verify-codegen-assembly.sh             CI sandbox (git worktree at HEAD)
#   ./hack/verify-codegen-assembly.sh --local     Local fast path (no sandbox)
#   ./hack/verify-codegen-assembly.sh --local=false  Explicit sandbox (same as CI default)
#
# Note: `gocell verify codegen-assembly` defaults to --local=true (K#05 W2 DX).
# This script passes --local=false when no args are given so that CI behaviour
# (sandbox mode) is preserved without callers needing to be updated.
#
# Pattern: kubernetes/kubernetes hack/lib/verify-generated.sh, but
# implemented in Go for testability and to avoid bash/Go logic split.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

if [[ $# -eq 0 ]]; then
  exec go run ./cmd/gocell verify codegen-assembly --local=false
fi
exec go run ./cmd/gocell verify codegen-assembly "$@"
