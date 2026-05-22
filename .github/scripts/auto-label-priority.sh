#!/usr/bin/env bash
# auto-label-priority.sh — applies pri-pX / pri-missing labels to a new
# backlog issue based on the rendered Issue Forms Priority dropdown.
#
# Required env:
#   ISSUE_NUMBER  — the issue number (numeric)
#   REPO          — owner/repo string
#   ISSUE_BODY    — full rendered issue body (may be empty string)
#   GH_TOKEN      — consumed by gh CLI for issues: write
#
# Behavior:
#   1. Parse `### Priority\n\nP<n>` section from ISSUE_BODY (Web UI path).
#   2. Match found → converge priority dimension: remove any other
#      `pri-(p[0-3]|missing)` label, then `--add-label pri-pX`.
#   3. Match not found → CLI / non-template path:
#      a. If issue already carries `pri-p[0-3]`, respect creator intent
#         and skip (no sentinel).
#      b. Otherwise apply `pri-missing` sentinel so the issue is
#         discoverable via `gh issue list --label pri-missing`.
#
# Offline test: .github/scripts/auto-label-priority-test.sh.

set -euo pipefail

: "${ISSUE_NUMBER:?ISSUE_NUMBER required}"
: "${REPO:?REPO required}"
# ISSUE_BODY uses the no-colon `:?` form (errors only when *unset*, not when
# empty) — a webhook event for an issue with no body produces an empty string
# that we still need to process via the sentinel path.
: "${ISSUE_BODY?ISSUE_BODY required (may be empty string)}"

# Issue Forms render dropdown sections as "### Priority\n\nP<n>".
# Cap body at 64KB (defensive against unbounded bodies); the Priority
# section is always near the top so truncation is safe. Anchor on the
# section header + require the first non-blank line after to be exactly
# P[0-3]; this rejects nested headers like "### Priority Notes" inside
# textarea field content.
priority=$(printf '%s\n' "$ISSUE_BODY" \
  | head -c 65536 \
  | awk '
      /^### Priority[[:space:]]*$/ { found=1; next }
      found && /^[[:space:]]*$/ { next }
      found { print; exit }
    ' \
  | tr -d '[:space:]')

if [[ ! "$priority" =~ ^P[0-3]$ ]]; then
  # No dropdown render — CLI-creation path. Respect explicit pri-pX from
  # `--label`; otherwise tag with `pri-missing` sentinel.
  echo "No Priority value found in issue body (got: '${priority}')."
  if gh issue view "$ISSUE_NUMBER" --repo "$REPO" --json labels \
       -q '.labels[].name' | grep -qE '^pri-p[0-3]$'; then
    echo "Creator already attached an explicit pri-pX — no sentinel needed."
    exit 0
  fi
  echo "Applying pri-missing sentinel to issue #$ISSUE_NUMBER."
  gh issue edit "$ISSUE_NUMBER" --repo "$REPO" --add-label "pri-missing"
  exit 0
fi

label="pri-p${priority#P}"
if [[ ! "$label" =~ ^pri-p[0-3]$ ]]; then
  echo "Computed label '${label}' fails sanity check — aborting." >&2
  exit 1
fi

echo "Applying label $label to issue #$ISSUE_NUMBER (converging priority dimension)"

# Pull current labels in a standalone call so a real gh failure still aborts
# under `set -euo pipefail`. The downstream awk pass cannot exit non-zero on
# no-match (unlike `grep`), so the pipeline is safe whether the issue has
# stale pri-* labels or not — this is the fix for the F3-new regression that
# previously broke the Web UI clean-create path.
current_labels=$(gh issue view "$ISSUE_NUMBER" --repo "$REPO" \
    --json labels -q '.labels[].name')
stale_labels=$(printf '%s\n' "$current_labels" \
  | awk -v target="$label" '/^pri-(p[0-3]|missing)$/ && $0 != target')

if [[ -n "$stale_labels" ]]; then
  while IFS= read -r stale; do
    [[ -z "$stale" ]] && continue
    echo "Removing stale priority label '$stale'"
    gh issue edit "$ISSUE_NUMBER" --repo "$REPO" --remove-label "$stale"
  done <<< "$stale_labels"
fi

gh issue edit "$ISSUE_NUMBER" --repo "$REPO" --add-label "$label"
echo "Applied $label to issue #$ISSUE_NUMBER"
