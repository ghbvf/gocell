#!/usr/bin/env bash
# Offline fixture-driven test for auto-label-priority.sh.
#
# Stubs the `gh` CLI via PATH; each case sets:
#   - ISSUE_BODY (rendered issue body)
#   - GH_STUB_LABELS (comma-separated list of labels the stub returns
#     when the script asks for current labels)
# then runs the target script and diffs the recorded `gh issue edit`
# operations against an expected sequence.
#
# Covered branches:
#   A — Web UI clean create: body has Priority section, only `backlog`
#       label exists  →  single --add-label pri-pX
#   B — CLI create with explicit pri-pX, no body Priority section
#       →  sentinel skipped, no edits at all
#   C — CLI create with no priority signal: no body, no pri-* label
#       →  --add-label pri-missing
#   D — Conflicting CLI+body (body P1, --label pri-p3 on create)
#       →  --remove-label pri-p3, --add-label pri-p1
#   E — Issue carries pri-missing from a prior path, body now has P2
#       →  --remove-label pri-missing, --add-label pri-p2
#   F — Idempotency: body has P1, label already has pri-p1
#       →  single --add-label pri-p1 (gh treats existing as no-op)
#
# Cases A and F together pin the F3-new regression: without the awk
# pipeline fix the stale-label sweep aborts the script under
# `set -euo pipefail` when no pri-* labels exist (grep no-match → 1).

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
target_script="${script_dir}/auto-label-priority.sh"
[[ -x "$target_script" ]] || { echo "Missing $target_script" >&2; exit 1; }

stub_dir=$(mktemp -d)
trap 'rm -rf "$stub_dir"' EXIT

cat > "${stub_dir}/gh" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  issue)
    case "${2:-}" in
      view)
        # gh issue view N --repo R --json labels -q '.labels[].name'
        # Stub returns the GH_STUB_LABELS env var (comma → newline).
        if [[ -n "${GH_STUB_LABELS:-}" ]]; then
          printf '%s\n' "${GH_STUB_LABELS}" | tr ',' '\n'
        fi
        ;;
      edit)
        # gh issue edit N --repo R --add-label X | --remove-label X
        shift 2
        echo "edit $*" >> "${GH_STUB_LOG:?GH_STUB_LOG required}"
        ;;
      *) echo "stub: unexpected gh issue $*" >&2; exit 1 ;;
    esac
    ;;
  *) echo "stub: unexpected gh $*" >&2; exit 1 ;;
esac
STUB
chmod +x "${stub_dir}/gh"

pass=0
fail=0

run_case() {
  local name="$1" body="$2" labels="$3" expected_ops="$4"
  local log
  log=$(mktemp)
  if ISSUE_NUMBER=999 REPO=test/test ISSUE_BODY="$body" \
       GH_STUB_LABELS="$labels" GH_STUB_LOG="$log" \
       GH_TOKEN=stub \
       PATH="${stub_dir}:${PATH}" \
       bash "$target_script" >/dev/null 2>&1; then
    :
  else
    local rc=$?
    echo "FAIL [$name] script exited $rc"
    rm -f "$log"
    fail=$((fail+1))
    return
  fi
  local actual
  actual=$(cat "$log")
  rm -f "$log"
  if [[ "$actual" != "$expected_ops" ]]; then
    echo "FAIL [$name]"
    echo "  --- expected ---"
    printf '%s\n' "$expected_ops" | sed 's/^/  /'
    echo "  --- got ---"
    printf '%s\n' "$actual" | sed 's/^/  /'
    fail=$((fail+1))
    return
  fi
  echo "PASS [$name]"
  pass=$((pass+1))
}

# Body fixtures use printf for explicit newline control.
body_p1=$(printf '### Priority\n\nP1\n\n### 现状\n\n...')
body_p2=$(printf '### Priority\n\nP2\n')
body_none=$(printf 'just some text\n\nno priority section here\n')
body_empty=""
# Nested header inside textarea should NOT match (regression guard for awk).
body_nested=$(printf '### 修复方向\n\nblah\n### Priority Notes\n\nP9 should be ignored\n')

run_case "A: Web UI clean create" \
  "$body_p2" \
  "backlog" \
  "edit 999 --repo test/test --add-label pri-p2"

run_case "B: CLI explicit pri-pX skips sentinel" \
  "$body_none" \
  "backlog,pri-p3" \
  ""

run_case "C: CLI fail-open → pri-missing" \
  "$body_none" \
  "backlog" \
  "edit 999 --repo test/test --add-label pri-missing"

run_case "D: conflicting CLI+body converges" \
  "$body_p1" \
  "backlog,pri-p3" \
  "$(printf 'edit 999 --repo test/test --remove-label pri-p3\nedit 999 --repo test/test --add-label pri-p1')"

run_case "E: drop pri-missing on converge" \
  "$body_p2" \
  "backlog,pri-missing" \
  "$(printf 'edit 999 --repo test/test --remove-label pri-missing\nedit 999 --repo test/test --add-label pri-p2')"

run_case "F: same-label re-apply" \
  "$body_p1" \
  "backlog,pri-p1" \
  "edit 999 --repo test/test --add-label pri-p1"

run_case "G: empty body → pri-missing" \
  "$body_empty" \
  "backlog" \
  "edit 999 --repo test/test --add-label pri-missing"

run_case "H: nested header inside textarea is rejected" \
  "$body_nested" \
  "backlog" \
  "edit 999 --repo test/test --add-label pri-missing"

echo
echo "Summary: ${pass} pass, ${fail} fail"
if [[ $fail -gt 0 ]]; then
  exit 1
fi
