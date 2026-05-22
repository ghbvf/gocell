#!/usr/bin/env bash
# backfill-priority-labels.sh — one-shot migration of Project v2 Priority
# field values to `pri-pX` labels for backlog-labeled issues (open + closed).
#
# Idempotent: if the `pri-pX` label is already present, the issue is skipped.
# Dry-run by default; pass --apply to actually mutate issues.
#
# Run locally with a token that has `project` + `repo` scopes; not wired into
# CI (one-shot migration tool, not a recurring gate).
#
# Re-runnable for label-drift recovery (still idempotent).

set -euo pipefail

PROJECT_NUMBER=3
PROJECT_OWNER=ghbvf
REPO=ghbvf/gocell

MODE=dry-run
MODE_SET=0
SINGLE_ISSUE=""
SELF_TEST=0

usage() {
  cat <<'EOF'
Usage: hack/backfill-priority-labels.sh [--dry-run|--apply] [--issue N]
       hack/backfill-priority-labels.sh --self-test

  --dry-run   (default) Print intended edits without modifying issues.
  --apply     Apply the edits (mutually exclusive with --dry-run).
  --issue N   Restrict to a single issue number (positive integer).
  --self-test Run offline regression test of jq + classification logic
              against an embedded fixture (no gh calls). Exit 0 on PASS.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run)
      if [[ $MODE_SET -eq 1 && "$MODE" != "dry-run" ]]; then
        echo "Error: --dry-run and --apply are mutually exclusive" >&2; exit 2
      fi
      MODE=dry-run; MODE_SET=1; shift ;;
    --apply)
      if [[ $MODE_SET -eq 1 && "$MODE" != "apply" ]]; then
        echo "Error: --dry-run and --apply are mutually exclusive" >&2; exit 2
      fi
      MODE=apply; MODE_SET=1; shift ;;
    --issue)
      [[ $# -ge 2 ]] || { usage >&2; exit 2; }
      SINGLE_ISSUE="$2"; shift 2 ;;
    --self-test) SELF_TEST=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [[ -n "${SINGLE_ISSUE}" && ! "${SINGLE_ISSUE}" =~ ^[0-9]+$ ]]; then
  echo "Error: --issue argument must be a positive integer (got: '${SINGLE_ISSUE}')" >&2
  exit 2
fi

# Classify a (project item priority value, current labels CSV) pair into one of
# {skip, dry, apply, unset, badvalue}. Pure function — used by main loop and
# self-test. Echoes the disposition and (for dry/apply) the target label.
classify() {
  local priority="$1" existing="$2" mode="$3"
  if [[ -z "${priority}" || "${priority}" == "null" ]]; then
    echo "unset"; return
  fi
  if [[ ! "${priority}" =~ ^P[0-3]$ ]]; then
    echo "badvalue ${priority}"; return
  fi
  local target_label="pri-p${priority#P}"
  case ",${existing}," in
    *",${target_label},"*) echo "skip ${target_label}"; return ;;
  esac
  case "${mode}" in
    dry-run) echo "dry ${target_label}" ;;
    apply)   echo "apply ${target_label}" ;;
  esac
}

