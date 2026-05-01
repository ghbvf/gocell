#!/usr/bin/env bash
# verify-supply-chain-clean is a drift-detection / hygiene gate. It rejects
# accidental additions of supply-chain bypass surfaces:
#   - Global ignore files (.govulncheckignore / .semgrepignore)
#   - Bypass flags (--exclude / --exclude-rule / --ignore / -skip) in the
#     security workflow files
#   - CodeQL paths-ignore that match anything beyond generated/ or vendor/
#   - Bare line-level `nosemgrep:` without a `// <reason>` comment
#
# This is NOT a fail-closed boundary against a coordinated malicious PR: it
# runs from the PR head, so a single PR could weaken both the policy and
# this checker simultaneously and still pass governance.yml. True fail-closed
# enforcement against intentional bypass requires PR review by the
# maintainer (this is a single-reviewer project — that review IS the
# boundary). The gate's value is catching accidental drift.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

fail=0

echo "[1/4] forbidden global-ignore files"
for f in .govulncheckignore .semgrepignore; do
  if [[ -e "$f" ]]; then
    printf 'FAIL: %s exists; supply-chain bypass forbidden\n' "$f" >&2
    fail=1
  fi
done

echo "[2/4] bypass flags in security workflows"
workflows=(
  .github/workflows/security-vuln.yml
  .github/workflows/security-static.yml
)
# Patterns intentionally narrow: govulncheck uses -skip; Semgrep uses
# --exclude / --exclude-rule; CodeQL would surface ignores via a config file
# (covered in step [3]). Note: ' -skip' also matches `go test -skip=<pattern>`
# — `go test -skip` is intentionally forbidden in security workflow files.
bypass_patterns=(
  '\-\-exclude(=| )'
  '\-\-exclude-rule'
  '\-\-ignore(=| )'
  ' -skip(=| )'
)
# Strip YAML comment lines (anything from `#` onward) before matching so
# documentation that mentions the forbidden flags by name does not trip the
# gate. Comments are removed in-line, not whole-line, so a directive followed
# by a trailing `# explanation` still counts.
for wf in "${workflows[@]}"; do
  if [[ ! -f "$wf" ]]; then
    printf 'FAIL: expected workflow %s not found\n' "$wf" >&2
    fail=1
    continue
  fi
  stripped=$(sed -E 's/(^|[[:space:]])#.*$//' "$wf")
  for pat in "${bypass_patterns[@]}"; do
    if printf '%s\n' "$stripped" | grep -nE "$pat" >/dev/null 2>&1; then
      printf 'FAIL: bypass flag matching /%s/ in %s (after stripping comments)\n' "$pat" "$wf" >&2
      printf '%s\n' "$stripped" | grep -nE "$pat" >&2 || true
      fail=1
    fi
  done
done

echo "[3/4] CodeQL paths-ignore (only generated/ or vendor/ allowed)"
codeql_cfg=.github/codeql/codeql-config.yml
if [[ -f "$codeql_cfg" ]]; then
  # Reject flow-style paths-ignore (`paths-ignore: [a, b]`) outright — the
  # block-style awk parser below cannot inspect entries inside `[...]`.
  if grep -E 'paths-ignore:.*\[' "$codeql_cfg" >/dev/null 2>&1; then
    printf 'FAIL: %s uses flow-style paths-ignore — only block style supported by verify gate\n' \
      "$codeql_cfg" >&2
    fail=1
  fi

  # Block-style: lines under `paths-ignore:` of the form `  - <pattern>`.
  # Reject any entry that is not generated/* or vendor/*.
  if grep -nE 'paths-ignore' "$codeql_cfg" >/dev/null 2>&1; then
    bad=$(awk '
      /paths-ignore:/ { in_block=1; next }
      in_block && /^[^[:space:]-]/ { in_block=0 }
      in_block && /^\s*-\s*/ {
        line=$0
        sub(/^\s*-\s*/, "", line)
        gsub(/^["'\'']|["'\'']$/, "", line)
        if (line !~ /^(generated\/|vendor\/)/) print line
      }
    ' "$codeql_cfg")
    if [[ -n "$bad" ]]; then
      printf 'FAIL: %s paths-ignore must only match generated/ or vendor/; offending entries:\n%s\n' \
        "$codeql_cfg" "$bad" >&2
      fail=1
    fi
  fi
fi

echo "[4/4] line-level nosemgrep must include reason"
# Format required: `// nosemgrep: <rule-id> // <reason>`
# Forbid: bare `nosemgrep:` with no rule, or rule but no `// <reason>` follow-up.
# Skip vendor/, generated/, and node_modules/ if present.
if find_out=$(grep -rnE 'nosemgrep:' \
                --include='*.go' --include='*.sh' --include='*.yml' --include='*.yaml' \
                --exclude-dir=vendor --exclude-dir=generated --exclude-dir=node_modules \
                . 2>/dev/null); then
  bad=$(printf '%s\n' "$find_out" | grep -vE 'nosemgrep:[[:space:]]*[A-Za-z0-9._/-]+([[:space:]]+//[[:space:]]+\S.*)' || true)
  # Also flag bare `nosemgrep:` with nothing after
  bare=$(printf '%s\n' "$find_out" | grep -E 'nosemgrep:[[:space:]]*$' || true)
  combined=""
  [[ -n "$bad" ]] && combined+="$bad"$'\n'
  [[ -n "$bare" ]] && combined+="$bare"$'\n'
  # The verify script itself contains the literal `nosemgrep:` in regex
  # strings; suppress matches against this script's own path. Use
  # BASH_SOURCE so the filter survives a future rename of this file.
  self_name="$(basename "${BASH_SOURCE[0]}")"
  combined=$(printf '%s' "$combined" | grep -vF "$self_name" || true)
  if [[ -n "$combined" ]]; then
    printf 'FAIL: nosemgrep must be `// nosemgrep: <rule-id> // <reason>`; offending lines:\n%s\n' \
      "$combined" >&2
    fail=1
  fi
fi

if (( fail )); then
  exit 1
fi
echo "OK"
