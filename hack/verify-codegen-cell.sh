#!/usr/bin/env bash
# K#04 CODEGEN-CELL-GEN gate.
#
# Thin wrapper around `gocell verify codegen-cell`. The Go subcommand owns
# the worktree sandbox + drift detection logic so bash and Go don't drift
# out of sync.
#
# Modes:
#   ./hack/verify-codegen-cell.sh           CI sandbox (git worktree at HEAD)
#   ./hack/verify-codegen-cell.sh --local   Local fast path (no sandbox)
#
# Pattern: kubernetes/kubernetes hack/lib/verify-generated.sh, but
# implemented in Go for testability and to avoid bash/Go logic split.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

exec go run ./cmd/gocell verify codegen-cell "$@"