if [[ $SELF_TEST -eq 1 ]]; then
  echo "Running offline self-test..."
  command -v jq >/dev/null 2>&1 || { echo "jq not in PATH" >&2; exit 1; }
  fixture_items=$(cat <<'JSON'
{"items":[
  {"content":{"type":"Issue","number":100,"url":"https://github.com/ghbvf/gocell/issues/100","state":"OPEN"},"priority":"P0"},
  {"content":{"type":"Issue","number":101,"url":"https://github.com/ghbvf/gocell/issues/101","state":"OPEN"},"Priority":"P1"},
  {"content":{"type":"Issue","number":102,"url":"https://github.com/ghbvf/gocell/issues/102","state":"CLOSED"}},
  {"content":{"type":"Issue","number":103,"url":"https://github.com/ghbvf/other/issues/103","state":"OPEN"},"priority":"P2"},
  {"content":{"type":"Issue","number":104,"url":"https://github.com/ghbvf/gocell/issues/104","state":"OPEN"},"priority":"BOGUS"}
]}
JSON
  )
  # jq path must coerce both .priority (camelCase, default gh) and .Priority
  # (PascalCase, the field display name) to a single value. Self-test feeds
  # both shapes; both must classify identically when the value is the same.
  rows=$(printf '%s' "$fixture_items" | jq -r '
    .items[]
    | select(.content.type == "Issue")
    | select((.content.url // "") | contains("/gocell/"))
    | [.content.number, ((.priority // .Priority) // "")] | @tsv
  ')
  # Expected: 100 P0, 101 P1, 102 (empty), 104 BOGUS (issue 103 filtered by URL).
  # jq @tsv emits trailing tab when the second column is empty.
  expected=$(printf '100\tP0\n101\tP1\n102\t\n104\tBOGUS')
  if [[ "$rows" != "$expected" ]]; then
    echo "FAIL: jq output diverges from expected" >&2
    echo "--- expected ---" >&2; echo "$expected" >&2
    echo "--- got ---" >&2; echo "$rows" >&2
    exit 1
  fi
  # Spot-check classify() across all dispositions.
  cases=(
    "P0||dry-run|dry pri-p0"
    "P1|backlog,cap-05|dry-run|dry pri-p1"
    "P1|backlog,cap-05,pri-p1|dry-run|skip pri-p1"
    "P2|backlog|apply|apply pri-p2"
    "|backlog|dry-run|unset"
    "BOGUS|backlog|dry-run|badvalue BOGUS"
  )
  for case_line in "${cases[@]}"; do
    IFS='|' read -r p e m want <<<"$case_line"
    got=$(classify "$p" "$e" "$m")
    if [[ "$got" != "$want" ]]; then
      echo "FAIL: classify('$p','$e','$m') = '$got', want '$want'" >&2
      exit 1
    fi
  done
  echo "PASS"
  exit 0
fi

command -v gh >/dev/null 2>&1 || { echo "gh CLI not in PATH" >&2; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "jq not in PATH" >&2; exit 1; }

echo "Mode: ${MODE}  Project: ${PROJECT_OWNER}/#${PROJECT_NUMBER}  Repo: ${REPO}"
if [[ -n "${SINGLE_ISSUE}" ]]; then
  echo "Restricted to issue #${SINGLE_ISSUE}"
fi

tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

echo "Fetching Project items..."
gh project item-list "${PROJECT_NUMBER}" --owner "${PROJECT_OWNER}" \
  --limit 1000 --format json > "${tmpdir}/items.json"

echo "Fetching backlog issue labels..."
gh issue list --repo "${REPO}" --label backlog --state all --limit 1000 \
  --json number,labels > "${tmpdir}/labels.json"

# Flat TSV keyed by issue number — keeps the script bash 3 compatible (macOS
# default /bin/bash has no associative arrays). awk lookup at O(N) per call
# is acceptable for ~1000-row label tables.
jq -r '.[] | [.number, ([.labels[].name] | join(","))] | @tsv' "${tmpdir}/labels.json" \
  > "${tmpdir}/labels.tsv"

# lookup_labels <issue_number>
# Echoes labels CSV (possibly empty) on success; returns non-zero if the
# issue is absent from labels.tsv (i.e., not in the backlog-labeled set).
lookup_labels() {
  awk -F'\t' -v n="$1" '
    $1 == n { print $2; found = 1; exit }
    END    { exit !found }
  ' "${tmpdir}/labels.tsv"
}

# Counters. Invariant: considered == skipped + would_add + added +
# unset_priority + bad_value + not_backlog. `filtered` is items dropped by
# --issue filter and is not part of `considered` (we count post-filter only).
considered=0
filtered=0
skipped=0
would_add=0
added=0
unset_priority=0
bad_value=0
not_backlog=0

while IFS=$'\t' read -r number priority state; do
  if [[ -n "${SINGLE_ISSUE}" && "${number}" != "${SINGLE_ISSUE}" ]]; then
    filtered=$((filtered+1))
    continue
  fi
  considered=$((considered+1))

  if existing="$(lookup_labels "${number}")"; then
    : # found in labels.tsv
  else
    echo "[skip] #${number} not in backlog-labeled set (priority='${priority}')"
    not_backlog=$((not_backlog+1))
    continue
  fi

  disposition=$(classify "${priority}" "${existing}" "${MODE}")
  verb="${disposition%% *}"
  label="${disposition#* }"

  case "${verb}" in
    unset)
      unset_priority=$((unset_priority+1)) ;;
    badvalue)
      echo "[warn] #${number} unexpected Priority value '${label}' — skipped"
      bad_value=$((bad_value+1)) ;;
    skip)
      echo "[skip] #${number} already has ${label}"
      skipped=$((skipped+1)) ;;
    dry)
      echo "[dry]  #${number} (${state}) would add ${label}"
      would_add=$((would_add+1)) ;;
    apply)
      gh issue edit "${number}" --repo "${REPO}" --add-label "${label}" >/dev/null
      echo "[ok]   #${number} (${state}) + ${label}"
      added=$((added+1))
      sleep 0.3 ;;
  esac
done < <(jq -r '
  .items[]
  | select(.content.type == "Issue")
  | select((.content.url // "") | contains("/gocell/"))
  | [.content.number, ((.priority // .Priority) // ""), (.content.state // "")] | @tsv
' "${tmpdir}/items.json")

echo "---"
echo "considered=${considered} skipped=${skipped} would_add=${would_add} added=${added}"
echo "unset_priority=${unset_priority} bad_value=${bad_value} not_backlog=${not_backlog} filtered=${filtered}"

# Reconcile: warn the operator if the bucket sum does not match. This guards
# against future drift in the loop logic.
sum=$((skipped + would_add + added + unset_priority + bad_value + not_backlog))
if [[ ${sum} -ne ${considered} ]]; then
  echo "WARN: bucket reconciliation failed: ${sum} != ${considered}" >&2
fi
