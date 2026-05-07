#!/usr/bin/env bash
# verify-archtest-inventory: regenerate docs/audit/archtest-inventory.md and
# fail if it differs from the committed copy. Update path:
#   bash scripts/audit/list-archtests.sh > docs/audit/archtest-inventory.md
#
# The inventory is a derived view of `// INVARIANT: <ID>` anchors (and
# fallback bare IDs in legacy single-rule files). Hand-edits are rejected.
#
# ref: kubernetes/kubernetes hack/lib/verify-generated.sh — regenerate +
#      `git diff --exit-code` dual-rail
# ref: golangci-lint Makefile fast_check_generated — same three-line pattern

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  echo "verify-archtest-inventory: must run inside a git work tree" >&2
  exit 1
fi

target="docs/audit/archtest-inventory.md"
generator="scripts/audit/list-archtests.sh"

if [[ ! -f "${generator}" ]]; then
  echo "verify-archtest-inventory: generator missing at ${generator}" >&2
  exit 1
fi
if [[ ! -f "${target}" ]]; then
  echo "verify-archtest-inventory: inventory missing at ${target}" >&2
  exit 1
fi

actual_dir="$(mktemp -d)"
trap 'rm -rf "${actual_dir}"' EXIT
actual="${actual_dir}/archtest-inventory.md"

bash "${generator}" >"${actual}"

if ! diff -u "${target}" "${actual}"; then
  cat >&2 <<MSG
verify-archtest-inventory: ${target} is stale.
Regenerate with:
  bash ${generator} > ${target}
MSG
  exit 1
fi
