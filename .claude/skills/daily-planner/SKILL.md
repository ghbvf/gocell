---
name: daily-planner
description: "每日迭代调度（默认 dry-run；--apply 才写入 Project v2 Iteration 字段）。触发：'今日计划' / 'daily plan' / 'standup' / '排今天的活' / 'iteration 排期'。"
argument-hint: "[--apply] [--date=YYYY-MM-DD]"
allowed-tools: [Bash, Read, Write, Agent]
---

# Daily Planner Skill

调度 backlog → 当日 Project v2 Iteration。**默认 dry-run**（只输出 markdown brief），`--apply` 才真写入。Backlog 真值源见 `docs/backlog.md`。

## 参数

| Flag | 默认 | 含义 |
|------|------|------|
| `--apply` | off | 写入 Project v2 Iteration 字段；未传则纯 dry-run |
| `--date=YYYY-MM-DD` | today（系统 TZ） | 目标排期日期 |

---

## 常量（2026-05-24 实测；详见 agent 文件 §Project v2 常量）

```bash
PROJECT_NODE_ID="PVT_kwHOBjsrB84BYQ3m"
ITERATION_FIELD_ID="PVTIF_lAHOBjsrB84BYQ3mzhTX_HQ"
```

如 Project v2 重建或迁移，更新 agent 文件常量块后同步这里。

## 阶段 1：Token scope fail-check

```bash
gh auth status 2>&1 | grep -q "'project'" && HAS_PROJECT=true || HAS_PROJECT=false
```

| 状态 | 模式 |
|------|------|
| `HAS_PROJECT=true` + `--apply` | 完整模式：读 + 写 Iteration |
| `HAS_PROJECT=true` + dry-run | 完整模式但不写：读 Project v2 字段、计算 diff、不调 `item-edit` |
| `HAS_PROJECT=false` + `--apply` | **报错退出**，提示 `gh auth refresh -s project` |
| `HAS_PROJECT=false` + dry-run | 降级"只读 brief"模式：跳过所有 `gh project` 命令，仅基于 `gh issue list` label 维度排序 |

云沙箱 token 默认无 `project` scope（详见 `docs/backlog.md` §云沙箱查询），降级模式必须可用。

## 阶段 2：拉取数据

```bash
WORKDIR="/tmp/daily-planner/$(date +%Y%m%d-%H%M%S)"
mkdir -p "$WORKDIR"
DATE="${DATE:-$(date +%Y-%m-%d)}"

# 2.1 Backlog 池（label 维度，全量；当前 263 items < limit）
gh issue list --repo ghbvf/gocell --label backlog --state open \
  --json number,title,labels,createdAt,body,url --limit 300 \
  > "$WORKDIR/issues.json"

# 2.2 Project v2 items + 当前字段值（仅 HAS_PROJECT=true）
if [[ $HAS_PROJECT == true ]]; then
  gh project item-list 3 --owner ghbvf --format json --limit 300 \
    > "$WORKDIR/items.json"

  # 2.3 Iteration sprint 配置（每次重查，option ID 随 sprint 切换漂移）
  gh api graphql -f query='query {
    user(login:"ghbvf"){ projectV2(number:3){
      field(name:"Iteration"){ ... on ProjectV2IterationField {
        configuration { iterations { id title startDate duration } }
      }}
    }}
  }' > "$WORKDIR/iterations.json"

  # 推断 target date 落在哪个 sprint（startDate ≤ DATE < startDate + duration）
  # 注意：pipe 后 root context 会丢失，必须用 `as $it` 绑定保留访问
  TARGET_ITERATION_ID=$(jq -r --arg d "$DATE" '
    .data.user.projectV2.field.configuration.iterations[]
    | . as $it
    | select($it.startDate <= $d
             and ($d < ((($it.startDate | strptime("%Y-%m-%d") | mktime) + ($it.duration * 86400))
                         | strftime("%Y-%m-%d")))).id
  ' "$WORKDIR/iterations.json")

  if [[ -z "$TARGET_ITERATION_ID" ]]; then
    echo "ERROR: no sprint iteration covers $DATE" >&2
    echo "Hint: add a new iteration in Project v2 UI before re-running --apply" >&2
    exit 1
  fi
fi

# 2.4 Sub-issue 关系（GraphQL 原生 + body grep 双源）
gh api graphql -H "GraphQL-Features: sub_issues" -f query='query {
  repository(owner:"ghbvf",name:"gocell"){
    issues(first:100, labels:["bundle-parent","backlog"], states:OPEN){
      nodes {
        number title
        subIssuesSummary { total completed }
        subIssues(first:50){ nodes { number title state } }
      }
    }
  }
}' > "$WORKDIR/sub-issues.json" 2>/dev/null || echo '{}' > "$WORKDIR/sub-issues.json"
```

