#!/usr/bin/env bash
# Proto lint + codegen drift gate.
#
# Two enforced checks over contracts/grpc/**.proto (both buf-native, hermetic via
# `go run` — see Makefile + buf.gen.yaml — so they run anywhere setup-go + the
# module cache are available, no buf/protoc install; first run needs network to
# fetch buf + the grpc plugin, the build cache covers reruns):
#
#   1. buf lint (`make proto-lint`): the buf.yaml STANDARD ruleset is an enforced
#      gate, not dead config.
#   2. Drift (regenerate + `git status`): the committed
#      generated/contracts/grpc/**/*.pb.go must equal a from-scratch regeneration.
#
# The drift check deletes the tracked *.pb.go first, then regenerates, so it is a
# CLEAN-SLATE diff that catches all four drift shapes — including orphans, which a
# generate-on-top diff misses (`buf generate` only writes, never deletes):
#   - stale / hand-edited output -> ` M`
#   - new .proto, output never committed -> `??`
#   - DELETED or renamed .proto -> its orphaned .pb.go is not regenerated -> ` D`
# Only buf-owned *.pb.go are cleaned; gocell-generated *_gen.go under the same
# tree (PR-8+) carry a different header and are gated by K#06, untouched here.
#
# Unlike the K#04/K#06 `gocell verify codegen-*` gates, proto codegen is buf-native
# (not a gocell subcommand), so this is a thin regenerate+diff rather than a
# sandbox worktree. A FAILED run may leave the working tree dirty (cleaned outputs
# not restored) — re-run after fixing, or `git restore generated/contracts/grpc`.
#
# Pattern: kubernetes/kubernetes hack/lib/verify-generated.sh (regenerate + diff).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

# 1. Lint (enforces buf.yaml STANDARD).
make proto-lint

# 2. Clean-slate regenerate so orphaned outputs from a removed/renamed .proto
#    surface as a ` D` drift (find -delete is a no-op when no *.pb.go exist yet).
find generated/contracts/grpc -name '*.pb.go' -delete 2>/dev/null || true
make proto-gen

DRIFT="$(git status --porcelain -- generated/contracts/grpc)"
if [[ -n "${DRIFT}" ]]; then
  echo >&2 "ERROR: generated proto code is out of sync with contracts/grpc/**.proto."
  echo >&2 "Run 'make proto-gen' (and 'git rm' any .pb.go whose .proto was deleted), then commit. Drift:"
  echo "${DRIFT}" >&2
  exit 1
fi
