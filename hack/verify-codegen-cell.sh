#!/usr/bin/env bash
# K#04 CODEGEN-CELL-GEN gate.
#
# Verifies cell_gen.go and slice_gen.go are in sync with cell.yaml /
# slice.yaml. Implementation: tools/codegen + tools/codegen/cellgen.
#
# Two modes:
#   ./hack/verify-codegen-cell.sh           CI sandbox: git worktree clone
#                                           + regenerate + git status diff
#   ./hack/verify-codegen-cell.sh --local   Local fast path: in-place
#                                           `gocell generate cell --all --verify`,
#                                           no worktree
#
# Use --local during development for fast feedback (no worktree overhead).
# CI runs without --local for hermetic isolation: the sandbox prevents the
# generator from polluting the developer's working tree if it has bugs.
#
# Pattern: kubernetes/kubernetes hack/lib/verify-generated.sh.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

mode="sandbox"
if [[ "${1:-}" == "--local" ]]; then
  mode="local"
fi

if [[ "${mode}" == "local" ]]; then
  echo "Verifying generated cell scaffolds (K#04, --local mode)..."
  if ! go run ./cmd/gocell generate cell --all --verify; then
    echo
    echo "ERROR: generated cell files are out of sync with cell.yaml/slice.yaml."
    echo "FIX: run locally and commit:"
    echo "    go run ./cmd/gocell generate cell --all"
    exit 1
  fi
  echo "Generated cell scaffolds OK (--local)."
  exit 0
fi

echo "Verifying generated cell scaffolds (K#04, sandbox mode)..."

TMP_WT="$(mktemp -d -t gocell-codegen-cell.XXXXXX)"
cleanup() {
  git worktree remove --force "${TMP_WT}" >/dev/null 2>&1 || true
  rm -rf "${TMP_WT}" 2>/dev/null || true
}
trap cleanup EXIT

git worktree add --detach "${TMP_WT}" HEAD

(
  cd "${TMP_WT}"
  if ! go run ./cmd/gocell generate cell --all; then
    echo "ERROR: gocell generate cell --all failed inside sandbox worktree." >&2
    exit 1
  fi

  if [[ -n "$(git status --porcelain)" ]]; then
    echo "ERROR: generated cell files are out of sync with cell.yaml/slice.yaml." >&2
    echo >&2
    echo "Drifted files:" >&2
    git status --porcelain >&2
    echo >&2
    echo "Per-file diff (truncated to 200 lines per file):" >&2
    git status --porcelain | cut -c4- | while read -r f; do
      echo "===== ${f} =====" >&2
      git diff -- "${f}" | head -200 >&2 || true
    done
    echo >&2
    echo "FIX: run locally and commit:" >&2
    echo "    go run ./cmd/gocell generate cell --all" >&2
    exit 1
  fi
)

echo "Generated cell scaffolds OK."
