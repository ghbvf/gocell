#!/usr/bin/env bash
# list-archtests.sh — generate the archtest invariant inventory by scanning
# every `// INVARIANT: <ID>` anchor in tools/archtest/*_test.go.
#
# Source-of-truth: the anchors themselves (one rule per anchor). The output is
# a derived view; the inventory file (docs/audit/archtest-inventory.md) is
# regenerated and diff-gated by hack/verify-archtest-inventory.sh.
#
# Theme is derived from the INVARIANT ID prefix (first dash-segment), with a
# small alias table for functional groupings whose rules live in the same
# theme file (e.g. `MESSAGE-CONST-LITERAL-*` lives in errcode_invariants_test.go).
#
# ref: kubernetes/kubernetes hack/update-codegen.sh — annotation-driven
#      discovery (`+k8s:deepcopy-gen=` → glob source files); we apply the same
#      pattern with `// INVARIANT: <ID>` as the anchor.

set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
cd "${repo_root}"

archtest_dir="tools/archtest"
governance_dir="kernel/governance"

# theme_for_id maps the leading dash-segment of an INVARIANT ID to a
# functional theme. Ungrouped IDs fall through to lowercased first segment.
theme_for_id() {
  local id="${1}"
  local prefix="${id%%-*}"
  case "${id}" in
    MESSAGE-CONST-LITERAL-*|DETAILS-SLOG-ATTR-*|EXPORTED-ERROR-NEW-*|ERROR-FIRST-*|ERRCODE-*)
      echo "errcode" ;;
    ASSEMBLY-*|ASSEMBLYREF-*)
      echo "assembly" ;;
    CODEGEN-*|SPEC-GEN-*)
      echo "codegen" ;;
    *)
      # Lowercase the prefix without invoking external tools.
      echo "${prefix}" | tr '[:upper:]' '[:lower:]' ;;
  esac
}

# Collect (file, line, id) triples by walking every anchor. Multi-INVARIANT
# theme files expand to one row per anchor. Sort by id then by file for stable
# diffs across renames.
archtest_files=()
while IFS= read -r line; do archtest_files+=("${line}"); done < <(find "${archtest_dir}" -maxdepth 1 -type f -name '*_test.go' | sort)
archtest_count="${#archtest_files[@]}"

# extract_rules collects rule rows (id<TAB>basename<TAB>line) for one file:
# 1) Prefer explicit `// INVARIANT: <ID>` anchors (any line). Theme files like
#    outbox_invariants_test.go declare all rules this way.
# 2) Fall back to bare leading-comment IDs in first 50 lines (e.g. single-rule
#    files like auth_plan_test.go that pre-date the INVARIANT anchor convention).
extract_rules() {
  local file="${1}"
  local base
  base="$(basename "${file}")"
  # ID shape: starts with letter, then uppercase/digits, with zero or more
  # `-<UPPER>` middle segments, ending in `-<digits>`. Matches OBS-01 (2-seg)
  # through CLOCK-INJECTION-PROD-CALLSITE-01 (5-seg).
  local id_pattern='[A-Z][A-Z0-9]+(-[A-Z0-9]+)*-[0-9]+'
  local anchored
  anchored="$(grep -nE "^[[:space:]]*//[[:space:]]*INVARIANT:[[:space:]]+${id_pattern}" "${file}" 2>/dev/null || true)"
  if [[ -n "${anchored}" ]]; then
    echo "${anchored}" | awk -v base="${base}" -v idre="${id_pattern}" '
      {
        idx = index($0, ":");
        if (idx <= 0) next;
        lineno = substr($0, 1, idx - 1);
        if (match($0, idre)) {
          id = substr($0, RSTART, RLENGTH);
          print id "\t" base "\t" lineno;
        }
      }'
    return
  fi
  head -n 100 "${file}" 2>/dev/null \
    | awk -v base="${base}" -v idre="${id_pattern}" '
        # Legacy single-rule files: scan first 100 lines for any ID pattern
        # in either comments or const string literals. Take first occurrence
        # per unique ID.
        match($0, idre) {
          id = substr($0, RSTART, RLENGTH);
          if (!(id in seen)) {
            seen[id] = 1;
            print id "\t" base "\t" NR;
          }
        }'
}

