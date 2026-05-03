#!/usr/bin/env bash
# CATALOG-GRAPH-DRIFT-01 gate.
#
# Regenerates cmd/corebundle/catalog_gen.go in-place and diffs against the
# checked-in copy. Exits 1 (with a clear message) if the file is stale so
# developers know to run `make generate` before committing.
#
# Usage: bash hack/verify-catalog-graph.sh
#
# ref: hack/verify-test-time-literal.sh — same exit-code convention.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

SCRATCH=$(mktemp -d)
trap 'rm -rf "$SCRATCH"' EXIT

cp cmd/corebundle/catalog_gen.go "$SCRATCH/before.go"

go generate ./cmd/corebundle/

if ! diff -q "$SCRATCH/before.go" cmd/corebundle/catalog_gen.go > /dev/null 2>&1; then
  echo "catalog_gen.go drift detected: file does not match current package graph."
  echo "Run 'make generate' and commit the result."
  diff "$SCRATCH/before.go" cmd/corebundle/catalog_gen.go || true
  exit 1
fi

echo "catalog_gen.go is up to date."
