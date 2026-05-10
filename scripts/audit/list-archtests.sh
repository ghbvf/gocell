#!/usr/bin/env bash
# list-archtests.sh — print the archtest invariant inventory to stdout by
# scanning every `// INVARIANT: <ID>` anchor in tools/archtest/*_test.go.
#
# This script is an on-demand audit aid; there is NO persisted view. The
# reverse index from rule ID → file is held by the anchors themselves; the
# `INVENTORY-ANCHOR-REQUIRED-01` archtest enforces that every file carries
# at least one. To find a rule's home, prefer:
#
#   grep -rn 'INVARIANT: <ID>' tools/archtest/
#
# Run this script when you need a sorted overview by theme.
#
# Theme is derived from the INVARIANT ID prefix (first dash-segment), with a
# small alias table for functional groupings whose rules live in the same
# theme file (e.g. `MESSAGE-CONST-LITERAL-*` lives in errcode_invariants_test.go).
#
# ref: kubernetes/kubernetes hack/update-codegen.sh — annotation-driven
#      discovery (`+k8s:deepcopy-gen=` → glob source files); we apply the same
#      pattern with `// INVARIANT: <ID>` as the anchor.

set -euo pipefail

# Pin sort/awk locale so the output order is bit-identical on macOS dev boxes
# and Linux runners. Without LC_ALL=C, GNU sort folds case and treats
# punctuation differently than BSD sort, which surfaces as drift.
export LC_ALL=C

repo_root="$(git rev-parse --show-toplevel 2>/dev/null)" || {
  echo "list-archtests.sh: must run inside a git work tree" >&2
  exit 1
}
cd "${repo_root}"

archtest_dir="tools/archtest"

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
# output across renames.
archtest_files=()
# git ls-files: tracked files only — drafts/.bak/IDE temp files cannot pollute
# the listing. The awk filter mimics `find -maxdepth 1 -type f` semantics:
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
# The leading `- ` prefix (for list-form anchors written as
# `//   - INVARIANT: ID`) is matched with the same regex; the awk normalizes
# both forms because INVENTORY-ANCHOR-REQUIRED-01 accepts either.
extract_rules() {
  local file="${1}"
  local base
  base="$(basename "${file}")"
  # ID shape: starts with letter, then uppercase/digits, with zero or more
  # `-<UPPER>` middle segments, ending in `-<digits>`. Matches OBS-01 (2-seg)
  # through CLOCK-INJECTION-PROD-CALLSITE-01 (5-seg).
  local id_pattern='[A-Z][A-Z0-9]+(-[A-Z0-9]+)*-[0-9]+'

  awk -v base="${base}" -v idre="${id_pattern}" '
    function trim(s) {
      sub(/^[[:space:]]+/, "", s);
      sub(/[[:space:]]+$/, "", s);
      return s;
    }
    function emit_id(id, lineno) {
      print id "\t" base "\t" lineno;
    }
    function expand_token(tok, lineno,    head, dotdot_pos, end_str, last_dash, start_id, start_str, width, i, fmt, padded, plain_id) {
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
      # Detect anchor start. Accept both "// INVARIANT: ..." and
      # "// - INVARIANT: ..." (list-form sibling within a CommentGroup).
      if (match($0, /^[[:space:]]*\/\/[[:space:]]*-?[[:space:]]*INVARIANT:[[:space:]]*/)) {
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

cat <<HEADER
# archtest invariant inventory (stdout-only, on-demand)

> Source-of-truth: \`// INVARIANT: <ID>\` anchors in \`tools/archtest/*_test.go\`.
> Reverse index: \`grep -rn 'INVARIANT: <ID>' tools/archtest/\`.
> Gate: \`INVENTORY-ANCHOR-REQUIRED-01\` archtest enforces every file carries an anchor.

## Overview

- archtest files: ${archtest_count}
- archtest INVARIANT anchors: ${#rule_rows[@]}

## Rules

| INVARIANT | File | Line | Theme |
|---|---|---|---|
HEADER

if (( ${#rule_rows[@]} > 0 )); then
  for row in "${rule_rows[@]}"; do
    IFS=$'\t' read -r id file line <<<"${row}"
    theme="$(theme_for_id "${id}")"
    echo "| \`${id}\` | \`${file}\` | ${line} | ${theme} |"
  done
fi
