#!/usr/bin/env bash
# migrate-backlog-to-project.sh — 一次性脚本：把 docs/backlog/20260520/ 下归档的
# markdown 表格 OPEN 条目迁移到 GitHub Issues + Project v2 "GoCell Backlog"。
#
# 用法：
#   1. 前置：已在 GitHub 建好 Project v2，配置 7 个 custom fields + automation
#      （docs/backlog.md §"前置操作"），把 PROJECT_NUMBER 回填本脚本顶部 vars
#   2. dry-run：./scripts/migrate-backlog-to-project.sh         （默认，仅打印计划）
#   3. 正式跑：./scripts/migrate-backlog-to-project.sh --apply  （需 GH_TOKEN
#      含 repo + project scope）
#
# 风险：
#   - rate limit ~5000 issue/h，~200 条预计 <1 min 完成
#   - --apply 后无回滚机制，请先在 fork 演练
#   - 脚本不重入：若中途失败，删除已建 issues 后重跑
#
# 数据源：docs/backlog/20260520/{backlog-root,cap-*,RERATING-*}.md
#   - 跳过 Flag=✅ 行（done，已交付）
#   - 跳过 ~~ID~~ 删除线行（已关闭）
#   - 其余 OPEN 行 → 1 issue + 1 project item

set -euo pipefail

# ===== Vars (admin 操作完成后回填) =====
OWNER="ghbvf"
REPO="gocell"
PROJECT_NUMBER=""   # ← 在 https://github.com/users/ghbvf/projects 建后回填

# field-id / option-id 从 `gh project field-list <NUM> --owner $OWNER --format json` 拿到
# 注：基于 Iterative development 模板。Status / Priority / Estimate / Iteration 是模板自带；
# Capability / Flag / Type / Trigger / Source 是自加。
CAP_FIELD_ID=""      # custom
PRI_FIELD_ID=""      # 模板自带 Priority
EST_FIELD_ID=""      # 模板自带 Estimate（值改 Cx1..Cx4）
FLAG_FIELD_ID=""     # custom
TYPE_FIELD_ID=""     # custom
TRIG_FIELD_ID=""     # custom
SRC_FIELD_ID=""      # custom

# option-id 用 case 函数返回（避免 bash 3 不支持 associative array）
cap_option_id() {
  case "$1" in
    cap-01) echo "" ;;  cap-02) echo "" ;;  cap-03) echo "" ;;  cap-04) echo "" ;;
    cap-05) echo "" ;;  cap-06) echo "" ;;  cap-07) echo "" ;;  cap-08) echo "" ;;
    cap-09) echo "" ;;  cap-10) echo "" ;;  cap-11) echo "" ;;  cap-12) echo "" ;;
    cap-13) echo "" ;;  cap-14) echo "" ;;  cap-x-cross|cap-x) echo "" ;;
    *) echo "" ;;
  esac
}
pri_option_id()  { case "$1" in P1) echo "";; P2) echo "";; P3) echo "";; P4) echo "";; *) echo "";; esac; }
est_option_id()   { case "$1" in Cx1) echo "";; Cx2) echo "";; Cx3) echo "";; Cx4) echo "";; *) echo "";; esac; }
flag_option_id() { case "$1" in hard) echo "";; cond) echo "";; soft) echo "";; planned) echo "";; *) echo "";; esac; }
type_option_id() {
  case "$1" in
    feat) echo "";; bug) echo "";; refactor) echo "";; arch-opt) echo "";;
    doc) echo "";; test) echo "";; debt) echo "";; fu) echo "";; *) echo "";;
  esac
}

# ===== Args =====
APPLY=0
[[ "${1:-}" == "--apply" ]] && APPLY=1
DRY_PREFIX=$([[ "$APPLY" == "1" ]] && echo "" || echo "# DRY-RUN: ")

# ===== Sanity =====
if [[ "$APPLY" == "1" ]] && [[ -z "$PROJECT_NUMBER" ]]; then
  echo "ERROR: PROJECT_NUMBER 未填，前置操作未完成。详见 docs/backlog.md" >&2
  exit 1
fi

if ! command -v gh >/dev/null; then
  echo "ERROR: gh CLI not found" >&2
  exit 1
fi

