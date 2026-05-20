#!/usr/bin/env bash
# migrate-backlog-to-project.sh — 一次性脚本：把 docs/backlog/20260520/ 下归档的
# markdown 表格 OPEN 条目迁移到 GitHub Issues（含 cap-XX / flag-XX / type-XX 标签）。
#
# 方案 A（极简）：
#   - 仅创建 issue + 贴 labels（repo scope 足够，无需 project scope token）
#   - 不设置 Project v2 fields（Priority / Estimate）—— automation 入 project 后由
#     管理员在 web UI 或独立脚本（需 project scope token）批量设
#   - Trigger / Source / 描述 narrative 全部写 issue body
#
# 用法：
#   dry-run： ./scripts/migrate-backlog-to-project.sh         （默认，仅打印计划）
#   正式跑： ./scripts/migrate-backlog-to-project.sh --apply
#
# 风险：
#   - rate limit ~5000 issue/h，~200 条预计 <1 min 完成
#   - --apply 后无回滚机制，请先在 fork 演练
#   - 脚本不重入：若中途失败，删除已建 issues 后重跑

set -eo pipefail

OWNER="ghbvf"
REPO="gocell"

APPLY=0
[[ "${1:-}" == "--apply" ]] && APPLY=1
DRY_PREFIX=$([[ "$APPLY" == "1" ]] && echo "" || echo "# DRY-RUN: ")

if ! command -v gh >/dev/null; then
  echo "ERROR: gh CLI not found" >&2
  exit 1
fi

# 续跑：先抓所有已建 backlog issue 的 ID（title 形如 [ID] ...）
EXISTING_IDS=""
if [[ "$APPLY" == "1" ]]; then
  EXISTING_IDS=$(gh issue list --repo "$OWNER/$REPO" --label backlog --state all \
    --limit 1000 --json title --jq '.[].title' \
    | sed -nE 's/^\[([^]]+)\].*/\1/p' \
    | sort -u)
  echo "${DRY_PREFIX}skip-existing: 已建 $(echo "$EXISTING_IDS" | grep -c .) issues"
fi
is_existing() {
  echo "$EXISTING_IDS" | grep -Fxq "$1"
}

# 带 retry 的 issue create — HTTP 4xx/5xx / GraphQL hiccup 自动 sleep 重试 3 次
create_issue_with_retry() {
  local title="$1" labels="$2" body_file="$3"
  local attempt=1
  while [[ $attempt -le 3 ]]; do
    if url=$(gh issue create --repo "$OWNER/$REPO" --title "$title" --label "$labels" --body-file "$body_file" 2>&1); then
      echo "$url"
      return 0
    fi
    echo "WARN attempt $attempt failed: $url" >&2
    sleep $((attempt * 2))
    attempt=$((attempt + 1))
  done
  return 1
}

trim() {
  printf '%s' "${1-}" | sed -E 's/^[[:space:]]+//; s/[[:space:]]+$//'
}

has_flag_emoji() {
  case "$1" in
    *🔴*|*🟠*|*🟡*|*🟢*|*✅*) return 0 ;;
    *) return 1 ;;
  esac
}

# Flag emoji → label suffix（小写 enum 值）
flag_label() {
  case "$1" in
    *🔴*) echo "flag-hard" ;;
    *🟠*) echo "flag-cond" ;;
    *🟡*) echo "flag-soft" ;;
    *🟢*) echo "flag-planned" ;;
    *) echo "" ;;
  esac
}

# Type 字符串归一（处理 doc+arch-opt 等混合）→ 单个 type label
# 若多 type 拼接（如 "doc+arch-opt"），取第一个；review 时人工调整
type_label() {
  local t="$1"
  local first=$(echo "$t" | sed -E 's/[+,/].*$//')
  case "$first" in
    feat|bug|refactor|arch-opt|doc|test|debt|fu) echo "type-$first" ;;
    # 历史别名归一（来自 backlog/20260520 实测分布）
    docs) echo "type-doc" ;;          # typo "docs"
    perf-opt|perf) echo "type-arch-opt" ;;
    arch|arch-hard) echo "type-arch-opt" ;;
    archtest) echo "type-test" ;;     # archtest 是测试治理
    tech-debt) echo "type-debt" ;;
    compliance) echo "type-doc" ;;    # 合规评估属文档/审计类
    *) echo "" ;;
  esac
}

