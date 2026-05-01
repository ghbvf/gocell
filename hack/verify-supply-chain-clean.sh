#!/usr/bin/env bash
# verify-supply-chain-clean enforces "no scanner bypass" as a static gate.
#
# Supply-chain scanners (govulncheck, Semgrep, CodeQL) are configured in
# .github/workflows/security-vuln.yml + security-static.yml to fail on any
# finding, with no --ignore / --exclude / paths-ignore flags. This script
# guards against drift: it forbids global ignore files from appearing in the
# repo, and rejects any future addition of bypass flags to the security
# workflows. It also enforces that line-level Semgrep suppressions
# (`// nosemgrep: <rule-id>`) include a reason, mirroring nolintlint.

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
# (covered in step [3]).
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
  # Extract paths-ignore entries; reject any line that is not generated/* or
  # vendor/*. yq is not assumed; rely on grep + line discipline.
  if grep -nE '^\s*-\s*' "$codeql_cfg" \
       | grep -A0 -B0 'paths-ignore' >/dev/null 2>&1 \
       || grep -nE 'paths-ignore' "$codeql_cfg" >/dev/null 2>&1; then
    # Lines under paths-ignore: "  - <pattern>"
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
  # Only fail if there's a real Go/sh/yaml hit. The verify script itself uses
  # the literal `nosemgrep:` in regex strings — those are in this file only
  # and matched by the `--exclude-dir` path won't exclude us, so allow our
  # own grep-pattern lines through.
  combined=$(printf '%s' "$combined" | grep -vF 'verify-supply-chain-clean.sh' || true)
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
