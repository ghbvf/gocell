#!/usr/bin/env bash
# list-archtests.sh — enumerate tools/archtest rule files and kernel/governance
# rules into a markdown table for funnel-化 audit. See
# docs/plans/202605070431-pr403-funnel-fix-roadmap.md.
#
# Output: stdout markdown. Filenames + INVARIANT IDs grepped from file-head
# godoc + theme grouping (by ID prefix).

set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

archtest_dir="tools/archtest"
governance_dir="kernel/governance"

extract_id() {
    # Match patterns like:
    #   // OUTBOX-CELL-01: ...
    #   // INVARIANT: OUTBOX-CELL-01
    #   // OUTBOX-CELL-01 ...
    # Pull the first capitalized hyphenated ID with -NN suffix from first 30 lines.
    grep -m1 -oE '\b[A-Z][A-Z0-9]+(-[A-Z0-9]+)+(-[0-9]+)\b' <(head -n 30 "$1") 2>/dev/null || true
}

theme_of() {
    # Bucket file by leading filename segment (split by underscore).
    case "$1" in
        outbox_*) echo "outbox";;
        rmq_*) echo "rmq";;
        clock_*|prod_clock_*) echo "clock";;
        refresh_*) echo "refresh";;
        handler_*) echo "handler";;
        errcode_*|error_first_*|details_slog_*|exported_error_*|message_const_*) echo "errcode";;
        panic_*) echo "panic";;
        httputil_*) echo "httputil";;
        assembly_*|assemblyref_*) echo "assembly";;
        prod_duration_*) echo "prod";;
        codegen_*|spec_gen_*) echo "codegen";;
        test_sleep_*|test_time_*|no_test_service_*) echo "test";;
        *) echo "_misc";;
    esac
}

echo "## archtest 文件清单（$(ls -1 "$archtest_dir"/*_test.go | wc -l | tr -d ' ') 个）"
echo
echo "| 文件 | INVARIANT ID | 主题 |"
echo "|---|---|---|"
for f in "$archtest_dir"/*_test.go; do
    base="$(basename "$f")"
    id="$(extract_id "$f")"
    theme="$(theme_of "$base")"
    [[ -z "$id" ]] && id="_未声明_"
    echo "| \`$base\` | $id | $theme |"
done

echo
echo "## kernel/governance/rules_*.go 清单（$(ls -1 "$governance_dir"/rules_*.go 2>/dev/null | wc -l | tr -d ' ') 个文件）"
echo
echo "| 文件 | 包含的规则 |"
echo "|---|---|"
for f in "$governance_dir"/rules_*.go; do
    base="$(basename "$f")"
    rules="$(grep -oE '\b(FMT|CH|REF|TOPO|VERIFY|ADV|SLICE|CONSISTENCY|OUTGUARD|DOC|WRAPPER)-[A-Z0-9-]+' "$f" 2>/dev/null | sort -u | tr '\n' ',' | sed 's/,$//' | sed 's/,/, /g')"
    [[ -z "$rules" ]] && rules="_无显式 ID_"
    echo "| \`$base\` | $rules |"
done
