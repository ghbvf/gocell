#!/usr/bin/env bash
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

  # SCAFFOLD-RUN-RUNTIME-SMOKE: runnable stub must block on ctx.Done() until
  # SIGTERM/SIGINT rather than returning immediately with an error. A blocking
  # stub lets `go run ./cmd/{id}` start cleanly and wait for a signal.
  # We use `timeout` (Linux/CI) with fallback to `gtimeout` (macOS + brew
  # coreutils) or a plain Perl sleep-kill loop. timeout exits 124 when it
  # kills the child; any other exit code means the stub returned early
  # (0 = already exited cleanly, 1 = error, etc.).
  TIMEOUT_CMD=""
  if command -v timeout >/dev/null 2>&1; then
    TIMEOUT_CMD="timeout"
  elif command -v gtimeout >/dev/null 2>&1; then
    TIMEOUT_CMD="gtimeout"
  fi

  echo "verify-scaffold-assembly: using ${TIMEOUT_CMD:-perl-fallback} for runtime smoke (ASM_ID=$ASM_ID)"

  if [ -n "$TIMEOUT_CMD" ]; then
    set +e
    $TIMEOUT_CMD 5 go run "./cmd/${ASM_ID}/..."
    RC=$?
    set -e
  else
    # Portable Perl fallback: run the child, kill it after 5 seconds, exit 124.
    # Note: shell expands ASM_ID before passing the string to Perl.
    set +e
    perl -e '
      my $pkg = shift;
      my $pid = fork();
      if ($pid == 0) { exec("go", "run", $pkg) or exit(127); }
      local $SIG{ALRM} = sub { kill(15, $pid); sleep 1; kill(9, $pid); exit(124); };
      alarm(5);
      waitpid($pid, 0);
      my $exit = ($? >> 8);
      exit($exit);
    ' -- "./cmd/${ASM_ID}/..."
    RC=$?
    set -e
  fi

  if [ "$RC" -ne 124 ]; then
    echo "FAIL: verify-scaffold-assembly — expected timeout-killed (exit 124), got exit code $RC" >&2
    echo "The runnable stub must block on ctx.Done() rather than returning immediately." >&2
    echo "See SCAFFOLD-RUN-RUNTIME-SMOKE and kernel/assembly/gentpl/scaffold-run-go.tpl" >&2
    exit 1
  fi
  echo "OK: runnable stub blocks correctly (timeout-killed with exit 124)"

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
