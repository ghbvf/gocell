#!/usr/bin/env bash
# K#06 CODEGEN-CONTRACT-GEN gate.
#
# Thin wrapper around `gocell verify codegen-contract`. The Go subcommand owns
# the worktree sandbox + drift detection logic so bash and Go don't drift out
# of sync.
#
# Modes:
#   ./hack/verify-codegen-contract.sh             CI sandbox (git worktree at HEAD)
#   ./hack/verify-codegen-contract.sh --local     Local fast path (no sandbox)
#   ./hack/verify-codegen-contract.sh --local=false  Explicit sandbox (same as CI default)
#
# Note: `gocell verify codegen-contract` defaults to --local=true (K#05 W2 DX).
# This script passes --local=false when no args are given so that CI behaviour
# (sandbox mode) is preserved without callers needing to be updated.
#
# Pattern: kubernetes/kubernetes hack/lib/verify-generated.sh, but implemented
# in Go for testability and to avoid bash/Go logic split.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if [[ $# -eq 0 ]]; then
  exec go -C "${ROOT}" run ./cmd/gocell verify codegen-contract --local=false
fi
exec go -C "${ROOT}" run ./cmd/gocell verify codegen-contract "$@"