# 提取 8-column table row 的字段
parse_row() {
  local line="$1"
  local cap_hint="$2"
  IFS='|' read -ra cols <<<"$line"
  # 仅处理 8-column backlog 表：前置 | 产生 cols[0]=""，8 数据列占 cols[1..8]，
  # bash read 末尾空字段可能被吞，故要求 ≥ 9 段
  [[ ${#cols[@]} -lt 9 ]] && return 1
  local id=$(trim "${cols[1]:-}")
  local desc=$(trim "${cols[2]:-}")
  local type=$(trim "${cols[3]:-}")
  local pcx=$(trim "${cols[4]:-}")
  local flag=$(trim "${cols[5]:-}")
  local trigger=$(trim "${cols[6]:-}")
  local files=$(trim "${cols[7]:-}")
  local source=$(trim "${cols[8]:-}")

  # skip header / separator / schema-doc 行
  [[ "$id" == "ID" ]] && return 1
  [[ "$id" =~ ^-+$ ]] && return 1
  [[ -z "$id" ]] && return 1
  # 实表条目必含 Flag emoji；schema 文档表无
  has_flag_emoji "$flag" || return 1
  # skip done & strikethrough
  case "$flag" in *✅*) return 1 ;; esac
  case "$id" in '~~'*'~~') return 1 ;; esac

  # parse P/Cx — rubric 真值 P0/P1/P2/P3 & Cx1/Cx2/Cx3/Cx4
  local pri=$(echo "$pcx" | grep -oE 'P[0-3]' | head -1 || echo "")
  local cx=$(echo "$pcx" | grep -oE 'Cx[1-4]' | head -1 || echo "")

  echo "$cap_hint|$id|$desc|$type|$pri|$cx|$flag|$trigger|$files|$source"
}

ARCHIVE_DIR="docs/backlog/20260520"
INPUT_FILES=(
  "$ARCHIVE_DIR/backlog-root.md"
  "$ARCHIVE_DIR/cap-02-metadata-governance.md"
  "$ARCHIVE_DIR/cap-13-observability.md"
  "$ARCHIVE_DIR/cap-14-tooling.md"
  "$ARCHIVE_DIR/cap-x-cross.md"
)

echo "${DRY_PREFIX}== migrate-backlog-to-project.sh =="
echo "${DRY_PREFIX}Owner: $OWNER, Repo: $REPO"
echo ""

total=0
for f in "${INPUT_FILES[@]}"; do
  [[ -f "$f" ]] || { echo "${DRY_PREFIX}skip missing: $f"; continue; }

  # cap_default 从文件名推：cap-02-* → cap-02；root → 看每个 ## cap-N 段
  cap_default=$(basename "$f" .md | grep -oE 'cap-[0-9x]+' || echo "")
  # cap-x → cap-x-cross
  [[ "$cap_default" == "cap-x" ]] && cap_default="cap-x-cross"

  cur_cap="$cap_default"
  while IFS= read -r line; do
    # backlog-root.md 内追踪 ## cap-NN 段标题
    if [[ "$line" =~ ^##[[:space:]]+cap- ]]; then
      cur_cap=$(echo "$line" | grep -oE 'cap-[0-9x-]+' | head -1)
      # 归一 cap-x → cap-x-cross
      [[ "$cur_cap" == "cap-x" ]] && cur_cap="cap-x-cross"
      continue
    fi
    [[ "$line" == "|"* ]] || continue
    row=$(parse_row "$line" "$cur_cap") || continue

    IFS='|' read -r cap id desc type pri cx flag trigger files source <<<"$row"

    total=$((total + 1))
    short_title=$(echo "$desc" | sed -E 's/^\*\*([^*]+)\*\*.*/\1/' | head -c 80)

    flag_lab=$(flag_label "$flag")
    type_lab=$(type_label "$type")

    labels="backlog"
    [[ -n "$cap" ]] && labels="$labels,$cap"
    [[ -n "$flag_lab" ]] && labels="$labels,$flag_lab"
    [[ -n "$type_lab" ]] && labels="$labels,$type_lab"

    echo "${DRY_PREFIX}--- item #$total ---"
    echo "${DRY_PREFIX}title=[${id}] ${short_title}"
    echo "${DRY_PREFIX}labels=$labels"
    echo "${DRY_PREFIX}priority=$pri cx=$cx (set in Project UI separately)"

    if [[ "$APPLY" == "1" ]]; then
      # skip if already created (续跑)
      if is_existing "$id"; then
        echo "  -> SKIP $id (already exists)"
        continue
      fi
      body_file=$(mktemp)
      cat > "$body_file" <<EOF
## 现状 / 修复方向

$desc

## Files

$files

## Trigger

$trigger

## Source

$source

---
*Migrated from \`docs/backlog/20260520/\` on $(date +%Y-%m-%d) by \`scripts/migrate-backlog-to-project.sh\`.*
*Priority=\`$pri\` Estimate=\`$cx\` — 设置到 Project v2 fields by admin（需 project scope token）。*
EOF
      if issue_url=$(create_issue_with_retry "[$id] $short_title" "$labels" "$body_file"); then
        echo "  -> created $issue_url"
      else
        echo "  -> FAILED after 3 retries: [$id]" >&2
      fi
      rm -f "$body_file"
    fi
  done <"$f"
done

echo ""
echo "${DRY_PREFIX}== total OPEN items: $total =="
if [[ "$APPLY" == "0" ]]; then
  echo "${DRY_PREFIX}Run with --apply to execute."
  echo "${DRY_PREFIX}Project v2 Priority/Estimate fields not set here — admin should batch-set"
  echo "${DRY_PREFIX}in Project UI (table view, multi-row edit) after issues land."
fi
