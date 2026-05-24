#!/usr/bin/env bash
# Shared-schema mirror codegen gate.
#
# Thin wrapper around `gocell verify codegen-shared-schema`. The Go subcommand
# owns the in-process byte diff logic so bash and Go don't drift out of sync.
#
# This gate has no --local / sandbox mode: the verify subcommand performs
# purely in-process file I/O and does not require a git worktree.
#
# Usage:
#   ./hack/verify-codegen-shared-schema.sh
#
# Pattern: kubernetes/kubernetes hack/lib/verify-generated.sh, but implemented
# in Go for testability.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

exec go -C "${ROOT}" run ./cmd/gocell verify codegen-shared-schema "$@"