rule_rows=()
for f in "${archtest_files[@]}"; do
  while IFS= read -r row; do
    [[ -z "${row}" ]] && continue
    rule_rows+=("${row}")
  done < <(extract_rules "${f}")
done
# Stable order: by id, then file, then line.
if (( ${#rule_rows[@]} > 0 )); then
  rule_rows_sorted=()
  while IFS= read -r row; do rule_rows_sorted+=("${row}"); done < <(printf '%s\n' "${rule_rows[@]}" | sort -u)
  rule_rows=("${rule_rows_sorted[@]}")
fi

files_with_anchor=()
if (( ${#rule_rows[@]} > 0 )); then
  while IFS= read -r line; do files_with_anchor+=("${line}"); done < <(
    printf '%s\n' "${rule_rows[@]}" | awk -F'\t' '{print $2}' | sort -u
  )
fi
anchor_set=$'\n'
for f in "${files_with_anchor[@]}"; do anchor_set+="${f}"$'\n'; done

uncategorized_files=()
for f in "${archtest_files[@]}"; do
  base="$(basename "${f}")"
  case "${base}" in
    archtest_test.go|helpers_test.go|*_fixtures_test.go) continue ;;
  esac
  if [[ "${anchor_set}" != *$'\n'"${base}"$'\n'* ]]; then
    uncategorized_files+=("${base}")
  fi
done

# Governance rules — IDs grepped from rules_*.go bodies.
governance_files=()
while IFS= read -r line; do governance_files+=("${line}"); done < <(find "${governance_dir}" -maxdepth 1 -type f -name 'rules_*.go' 2>/dev/null | sort || true)
governance_count="${#governance_files[@]}"

cat <<HEADER
<!-- DO NOT EDIT: regenerate with scripts/audit/list-archtests.sh -->
# archtest + governance invariant inventory

> 派生自 \`scripts/audit/list-archtests.sh\`，由 \`hack/verify-archtest-inventory.sh\` 漂移闸守护。
> 路线图：\`docs/plans/202605070431-pr403-funnel-fix-roadmap.md\`。

## 概览

- archtest 文件总数：${archtest_count}
- archtest INVARIANT 锚点数：${#rule_rows[@]}
- governance \`rules_*.go\` 文件数：${governance_count}

## archtest 规则清单

| INVARIANT | 文件 | 行 | 主题 |
|---|---|---|---|
HEADER

if (( ${#rule_rows[@]} > 0 )); then
  for row in "${rule_rows[@]}"; do
    IFS=$'\t' read -r id file line <<<"${row}"
    theme="$(theme_for_id "${id}")"
    echo "| \`${id}\` | \`${file}\` | ${line} | ${theme} |"
  done
fi

if (( ${#uncategorized_files[@]} > 0 )); then
  echo
  echo "## 未声明 INVARIANT 锚点的 archtest 文件"
  echo
  echo "下列文件没有 \`// INVARIANT: <ID>\` 锚点。helpers / fixtures 已排除；以下都是规则文件，请补锚点或加入排除列表。"
  echo
  for base in "${uncategorized_files[@]}"; do
    echo "- \`${base}\`" >&2
    echo "- \`${base}\`"
  done
fi

cat <<GOVHEADER

## governance \`rules_*.go\` 清单

| 文件 | 包含的规则 |
|---|---|
GOVHEADER

if (( governance_count > 0 )); then
  for f in "${governance_files[@]}"; do
    base="$(basename "${f}")"
    rules="$(grep -hoE '\b(FMT|CH|REF|TOPO|VERIFY|ADV|SLICE|CONSISTENCY|OUTGUARD|DOC|WRAPPER)-[A-Z0-9-]+' "${f}" 2>/dev/null \
      | sort -u | paste -sd, - | sed 's/,/, /g')"
    if [[ -z "${rules}" ]]; then
      rules="_无显式 ID_"
    fi
    echo "| \`${base}\` | ${rules} |"
  done
fi
