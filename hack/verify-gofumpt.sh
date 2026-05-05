#!/usr/bin/env bash
# verify-gofumpt.sh — formatter-only gate.
#
# .golangci.yml lists gofumpt, gofmt, goimports under formatters.enable, and
# the full `golangci-lint run` invocation in the CI lint shard already
# rejects formatter drift transitively. This gate exposes the same check as
# a standalone `make verify` step so:
#
#   - local `make verify` surfaces formatter drift without rerunning all
#     linters (the full run takes minutes; fmt-only is seconds);
#   - the verify-* gate list mirrors K8s hack/verify-gofmt.sh and signals
#     to reviewers that producer-side formatter compliance is a first-class
#     governance check, not an implicit by-product of lint.
#
# Producer-side counterpart: tools/codegen/render.go FormatGoSource —
# every codegen / scaffold path funnels through the same goimports → gofumpt
# pipeline so generated files start out compliant and never trip this gate.
#
# Fix recipe: `make fmt` (or `golangci-lint fmt ./...`).
#
# ref: kubernetes/kubernetes hack/verify-gofmt.sh — same diff-mode pattern.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

if ! command -v golangci-lint &>/dev/null; then
  cat >&2 <<'EOF'
verify-gofumpt: golangci-lint not in PATH.
Install with:
  * make fmt        (uses the project-pinned version via go run)
  * go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
EOF
  exit 1
fi

diff_output="$(golangci-lint fmt -d ./...)"
if [[ -n "${diff_output}" ]]; then
    echo "formatter drift detected; run 'make fmt' to fix:" >&2
    echo "${diff_output}"
    exit 1
fi
