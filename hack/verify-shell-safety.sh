#!/usr/bin/env bash
# verify-shell-safety: enforce strict mode (`set -euo pipefail`) on project-owned
# shell scripts so silent CI failures cannot recur.
#
# Why: bash defaults swallow command failures (`set +e`), unset variables
# (`set +u`), and pipeline mid-failures (no `pipefail`). A CI step that just
# `bash some-script.sh` would log a red error and still exit 0. `set -euo
# pipefail` is the one-line floor that turns those into hard failures.
#
# Scope:
#   - Scans scripts/, hack/, tests/ for *.sh
#   - Excludes hack/lib/ (sourced libraries; setting strict-mode there would
#     pollute the caller's shell)
#   - Excludes this verifier itself (its own check is its `set -euo pipefail`
#     on line 19)
#   - Does NOT scan .specify/ (upstream spec-kit artifacts; would be clobbered
#     on next sync)
#
# Rule: shebang + first 30 lines must contain top-level `set ... -e`, `set ... -u`,
# and `pipefail` (any order, multi-line splits accepted; matches must start at
# line head — not nested inside a function — but all three flags required).

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

scan_dirs=("scripts" "hack" "tests")
exclude_patterns=("hack/lib/" "hack/verify-shell-safety.sh")

fail=0
checked=0

while IFS= read -r script; do
  skip=0
  for pat in "${exclude_patterns[@]}"; do
    case "$script" in
      *"$pat"*) skip=1; break ;;
    esac
  done
  (( skip )) && continue

  checked=$((checked + 1))
  head_block=$(head -n 30 "$script")
  # All three checks anchor at line head (`^set`) so `pipefail` mentioned in a
  # comment or string cannot satisfy the gate.
  if ! grep -qE '^set[[:space:]]+-[a-zA-Z]*e' <<<"$head_block" \
     || ! grep -qE '^set[[:space:]]+-[a-zA-Z]*u' <<<"$head_block" \
     || ! grep -qE '^set[[:space:]].*pipefail' <<<"$head_block"; then
    printf 'FAIL: %s missing `set -euo pipefail` in first 30 lines\n' "$script" >&2
    fail=1
  fi
done < <(find "${scan_dirs[@]}" -type f -name '*.sh' 2>/dev/null | sort)

if (( fail )); then
  echo "" >&2
  echo "Add 'set -euo pipefail' immediately after the shebang line." >&2
  echo "Library files that are sourced (not executed) belong in hack/lib/." >&2
  exit 1
fi

printf 'OK (%d scripts checked)\n' "$checked"
