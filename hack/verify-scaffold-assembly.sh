#!/usr/bin/env bash
# verify-bucket: scaffold
# K#09 SCAFFOLD-ONE-CMD assembly gate.
#
# Verifies that `gocell scaffold assembly` produces a compilable assembly:
# scaffolds a throwaway assembly + cmd/{id} skeleton into a temp directory,
# auto-generates K#10 derived files (modules_gen.go + main.go + boundary.yaml),
# then runs `go build` over cmd/{id}/.... Fails with a clear diagnostic if
# scaffold output drifts from the buildable baseline.
#
# Modes (default is `--sandbox` — isolated git worktree clone, safe for CI):
#   ./hack/verify-scaffold-assembly.sh             sandbox mode (default)
#   ./hack/verify-scaffold-assembly.sh --local     local fast path (worktree writes)
#   ./hack/verify-scaffold-assembly.sh --sandbox   isolated git worktree clone
#
# Pattern: same sandbox-default model as verify-scaffold-bundle.sh.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

MODE="sandbox"
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

  # Need a cell first so --cells references are valid. scaffold cell writes to
  # the conventional root cells/<id>/ location.
  go run ./cmd/gocell scaffold cell \
    --id="${CELL_ID}" \
    --type=core \
    --level=L1 \
    --team=scaffoldsmoke \
    --role=cell-owner \
    --skip-generate

  # --layout=conventional: this smoke exercises the framework-general CONSUMER
  # scaffold convention (a cell at root cells/<id>/). The sandbox is a clone of
  # GoCell's OWN repo, which ships .gocell/manifest.yaml and would otherwise
  # auto-detect MANIFEST mode — whose root module intentionally does NOT declare
  # root cells/ (GoCell's platform cells live in the corecells module, #1560), so
  # manifest-mode discovery cannot see the just-scaffolded cells/asmsmokecell and
  # --cells would fail "unknown cell". Forcing conventional mode matches the
  # consumer scenario this gate is actually validating. (#1560 / PR #1814.)
  go run ./cmd/gocell scaffold assembly \
    --layout=conventional \
    --id="${ASM_ID}" \
    --cells="${CELL_ID}" \
    --team=scaffoldsmoke \
    --role=maintainer \
    --deploy=k8s

  # Auto-generate ran inside `scaffold assembly`; verify the result builds.
  go build -o /dev/null "./cmd/${ASM_ID}/..."

  # SCAFFOLD-RUN-RUNTIME-SMOKE: runnable stub must block on ctx.Done() until
  # SIGTERM/SIGINT rather than returning immediately with an error. A blocking
  # stub lets `go run ./cmd/{id}` start cleanly and wait for a signal.
  # Pure-POSIX background-kill loop — no external deps (timeout/gtimeout/perl).
  # timeout exits 124 when it kills the child; any other exit code means the
  # stub returned early (0 = already exited cleanly, 1 = error, etc.).
  echo "verify-scaffold-assembly: smoke gate ASM_ID=$ASM_ID"

  set +e
  go run "./cmd/${ASM_ID}/..." &
  GO_PID=$!
  SLEPT=0
  RC=""
  while [ "$SLEPT" -lt 5 ]; do
    if ! kill -0 "$GO_PID" 2>/dev/null; then
      # Process already exited — capture its exit code.
      wait "$GO_PID"
      RC=$?
      break
    fi
    sleep 1
    SLEPT=$((SLEPT + 1))
  done
  if [ -z "$RC" ]; then
    # Still running after 5 s — treat as success (124-equivalent).
    kill -TERM "$GO_PID" 2>/dev/null
    sleep 1
    kill -KILL "$GO_PID" 2>/dev/null
    wait "$GO_PID" 2>/dev/null
    RC=124
  fi
  set -e

  if [ "$RC" -ne 124 ]; then
    echo "FAIL: verify-scaffold-assembly — expected timeout-killed (exit 124), got exit code $RC" >&2
    echo "The runnable stub must block on ctx.Done() rather than returning immediately." >&2
    echo "See SCAFFOLD-RUN-RUNTIME-SMOKE and kernel/assembly/gentpl/scaffold-run-go.tpl" >&2
    exit 1
  fi
  echo "OK: runnable stub blocks correctly (timeout-killed, RC=124)"

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
