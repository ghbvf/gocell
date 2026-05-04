#!/usr/bin/env bash
# K#06 CODEGEN-CONTRACT-GEN gate.
#
# Thin wrapper around `gocell verify codegen-contract`. The Go subcommand owns
# the worktree sandbox + drift detection logic so bash and Go don't drift out
# of sync.
#
# Modes:
#   ./hack/verify-codegen-contract.sh           CI sandbox (git worktree at HEAD)
#   ./hack/verify-codegen-contract.sh --local   Local fast path (no sandbox)
#
# Pattern: kubernetes/kubernetes hack/lib/verify-generated.sh, but implemented
# in Go for testability and to avoid bash/Go logic split.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
exec go -C "${ROOT}" run ./cmd/gocell verify codegen-contract "$@"