# ===== Parse markdown tables =====
ARCHIVE_DIR="docs/backlog/20260520"
INPUT_FILES=(
  "$ARCHIVE_DIR/backlog-root.md"
  "$ARCHIVE_DIR/cap-02-metadata-governance.md"
  "$ARCHIVE_DIR/cap-13-observability.md"
  "$ARCHIVE_DIR/cap-14-tooling.md"
  "$ARCHIVE_DIR/cap-x-cross.md"
)

# shell trim — xargs 对含 quote 的 markdown 不稳，bash parameter expansion 在多字节 UTF-8 emoji
# 边界不可靠，统一走 sed
trim() {
  printf '%s' "${1-}" | sed -E 's/^[[:space:]]+//; s/[[:space:]]+$//'
}

# flag emoji 探测（多字节 case 比 [[ ]] 多重组合稳定）
has_flag_emoji() {
  case "$1" in
    *🔴*|*🟠*|*🟡*|*🟢*|*✅*) return 0 ;;
    *) return 1 ;;
  esac
}

# 提取 8-column table row 的字段（粗解析，逗号/管道在 description 内已转义为正常文本）
# 跳过 header / separator / strikethrough / ✅ 行
parse_row() {
  local line="$1"
  local cap_hint="$2"
  # split on '|', strip leading/trailing whitespace and pipes
  # row 形如 "| ID | desc | type | P/Cx | Flag | Trigger | Files | Source |"
  IFS='|' read -ra cols <<<"$line"
  # 仅处理 8-column backlog 表：前置 | 产生 cols[0]=""，8 个数据列占 cols[1..8]，
  # 末尾 | 可能不产生空字段（bash read 行为），故要求 ≥ 9 段
  [[ ${#cols[@]} -lt 9 ]] && return 1
  local id=$(trim "${cols[1]:-}")
  local desc=$(trim "${cols[2]:-}")
  local type=$(trim "${cols[3]:-}")
  local pcx=$(trim "${cols[4]:-}")
  local flag=$(trim "${cols[5]:-}")
  local trigger=$(trim "${cols[6]:-}")
  local files=$(trim "${cols[7]:-}")
  local source=$(trim "${cols[8]:-}")

  # skip header & separator & schema-doc rows
  [[ "$id" == "ID" ]] && return 1
  [[ "$id" =~ ^-+$ ]] && return 1
  [[ "$id" =~ :?-+:? ]] && [[ ${#id} -le 5 ]] && return 1   # ---|:--- separator
  [[ -z "$id" ]] && return 1
  # 必须含 Flag emoji（实表条目都带 🔴/🟠/🟡/🟢/✅）；schema 文档表无
  has_flag_emoji "$flag" || return 1

  # skip done (✅ or strikethrough)
  case "$flag" in *✅*) return 1 ;; esac
  case "$id" in '~~'*'~~') return 1 ;; esac

  # parse P/Cx -> P1 / Cx2
  local pri=$(echo "$pcx" | grep -oE 'P[1-4]' | head -1)
  local cx=$(echo "$pcx" | grep -oE 'Cx[1-4]' | head -1)

  # parse Flag emoji -> flag-* enum
  local flag_enum=""
  case "$flag" in
    *"🔴"*) flag_enum="hard" ;;
    *"🟠"*) flag_enum="cond" ;;
    *"🟡"*) flag_enum="soft" ;;
    *"🟢"*) flag_enum="planned" ;;
  esac

  echo "$cap_hint|$id|$desc|$type|$pri|$cx|$flag_enum|$trigger|$files|$source"
}

# ===== Generate plan =====
echo "${DRY_PREFIX}== migrate-backlog-to-project.sh =="
echo "${DRY_PREFIX}Owner: $OWNER, Repo: $REPO, Project: ${PROJECT_NUMBER:-<unset>}"
echo ""

