#!/usr/bin/env bash
# backfill-priority-labels.sh — one-shot migration of Project v2 Priority
# field values to `pri-pX` labels for backlog-labeled issues (open + closed).
#
# Idempotent: if the `pri-pX` label is already present, the issue is skipped.
# Dry-run by default; pass --apply to actually mutate issues.
#
# Run locally with a token that has `project` + `repo` scopes; not wired into
# CI (one-shot migration tool, not a recurring gate).

set -euo pipefail

PROJECT_NUMBER=3
PROJECT_OWNER=ghbvf
REPO=ghbvf/gocell

MODE=dry-run
SINGLE_ISSUE=""

usage() {
  cat <<'EOF'
Usage: hack/backfill-priority-labels.sh [--dry-run|--apply] [--issue N]
  --dry-run   (default) Print intended edits without modifying issues.
  --apply     Apply the edits.
  --issue N   Restrict to a single issue number (still honors --dry-run/--apply).
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run) MODE=dry-run; shift ;;
    --apply)   MODE=apply;   shift ;;
    --issue)
      [[ $# -ge 2 ]] || { usage >&2; exit 2; }
      SINGLE_ISSUE="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

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

declare -A current_labels
while IFS=$'\t' read -r number labels_csv; do
  current_labels["${number}"]="${labels_csv}"
done < <(jq -r '.[] | [.number, ([.labels[].name] | join(","))] | @tsv' "${tmpdir}/labels.json")

total=0
skipped=0
would_add=0
added=0
unset_priority=0
not_backlog=0

while IFS=$'\t' read -r number priority state; do
  total=$((total+1))
  if [[ -n "${SINGLE_ISSUE}" && "${number}" != "${SINGLE_ISSUE}" ]]; then
    continue
  fi
  if [[ -z "${priority}" || "${priority}" == "null" ]]; then
    unset_priority=$((unset_priority+1))
    continue
  fi
  if [[ ! "${priority}" =~ ^P[0-3]$ ]]; then
    echo "[warn] #${number} unexpected Priority value '${priority}' — skipped"
    continue
  fi
  target_label="pri-p${priority#P}"

  if [[ -z "${current_labels[${number}]+x}" ]]; then
    # Issue is in Project but not labeled `backlog` in this repo — could be
    # from another repo sharing the Project, or backlog label was removed.
    not_backlog=$((not_backlog+1))
    continue
  fi
  existing="${current_labels[${number}]}"
  case ",${existing}," in
    *",${target_label},"*)
      echo "[skip] #${number} already has ${target_label}"
      skipped=$((skipped+1))
      continue
      ;;
  esac

  case "${MODE}" in
    dry-run)
      echo "[dry] #${number} (${state}) would add ${target_label}"
      would_add=$((would_add+1))
      ;;
    apply)
      gh issue edit "${number}" --repo "${REPO}" --add-label "${target_label}" >/dev/null
      echo "[ok]  #${number} (${state}) + ${target_label}"
      added=$((added+1))
      sleep 0.3
      ;;
  esac
done < <(jq -r '
  .items[]
  | select(.content.type == "Issue")
  | select((.content.url // "") | contains("/gocell/"))
  | [.content.number, (.priority // ""), (.content.state // "")] | @tsv
' "${tmpdir}/items.json")

echo "---"
echo "total=${total} skipped=${skipped} would_add=${would_add} added=${added}"
echo "unset_priority=${unset_priority} not_backlog=${not_backlog}"
