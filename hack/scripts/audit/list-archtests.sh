#!/usr/bin/env bash
# list-archtests.sh — raw audit listing of every `// INVARIANT: <ID>` anchor
# in tools/archtest/*_test.go (top-level only).
#
# This is a stdout-only, on-demand view; there is no persisted artifact and
# no derived parse step. The reverse index from rule ID → file is held by
# the anchors themselves; the archtest gates enforce both presence and
# canonical ID grammar:
#
#   - INVENTORY-ANCHOR-REQUIRED-01: every file carries an anchor
#   - INVENTORY-ANCHOR-VALID-ID-01: every anchor matches the ID grammar
#
# The grammar is defined exactly once, in
# tools/archtest/inventory_anchor_required_test.go (see
# `inventoryAnchorIDPattern`). This script intentionally does NOT
# re-implement parsing — it grep-prints raw comment lines so there is no
# second grammar to drift.
#
# For rule-ID lookup, prefer direct grep:
#
#   grep -rn 'INVARIANT: <ID>' tools/archtest/
#
# Run this script when you want a sorted, file-by-file roll-up.
#
# ref: kubernetes/kubernetes hack/update-codegen.sh — annotation-driven
#      discovery (`+k8s:deepcopy-gen=` → glob source files); we apply the
#      same pattern with `// INVARIANT: <ID>` as the anchor, then leave the
#      semantics to the Go-side gate.

set -euo pipefail

# Pin sort/awk locale so the output order is bit-identical on macOS dev
# boxes and Linux runners.
export LC_ALL=C

repo_root="$(git rev-parse --show-toplevel 2>/dev/null)" || {
  echo "list-archtests.sh: must run inside a git work tree" >&2
  exit 1
}
cd "${repo_root}"

archtest_dir="tools/archtest"
archtest_depth="$(awk -F/ '{print NF + 1}' <<<"${archtest_dir}")"

# Top-level _test.go only — subpackages under tools/archtest/internal/ are
# out of scope (they have their own concerns and are governed separately).
files=()
while IFS= read -r line; do files+=("${line}"); done < <(
  git ls-files -- "${archtest_dir}/" \
    | awk -F/ -v want="${archtest_depth}" 'NF == want && /_test\.go$/' \
    | sort
)

# Emit `<file>:<line>:<comment>` for every `// INVARIANT: …` /
# `// - INVARIANT: …` line. No grammar parsing, no theme grouping, no ID
# extraction — that responsibility lives in the archtest gate.
for f in "${files[@]}"; do
  grep -nHE '^[[:space:]]*//[[:space:]]*-?[[:space:]]*INVARIANT:' "${f}" 2>/dev/null || true
done