> **Sprint vs Daily 语义**（详见 agent 文件同名章节）：本 skill 写入 Project v2 Iteration = "把 issue 加入 current sprint"。Daily 焦点队列在 brief 本地落盘。Sprint 切换日 `TARGET_ITERATION_ID` 自动指向新 sprint。

## 阶段 3：派发 daily-planner agent 评分排序

```
Agent(
  description: "Score backlog and emit daily brief",
  subagent_type: "daily-planner",
  prompt: """
    Data files: $WORKDIR/{issues,items,fields,sub-issues}.json
    Mode: ${APPLY:+apply}${APPLY:-dry-run}
    Date: ${DATE:-$(date +%Y-%m-%d)}
    Target iteration option ID: $TARGET_ITERATION_ID
    HAS_PROJECT: $HAS_PROJECT

    Read the 4 files, score & sort per WSJF 简化版，emit brief to
    ~/.local/share/gocell-daily/${DATE}.md following 输出格式 章节。
    Also emit machine-readable plan to $WORKDIR/plan.json
    形如 [{item_id, issue, target_iteration_id, current_iteration_id, action}]，
    供阶段 4 apply 消费。dry-run 模式只输出 brief 与 plan.json，不要建议任何写入。
  """
)
```

## 阶段 4：Apply 分支（仅 `--apply` + `HAS_PROJECT=true`）

```bash
# 4.1 快照（apply 前防丢失，回滚依据）
SNAPSHOT="$WORKDIR/snapshot-before.json"
cp "$WORKDIR/items.json" "$SNAPSHOT"

# 4.2 逐项写入（客户端幂等：当前值 == 期望值 → skip）
AUDIT=".claude/logs/daily-planner-$(date +%Y%m%d).jsonl"
mkdir -p "$(dirname "$AUDIT")"

jq -c '.[]' "$WORKDIR/plan.json" | while read -r row; do
  item_id=$(jq -r '.item_id' <<<"$row")
  cur_iter=$(jq -r '.current_iteration_id // empty' <<<"$row")
  tgt_iter=$(jq -r '.target_iteration_id' <<<"$row")

  if [[ "$cur_iter" == "$tgt_iter" ]]; then
    echo "SKIP (no-op) $item_id" >&2
    continue
  fi

  if gh project item-edit \
       --project-id "$PROJECT_NODE_ID" \
       --id "$item_id" \
       --field-id "$ITERATION_FIELD_ID" \
       --iteration-id "$tgt_iter"; then
    jq -nc --arg ts "$(date -u +%FT%TZ)" --arg item "$item_id" \
       --arg old "$cur_iter" --arg new "$tgt_iter" --arg snap "$SNAPSHOT" \
       '{ts:$ts, mode:"apply", item_id:$item, field:"Iteration",
         old:$old, new:$new, snapshot:$snap}' >> "$AUDIT"
  else
    # 失败：输出 rollback 命令清单到 stderr，不自动执行
    echo "FAIL $item_id" >&2
    echo "ROLLBACK: gh project item-edit --project-id $PROJECT_NODE_ID --id $item_id --field-id $ITERATION_FIELD_ID --iteration-id $cur_iter" >&2
    jq -nc --arg ts "$(date -u +%FT%TZ)" --arg item "$item_id" \
       --arg snap "$SNAPSHOT" \
       '{ts:$ts, mode:"apply", item_id:$item, status:"failed",
         snapshot:$snap, partial:true}' >> "$AUDIT"
  fi
done
```

## 阶段 5：报告

输出给用户：
- Brief 路径：`~/.local/share/gocell-daily/<DATE>.md`（点击可看）
- Plan 路径：`$WORKDIR/plan.json`（机器可读）
- 写入数 / 跳过数 / 失败数（仅 apply 模式）
- Snapshot：`$SNAPSHOT`（仅 apply 模式）
- Audit log：`$AUDIT`（仅 apply 模式）
- 任何 `[NEEDS PRIORITY]` / `[MISSING LABEL]` / `[CAP COLLISION]` / `[需人工确认]` 警告

## 约束

- 所有 `gh` 命令 `dangerouslyDisableSandbox: true`
- **默认 dry-run；`--apply` 必须显式 opt-in**（首次使用一定先看 brief）
- 只写 Project v2 Iteration 字段；**不动 Status / Estimate / labels**（这些是人控的进度语义）
- 不创建 / 修改 / 关闭 issue
- 不修改代码、不跑 build/test
- Apply 失败不自动回滚（输出回滚命令清单交人决策，避免二次破坏）
- Iteration option ID 每次重查（sprint 切换漂移），不缓存到 agent 文件常量
- Snapshot + audit log 落 `/tmp/daily-planner/` 与 `.claude/logs/`（已被 .gitignore）
