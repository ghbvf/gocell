#!/usr/bin/env bash
# K#09 SCAFFOLD-ONE-CMD assembly gate.
#
# Verifies that `gocell scaffold assembly` produces a compilable assembly:
# scaffolds a throwaway assembly + cmd/{id} skeleton into a temp directory,
# auto-generates K#10 derived files (modules_gen.go + main.go + boundary.yaml),
# then runs `go build` over cmd/{id}/.... Fails with a clear diagnostic if
# scaffold output drifts from the buildable baseline.
#
# Modes:
#   ./hack/verify-scaffold-assembly.sh             local fast path (default)
#   ./hack/verify-scaffold-assembly.sh --sandbox   isolated git worktree clone
#
# Pattern: same fast-path + sandbox model as verify-scaffold-bundle.sh.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

MODE="local"
for arg in "$@"; do
  case "$arg" in
    --sandbox) MODE="sandbox" ;;
    --local)   MODE="local" ;;
    *) echo "verify-scaffold-assembly: unknown arg: $arg" >&2; exit 2 ;;
  esac
done

CELL_ID="asmsmokecell"
ASM_ID="asmsmoke"

run_smoke() {
  local root="$1"
  pushd "$root" >/dev/null

  cleanup_smoke_artifacts() {
    rm -rf "cells/${CELL_ID}" \
           "contracts/http/${CELL_ID}" \
           "generated/contracts/http/${CELL_ID}" \
           "assemblies/${ASM_ID}" \
           "cmd/${ASM_ID}"
    # `go build ./cmd/${ASM_ID}/...` invoked from repo root drops a binary
    # named ${ASM_ID} in the working directory; remove it too.
    rm -f "${ASM_ID}"
  }
  cleanup_smoke_artifacts
  trap cleanup_smoke_artifacts RETURN

  # Need a cell first so --cells references are valid.
  go run ./cmd/gocell scaffold cell \
    --id="${CELL_ID}" \
    --type=core \
    --level=L1 \
    --team=scaffoldsmoke \
    --role=cell-owner \
    --skip-generate

  go run ./cmd/gocell scaffold assembly \
    --id="${ASM_ID}" \
    --cells="${CELL_ID}" \
    --team=scaffoldsmoke \
    --role=maintainer \
    --deploy=k8s

  # Auto-generate ran inside `scaffold assembly`; verify the result builds.
  go build -o /dev/null "./cmd/${ASM_ID}/..."

  # K#10 funnel sanity: assembly.yaml must NOT carry deployTemplate when
  # --deploy=k8s (default). Grep returns 1 (no match) on success.
  if grep -q "deployTemplate" "assemblies/${ASM_ID}/assembly.yaml"; then
    echo "verify-scaffold-assembly: regression — --deploy=k8s wrote deployTemplate to yaml" >&2
    exit 1
  fi

  popd >/dev/null
}

case "$MODE" in
  local)
    run_smoke .
    ;;
  sandbox)
    SANDBOX_DIR="$(mktemp -d)"
    git worktree add --quiet --detach "$SANDBOX_DIR" HEAD
    trap 'git worktree remove --force "$SANDBOX_DIR" >/dev/null 2>&1 || true; rm -rf "$SANDBOX_DIR"' EXIT
    run_smoke "$SANDBOX_DIR"
    ;;
esac

echo "verify-scaffold-assembly: OK"
