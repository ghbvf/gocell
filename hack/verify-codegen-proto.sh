#!/usr/bin/env bash
# Proto codegen drift gate.
#
# Regenerates generated/contracts/grpc/**/*.pb.go from contracts/grpc/**.proto
# (via `make proto-gen` -> buf + protoc-gen-go + protoc-gen-go-grpc) and fails if
# the working tree differs from what is committed — a stale or hand-edited
# .pb.go, or a new .proto whose output was never committed, turns the build red.
#
# The pipeline is hermetic via `go run` (see Makefile `proto-gen` + buf.gen.yaml),
# so this runs anywhere setup-go + the module cache are available with no extra
# buf/protoc install. It DOES require network on the first run (buf + the grpc
# plugin are fetched via `go run …@version`); the build cache covers reruns.
# Unlike the K#04/K#06 `gocell verify codegen-*` gates, proto generation is
# buf-native (not a gocell subcommand), so drift is detected with `git status`
# rather than a sandbox worktree.
#
# Blind spot: `buf generate` only writes — it never deletes. If a .proto is
# removed, its now-orphaned .pb.go is NOT regenerated and stays byte-identical,
# so this gate passes. Deleting a .proto therefore requires manually removing
# its generated .pb.go in the same change (documented in contracts/grpc/README.md).
#
# Pattern: kubernetes/kubernetes hack/lib/verify-generated.sh (regenerate + diff).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

make proto-gen

# --porcelain surfaces both modified (committed-but-stale) and untracked (new
# .proto with uncommitted output) files under the generated grpc tree.
DRIFT="$(git status --porcelain -- generated/contracts/grpc)"
if [[ -n "${DRIFT}" ]]; then
  echo >&2 "ERROR: generated proto code is out of sync with contracts/grpc/**.proto."
  echo >&2 "Run 'make proto-gen' and commit the result. Drift:"
  echo "${DRIFT}" >&2
  exit 1
fi
