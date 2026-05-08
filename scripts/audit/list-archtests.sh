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

# Pin sort/awk locale so the inventory order is bit-identical on macOS dev
# boxes and Linux CI runners. Without LC_ALL=C, GNU sort folds case and
# treats punctuation differently than BSD sort, which surfaces as drift.
export LC_ALL=C

repo_root="$(git rev-parse --show-toplevel 2>/dev/null)" || {
  echo "list-archtests.sh: must run inside a git work tree" >&2
  exit 1
}
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
# git ls-files: tracked files only — drafts/.bak/IDE temp files cannot pollute
# the inventory. The awk filter mimics `find -maxdepth 1 -type f` semantics:
# git pathspec wildcards match across `/`, so we count slash segments.
archtest_depth="$(awk -F/ '{print NF + 1}' <<<"${archtest_dir}")"
while IFS= read -r line; do archtest_files+=("${line}"); done < <(
  git ls-files -- "${archtest_dir}/" \
    | awk -F/ -v want="${archtest_depth}" 'NF == want && /_test\.go$/' \
    | sort
)
archtest_count="${#archtest_files[@]}"

# extract_rules collects rule rows (id<TAB>basename<TAB>line) for one file.
#
# Anchor grammar (all variants emit one row per declared ID):
#   // INVARIANT: ID-01
#   // INVARIANT: ID-01..04                          # numeric range
#   // INVARIANT: ID-01, ID-02, ID-03                # comma list
#   // INVARIANT: ID-01..02, OTHER-ID-01             # mixed
#   // INVARIANT: ID-01, ID-02,                      # multi-line continuation:
#   //            ID-03, ID-04                       # any subsequent comment
#   //            ID-05                              # line until trailing comma drops
#
# Fallback (legacy single-rule files without the anchor): scan first 100
# lines for any ID-shape literal, take first occurrence per ID.
extract_rules() {
  local file="${1}"
  local base
  base="$(basename "${file}")"
  # ID shape: starts with letter, then uppercase/digits, with zero or more
  # `-<UPPER>` middle segments, ending in `-<digits>`. Matches OBS-01 (2-seg)
  # through CLOCK-INJECTION-PROD-CALLSITE-01 (5-seg).
  local id_pattern='[A-Z][A-Z0-9]+(-[A-Z0-9]+)*-[0-9]+'

  if grep -qE "^[[:space:]]*//[[:space:]]*INVARIANT:[[:space:]]+${id_pattern}" "${file}" 2>/dev/null; then
    awk -v base="${base}" -v idre="${id_pattern}" '
      function trim(s) {
        sub(/^[[:space:]]+/, "", s);
        sub(/[[:space:]]+$/, "", s);
        return s;
      }
      function emit_id(id, lineno) {
        print id "\t" base "\t" lineno;
      }
      function expand_token(tok, lineno,    head, dotdot_pos, end_str, last_dash, start_str, width, i, fmt, padded, plain_id) {
        # Range form: token starts with "<ID>..<digits>" (with the ID itself
        # ending in a numeric segment so we know where to splice).
        if (match(tok, /^[A-Z][A-Z0-9]+(-[A-Z0-9]+)*-[0-9]+\.\.[0-9]+/)) {
          head = substr(tok, RSTART, RLENGTH);
          dotdot_pos = index(head, "..");
          end_str    = substr(head, dotdot_pos + 2);
          start_id   = substr(head, 1, dotdot_pos - 1);
          # split start_id into "<prefix>-<digits>" via the trailing -NN.
          last_dash = match(start_id, /-[0-9]+$/);
          if (last_dash > 0) {
            start_str = substr(start_id, last_dash + 1);
            width = length(start_str);
            fmt = "%0" width "d";
            for (i = start_str + 0; i <= end_str + 0; i++) {
              padded = sprintf(fmt, i);
              emit_id(substr(start_id, 1, last_dash) padded, lineno);
            }
            return;
          }
        }
        # Plain ID at the start of the token (description / colon / dash
        # tail is allowed and ignored).
        if (match(tok, /^[A-Z][A-Z0-9]+(-[A-Z0-9]+)*-[0-9]+/)) {
          plain_id = substr(tok, RSTART, RLENGTH);
          emit_id(plain_id, lineno);
        }
      }
      function flush(text, lineno,    parts, n, i, tok) {
        n = split(text, parts, ",");
        for (i = 1; i <= n; i++) {
          tok = trim(parts[i]);
          if (tok != "") {
            expand_token(tok, lineno);
          }
        }
      }
      {
        # Detect anchor start.
        if (match($0, /^[[:space:]]*\/\/[[:space:]]*INVARIANT:[[:space:]]*/)) {
          start_line = NR;
          # Strip the prefix; keep the payload (may be incomplete on continuation).
          payload = substr($0, RSTART + RLENGTH);
          # Drop trailing CR (CRLF tolerance).
          sub(/\r$/, "", payload);
          # Continuation while payload ends with a comma (after trim).
          while (match(payload, /,[[:space:]]*$/)) {
            if ((getline next_line) <= 0) break;
            sub(/\r$/, "", next_line);
            if (match(next_line, /^[[:space:]]*\/\/[[:space:]]*/)) {
              cont = substr(next_line, RSTART + RLENGTH);
              # Empty `//` line breaks the continuation.
              if (trim(cont) == "") break;
              payload = payload " " cont;
            } else {
              break;
            }
          }
          flush(payload, start_line);
        }
      }
    ' "${file}"
    return
  fi

  head -n 100 "${file}" 2>/dev/null \
    | awk -v base="${base}" -v idre="${id_pattern}" '
        # Legacy single-rule files: scan first 100 lines for any ID-shape
        # literal in either comments or const string literals. Take first
        # occurrence per unique ID.
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

# Governance rules — IDs grepped from production rules_*.go bodies only.
# `_test.go` companions are excluded so the inventory reports the real rule
# definition surface (the *_test.go count would double-count rules).
governance_files=()
governance_depth="$(awk -F/ '{print NF + 1}' <<<"${governance_dir}")"
while IFS= read -r line; do governance_files+=("${line}"); done < <(
  git ls-files -- "${governance_dir}/" \
    | awk -F/ -v want="${governance_depth}" \
        'NF == want && $NF ~ /^rules_.*\.go$/ && $NF !~ /_test\.go$/' \
    | sort
)
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
    # NB: longer compound prefixes (CONTRACT-CONSISTENCY-EMIT / SLICE-
    # CONSISTENCY / DOC-NAME) MUST come before their shorter substrings
    # in the alternation so grep -oE picks the maximal match. Without this
    # ordering, CONTRACT-CONSISTENCY-EMIT-01 was truncated to
    # CONSISTENCY-EMIT-01 because \bCONSISTENCY matched mid-token at the
    # dash boundary. The suffix part allows internal hyphens to keep
    # legacy archtest IDs intact (e.g. WRAPPER-CONTRACTSPEC-IMPORT-01).
    # Regression: PR-FUNNEL-03 review (2026-05-08), regression test in
    # kernel/governance/rule_inventory_test.go::TestArchtestInventoryNoIDTruncation.
    rules="$(grep -hoE '\b(CONTRACT-CONSISTENCY-EMIT|SLICE-CONSISTENCY|DOC-NAME|FMT|CH|REF|TOPO|VERIFY|ADV|OUTGUARD|DOC|WRAPPER)-[A-Z0-9-]+' "${f}" 2>/dev/null \
      | sort -u | paste -sd, - | sed 's/,/, /g')"
    if [[ -z "${rules}" ]]; then
      rules="_无显式 ID_"
    fi
    echo "| \`${base}\` | ${rules} |"
  done
fi