total=0
for f in "${INPUT_FILES[@]}"; do
  [[ -f "$f" ]] || { echo "${DRY_PREFIX}skip missing: $f"; continue; }

  # cap hint 从文件名推：cap-02-* -> cap-02；root → 看每个 ## cap-N 段
  cap_default=$(basename "$f" .md | grep -oE 'cap-[0-9x]+' || echo "")

  cur_cap="$cap_default"
  while IFS= read -r line; do
    # 在 backlog-root.md 内追踪 ## cap-NN 段标题
    if [[ "$line" =~ ^##[[:space:]]+cap- ]]; then
      cur_cap=$(echo "$line" | grep -oE 'cap-[0-9x-]+' | head -1)
      continue
    fi
    # only process table rows (start with '|')
    [[ "$line" == "|"* ]] || continue
    row=$(parse_row "$line" "$cur_cap") || continue

    IFS='|' read -r cap id desc type pri cx flag trigger files source <<<"$row"

    total=$((total + 1))
    short_title=$(echo "$desc" | sed -E 's/^\*\*([^*]+)\*\*.*/\1/' | head -c 80)

    echo "${DRY_PREFIX}--- item #$total ---"
    echo "${DRY_PREFIX}cap=$cap id=$id pri=$pri cx=$cx flag=$flag type=$type"
    echo "${DRY_PREFIX}title=[${id}] ${short_title}"
    [[ -n "$trigger" && "$trigger" != "—" ]] && echo "${DRY_PREFIX}trigger=$trigger"
    [[ -n "$files" && "$files" != "—" ]] && echo "${DRY_PREFIX}files=$files"
    [[ -n "$source" && "$source" != "—" ]] && echo "${DRY_PREFIX}source=$source"

    if [[ "$APPLY" == "1" ]]; then
      # 1. create issue
      body=$(cat <<EOF
## 现状 / 修复方向

$desc

## Files

$files

## Trigger

$trigger

## Source

$source

---
*Migrated from \`docs/backlog/20260520/\` on $(date +%Y-%m-%d) by scripts/migrate-backlog-to-project.sh.*
EOF
)
      issue_url=$(gh issue create \
        --repo "$OWNER/$REPO" \
        --title "[$id] $short_title" \
        --label backlog \
        --body "$body")
      issue_num=$(echo "$issue_url" | grep -oE '[0-9]+$')

      # 2. wait for automation 自动入 project (best-effort retry)
      sleep 2
      item_id=$(gh project item-list "$PROJECT_NUMBER" --owner "$OWNER" --format json \
        --limit 9999 \
        | jq -r ".items[] | select(.content.number==$issue_num) | .id")

      if [[ -z "$item_id" ]]; then
        echo "WARN: issue #$issue_num not auto-added to project, adding manually" >&2
        item_id=$(gh project item-add "$PROJECT_NUMBER" --owner "$OWNER" --url "$issue_url" --format json | jq -r '.id')
      fi

      # 3. set fields (单选 fields 用 --single-select-option-id)
      cap_opt=$(cap_option_id "$cap")
      pri_opt=$(pri_option_id "$pri")
      est_opt=$(est_option_id "$cx")
      flag_opt=$(flag_option_id "$flag")
      type_opt=$(type_option_id "$type")

      [[ -n "$cap_opt" ]] && \
        gh project item-edit --id "$item_id" --project-id "$PROJECT_NUMBER" \
          --field-id "$CAP_FIELD_ID" --single-select-option-id "$cap_opt" >/dev/null
      [[ -n "$pri_opt" ]] && \
        gh project item-edit --id "$item_id" --project-id "$PROJECT_NUMBER" \
          --field-id "$PRI_FIELD_ID" --single-select-option-id "$pri_opt" >/dev/null
      [[ -n "$est_opt" ]] && \
        gh project item-edit --id "$item_id" --project-id "$PROJECT_NUMBER" \
          --field-id "$EST_FIELD_ID" --single-select-option-id "$est_opt" >/dev/null
      [[ -n "$flag_opt" ]] && \
        gh project item-edit --id "$item_id" --project-id "$PROJECT_NUMBER" \
          --field-id "$FLAG_FIELD_ID" --single-select-option-id "$flag_opt" >/dev/null
      [[ -n "$type_opt" ]] && \
        gh project item-edit --id "$item_id" --project-id "$PROJECT_NUMBER" \
          --field-id "$TYPE_FIELD_ID" --single-select-option-id "$type_opt" >/dev/null
      [[ -n "$trigger" && "$trigger" != "—" ]] && \
        gh project item-edit --id "$item_id" --project-id "$PROJECT_NUMBER" \
          --field-id "$TRIG_FIELD_ID" --text "$trigger" >/dev/null
      [[ -n "$source" && "$source" != "—" ]] && \
        gh project item-edit --id "$item_id" --project-id "$PROJECT_NUMBER" \
          --field-id "$SRC_FIELD_ID" --text "$source" >/dev/null

      echo "  -> created $issue_url (item $item_id)"
    fi
  done <"$f"
done

echo ""
echo "${DRY_PREFIX}== total OPEN items: $total =="
[[ "$APPLY" == "0" ]] && echo "${DRY_PREFIX}Run with --apply to execute."
