#!/usr/bin/env bash
# verify-bucket: codegen
# cellmodules exported-metadata bundle codegen gate (#1515).
#
# Thin wrapper around `gocell verify codegen-cellmodule-metadata`. The Go
# subcommand owns the two-layer check (Layer-1 byte regenerate diff + Layer-2
# semantic closure equivalence) so bash and Go don't drift out of sync.
#
# This gate has no --local / sandbox mode: the verify subcommand performs purely
# in-process file I/O and does not require a git worktree.
#
# Usage:
#   ./hack/verify-codegen-cellmodule-metadata.sh
#
# Pattern: kubernetes/kubernetes hack/lib/verify-generated.sh, but implemented
# in Go for testability — mirrors hack/verify-codegen-shared-schema.sh.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

exec go -C "${ROOT}" run ./cmd/gocell verify codegen-cellmodule-metadata "$@"
