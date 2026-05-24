---
name: ship
description: "全流程实施：探索→计划→worktree→TDD→实施→PR→review→/fix Cx1/Cx2→人工确认。L1(跳过探索,1 reviewer)/L2(单agent探索,1 reviewer)/L3(默认,三agent探索,按diff行数1/2/3/6 reviewer自动)"
argument-hint: "[--level=L1|L2|L3] (<#issue-number 或任务描述> | --from-plan=<path>)"
allowed-tools: [Read, Write, Edit, Glob, Grep, Bash, Agent, AskUserQuestion]
---

# GoCell Ship — 全流程实施

默认 L3（三 agent 探索 + 详细计划 + 与用户确认 + 按 diff 行数自动 1/2/3/6 reviewer，见阶段 7）。

剥离 `--level=` 和 `--from-plan=` flag 后，剩余参数匹配 `^#?[0-9]+$` 时视为 issue 号，先 `gh issue view <N> --json title,body,labels,state`（`dangerouslyDisableSandbox: true`）拉取作为任务上下文；后续阶段以 issue title/body 替代自由文本任务描述，阶段 6 PR body 追加 `Closes #<N>`。`state != "OPEN"`（CLOSED / MERGED 等）或 `gh issue view` 失败均用 AskUserQuestion 让用户裁定是否继续。

**`--from-plan=<path>` 与 positional issue 号参数互斥，不能同时传。** 同时给出时用 AskUserQuestion 让用户裁定使用哪个。`--from-plan` 触发"plan-driven 模式"，见阶段 0.5。

## 等级

| 等级 | 探索 | 计划确认 | 实施 agent | review |
|------|------|---------|-----------|--------|
| L1 | 不探索 | 不需要 | 1-2 并行 | 1 reviewer |
| L2 | 1 explorer | 展示给用户 | 1-2 并行 | 1 reviewer |
| L3（默认） | 3 并行 explorer | AskUserQuestion 确认 | ≤ 4 并行 | 1/2/3/6 reviewer（按 diff 行数自动，见阶段 7） |

---

## C3a 前置：Project v2 配置（已完成）

/ship 的 Status 写入依赖 Project v2（owner `ghbvf`，project number `3`）的内建 Status 字段（single-select，已存在）。Workflow 配置状态：
- **Wave 字段**：已建好（single-select，options Wave 1/2/3/4）——daily-planner 负责写入。
- **Status workflow**：
  - `Item added to project` → Backlog（自动）
  - `Pull request linked to issue`（`Closes #N`）→ In review（自动，由 PR body `Closes` link 触发）
  - `Pull request merged` → Done（自动）
- **/ship 在阶段 3 显式写 In progress**；In review 交 Project v2 内建 workflow，/ship 不写。

详见 daily-planner SKILL.md §C3a。

---

## 阶段 0.5：plan-driven 模式（仅 `--from-plan=<path>`）

本阶段仅在传入 `--from-plan=<path>` 时执行；传入 issue 号时跳过，直接进阶段 1。

> `<path>` 来自 `/daily-planner --apply` 输出的 `PLAN_PATH`（daily-planner 阶段 5 会在终端显式输出该路径）。

```bash
# 读取 plan.json（daily-planner 产出）
PLAN_PATH="<path>"   # --from-plan 的值
plan=$(cat "$PLAN_PATH")

# 过滤：action=="set" 的条目（已知今日 target_iteration_id 由调用方确认）
entries=$(echo "$plan" | jq '[.[] | select(.action=="set")]')

# 按 parallel_group 升序排列（同 group 并行，不同 group 串行）
groups=$(echo "$entries" | jq '[.[].parallel_group] | unique | sort')

# 幂等：逐 group 判定完成度（所有 issue 的 PR 均 MERGED/CLOSED = 该 group 完成）
# 启动第一个"未完成的 group"，起完即结束本次调用
for group_id in $(echo "$groups" | jq -r '.[]'); do
  group_issues=$(echo "$entries" | jq -r --argjson g "$group_id" '[.[] | select(.parallel_group==$g) | .issue_number] | .[]')
  group_done=true
  for n in $group_issues; do
    # dangerouslyDisableSandbox: true
    pr_state=$(gh pr list --repo ghbvf/gocell --search "#$n" --json state -q '.[0].state // "NONE"' 2>/dev/null || echo "NONE")
    # 也尝试 gh pr view --head 匹配
    if [[ "$pr_state" != "MERGED" && "$pr_state" != "CLOSED" ]]; then
      group_done=false
      break
    fi
  done
  if $group_done; then
    echo "INFO: group $group_id 已完成（PR 全 MERGED/CLOSED），跳过" >&2
  else
    echo "INFO: 启动 group $group_id（issues: $group_issues）" >&2
    # 对 group 内每个 issue 并行起 /ship #N 流程（复用下方 issue-number 路径）
    # 组内 >4 个 issue → 子批，每批 ≤4 并行
    break
  fi
done
# 若所有 group 均已完成（循环未 break），提示用户
all_complete=true
for group_id in $(echo "$groups" | jq -r '.[]'); do
  group_issues=$(echo "$entries" | jq -r --argjson g "$group_id" '[.[] | select(.parallel_group==$g) | .issue_number] | .[]')
  for n in $group_issues; do
    pr_state=$(gh pr list --repo ghbvf/gocell --search "#$n" --json state -q '.[0].state // "NONE"' 2>/dev/null || echo "NONE")
    if [[ "$pr_state" != "MERGED" && "$pr_state" != "CLOSED" ]]; then
      all_complete=false
      break 2
    fi
  done
done
if $all_complete; then
  echo "INFO: 全部 parallel_group 已完成，无新 group 可启动" >&2
fi
```

**幂等重跑**：每次 `/ship --from-plan=<path>` 调用只启动第一个未完成的 group；人工 review/merge 本组 PR 后，重跑 `/ship --from-plan=<path>` 继续下一 group。状态从 PR 派生，无本地持久化。

**parallel_group 契约**（plan.json 字段）：`parallel_group` 为 `int ≥ 1`，全 plan 全局唯一编号，同 group 可并行，异 group 串行（等上一 group 全 PR MERGED/CLOSED）。跨 wave 不共组。由 daily-planner agent STEP 6 派生（union-find on affected_paths），apply-gate.sh schema 校验该字段。

---

## 阶段 1：探索（L1 跳过）

**L2**：启动 1 个 `explorer` agent，研究对标开源项目实现方案，查 `docs/references/framework-comparison.md` 找 primary 对标框架，用 WebFetch 拉取源码（`raw.githubusercontent.com`），提取接口签名、生命周期、错误处理关键设计，输出采纳建议和偏离理由。

**L3（默认）**：并行启动 3 个 `explorer` agent：
1. **对标开源项目实现方案**
2. 测试策略（table-driven / 集成 / benchmark 覆盖模式）
3. 边界条件与安全处理

全部完成后按"方案与计划原则"汇总，执行下方"反思自检"，再**用 AskUserQuestion 与用户确认方案方向**后继续。

---

## 方案与计划原则（阶段 1 汇总 / 阶段 2 计划必须满足）

- **彻底**：根因 + 完整解法，不留 TODO/FIXME/follow-up；范围内紧密相关的小工作一并纳入，不拆 P2/后续 PR
- **不向后兼容**：删字段/改签名/换实现直接做，不留 deprecation 别名、不留兼容 shim、不留旧路径
- **优雅简洁**：用最少的代码改动达成目标，不引入新抽象层、不预设未来需求
- **开源对标**：做了嘛，方向正确吗

### 反思自检（AskUserQuestion 前强制执行）

呈现给用户前，逐条自查并在确认问题中如实回答：

1. **彻底**：方案/计划里是否还藏着 TODO、兼容代码、未列入范围的关联工作？→ 合并进当前 PR 或写明 blocker 理由
2. **不向后兼容**：是否引入了 deprecation 别名、旧字段保留、双路径并存？→ 删掉或写明保留理由
3. **优雅简洁**：能否用更少的代码、更少的抽象、更少的新文件达成同样目标？→ 简化或写明保留理由

任一条不通过 → AskUserQuestion 中**显式列出取舍及理由**，不得默认放行。

---

## 阶段 2：计划（L1 跳过）

按"方案与计划原则"生成改动文件清单（按依赖顺序）、任务分组（串行/并行批次）、TDD 测试先写清单、对标参考（`ref: framework file`）。生成后执行"反思自检"，L3 用 AskUserQuestion 与用户确认计划后继续。

**并行批次分析**（改动文件 ≥ 4 时必须在计划中明确）：
- 标注各任务的文件归属和批次编号
- 标注批次间依赖关系（有依赖 → 串行；无依赖 → 可并行）
- 解决同文件冲突：同一文件必须归入同一批次/agent

---

## 阶段 3：Worktree

基于 `origin/develop` 创建（依照 `git-worktree` skill 约定）：

```bash
git fetch origin
git worktree add worktrees/<NNN-short-name> -b <branch-name> origin/develop
```

编号：Fix 200-299 / Feature 001-199 / Refactor 500-599，扫描 `worktrees/` + `git branch -a` 取最大 +1。

### 3.1 Project v2 Status → In progress（issue/plan 模式）

worktree 建好后，若本次调用携带 issue 号（单 issue 模式或 --from-plan fan-out 的单条），将对应 Project item 的 Status 写为 `In progress`。free-text /ship（无 issue 号）跳过此步骤（N/A，非降级）。

**preflight**（同 daily-planner 风格，fail-fast）：

```bash
# dangerouslyDisableSandbox: true（所有 gh 命令）
if ! AUTH_STATUS=$(gh auth status 2>&1); then
  echo "ERROR: gh auth status failed; run: gh auth login" >&2; exit 1
fi
if ! grep -qiE "scopes:.*\bproject\b" <<<"$AUTH_STATUS"; then
  echo "ERROR: token missing 'project' scope; run: gh auth refresh -s project" >&2; exit 1
fi
```

**Status 写入流程**：

```bash
ISSUE_NUMBER=<N>  # 本次实施的 issue 号

# 动态查 Project node ID + Status field ID + "In progress" option ID
PROJECT_NODE_ID=$(gh project view 3 --owner ghbvf --format json | jq -r '.id')
FIELD_LIST_JSON=$(gh project field-list 3 --owner ghbvf --format json)
STATUS_FIELD_ID=$(jq -r '.fields[] | select(.name=="Status").id' <<<"$FIELD_LIST_JSON")
IN_PROGRESS_OPTION_ID=$(jq -r \
  '.fields[] | select(.name=="Status") | .options[] | select(.name=="In progress") | .id' \
  <<<"$FIELD_LIST_JSON")

# fail-CLOSED：Status 是 Project 内建字段；查不到 = 异常，不静默跳过
[[ -z "$STATUS_FIELD_ID" ]] && {
  echo "ERROR: Status field not found in Project #3; check Project v2 configuration" >&2; exit 1
}
[[ -z "$IN_PROGRESS_OPTION_ID" ]] && {
  echo "ERROR: 'In progress' option not found in Status field; check Project v2 configuration" >&2; exit 1
}

# 查 issue 对应的 item id（by content number）
ITEM_ID=$(gh api graphql -f query='query($proj: ID!, $num: Int!) {
  node(id: $proj) { ... on ProjectV2 {
    items(first: 100) { nodes {
      id
      content { ... on Issue { number } }
    }}
  }}
}' -f proj="$PROJECT_NODE_ID" -F num="$ISSUE_NUMBER" \
  --jq ".data.node.items.nodes[] | select(.content.number==$ISSUE_NUMBER) | .id" 2>/dev/null || true)

[[ -z "$ITEM_ID" ]] && {
  echo "ERROR: issue #$ISSUE_NUMBER not found in Project #3; add it to the project first" >&2; exit 1
}

# 查当前 Status，仅 Backlog/Ready → In progress；已是 In review 不覆盖
# fail-CLOSED：查询失败时 WARN + 跳过写入（不写比误覆盖 In review 安全）
if ! CURRENT_STATUS=$(gh api graphql -f query='query($id: ID!) {
  node(id: $id) { ... on ProjectV2Item {
    status: fieldValueByName(name:"Status") {
      ... on ProjectV2ItemFieldSingleSelectValue { name }
    }
  }}
}' -f id="$ITEM_ID" --jq '.data.node.status.name // ""' 2>&1); then
  echo "WARN: failed to query current Status for issue #$ISSUE_NUMBER; skipping Status write to avoid overwriting In review" >&2
else
  if [[ "$CURRENT_STATUS" == "In review" ]]; then
    echo "INFO: issue #$ISSUE_NUMBER already In review; skipping Status write (In review owned by Project v2 workflow)" >&2
  else
    gh api graphql -f query='mutation($proj: ID!, $item: ID!, $field: ID!, $opt: String!) {
      updateProjectV2ItemFieldValue(input: {
        projectId: $proj, itemId: $item, fieldId: $field,
        value: { singleSelectOptionId: $opt }
      }) { projectV2Item { id } }
    }' -f proj="$PROJECT_NODE_ID" -f item="$ITEM_ID" \
       -f field="$STATUS_FIELD_ID" -f opt="$IN_PROGRESS_OPTION_ID"
    echo "INFO: issue #$ISSUE_NUMBER Status → In progress" >&2
  fi
fi
```

**边界说明**：
- `In progress` 写入仅针对 Backlog / Ready / 其他非 In review 状态；In review 不覆盖（round-2 fix / codex review 留 In review）。
- In review 转换由 `Pull request linked to issue` 内建 workflow 负责（PR body `Closes #N` 触发）。
- /ship 不写 In review，不写 Done（Done 由 PR merged workflow 写）。

---

## 阶段 4：TDD — 先写测试

在 worktree 中先写 `*_test.go`，覆盖正常/边界/错误路径（kernel/ ≥ 90%，其余 ≥ 80%）。运行 `go -C worktrees/<NNN> test ./...` 确认测试先 **FAIL**，再进入实施。

---

## 阶段 5：实施

### 5.0 分组与并行度决策（实施前必须执行）

主 agent 根据阶段 2 的改动文件清单和批次依赖关系，**自主决定**：
- 哪些任务无文件交叉且无逻辑依赖 → 可并行启动 developer agent
- 哪些任务有依赖或改同一文件 → 串行或归入同一 agent

**plan-driven 模式覆盖**：通过 `--from-plan=<path>` 调用时，plan.json 的 `parallel_group` 字段**覆盖**主 agent 自身的文件重合分析——同 `parallel_group` 值的 issue 视为可并行（由 daily-planner agent STEP 6 union-find 已做文件冲突分析），不同 `parallel_group` 值的 issue 串行（等上一 group 全 PR MERGED/CLOSED 后重跑）。非 plan-driven 模式维持原有自主分析。

**硬约束**：
- 同一文件只能分给同一 agent（防写冲突）
- 有前置依赖的批次必须等上一批全部完成后再启动
- 并行 developer agent 上限 **4 个**（plan-driven 模式下组内 >4 个 issue → 子批，每批 ≤4 并行）

### 5.1 Sub-agent prompt 自包含要求

每个 developer sub-agent prompt 必须包含：
- worktree 路径（`worktrees/<NNN>`）
- 分配的任务列表（文件路径 + 改动描述）
- go 命令格式：`go -C worktrees/<NNN> test ./...`
- CLAUDE.md 关键约束（分层规则、覆盖率要求）
- commit 格式：`<type>(<scope>): <描述>`

每个 sub-agent 在自己负责的任务上**串行**执行 Edit-Test Loop，完成后跑 `golangci-lint run ./...`（0 issues 才 commit）。

### 5.2 主 agent 汇总（所有并行 agent 完成后）

```bash
go -C worktrees/<NNN> build ./...
go -C worktrees/<NNN> test ./...
golangci-lint run ./...   # 0 issues 才进阶段 6
```

---

## 阶段 6：PR

```bash
git -C worktrees/<NNN> push -u origin <branch>   # dangerouslyDisableSandbox: true
gh pr create --title "..." --body "..."
```

PR body 包含：Summary、`Refs: <ID>`、`ref: framework file`、Test plan checklist。

---

## 阶段 7：Review

**L1/L2**：1 个 `reviewer` agent（GoCell 六维度）。

**L3**：按 PR diff 净增删行数确定 `reviewer` agent 数量。取数命令（求和 numstat，避开 `--stat` 末行格式随单文件/无删除变化的解析坑）：

```bash
git -C worktrees/<NNN> diff --numstat origin/develop | awk '{i+=$1; d+=$2} END{print i+d}'
```

> 阶段 7 被单独调用（非 ship 完整流程、无 worktree）时，回退到仓库根执行 `git diff --numstat origin/develop | awk '{i+=$1; d+=$2} END{print i+d}'`。

GoCell 六维度 = 架构合规 / 安全 / 测试 / 运维可观测 / DX / 产品。分档（区间左闭右开，边界值归入更高档）：

| diff 行数 | reviewer 数 | 维度切分（不重不漏覆盖六维度全集） |
|-----------|------------|---------|
| `diff < 200` | 1 | 单 agent 跑全六维度 |
| `200 ≤ diff < 600` | 2 | A：架构合规 + 测试 + 产品；B：安全 + 运维可观测 + DX |
| `600 ≤ diff < 1500` | 3 | A：架构合规 + 测试；B：安全 + 产品；C：运维可观测 + DX |
| `diff ≥ 1500` | 6 | 六角色一一对应：架构合规 / 安全 / 测试 / 运维可观测 / DX / 产品 各 1 agent 并行 |

多 agent 时并行启动，每个 agent prompt 自包含其负责维度；全部完成后由主 agent 汇总去重 findings 表（含 Cx 分级）。

---

## 阶段 8：Fix

对 Cx1/Cx2 IN_SCOPE findings 派发 `developer` agent 执行 `/fix <finding>`；Cx3/Cx4 和 OUT_OF_SCOPE 收集到阶段 9。

---

## 阶段 9：人工确认

```
PR: #<编号> <URL>
已完成：TDD / 实施 / PR / review（实跑 reviewer 数：按 diff 1/2/3/6 自动） / Cx1-Cx2 fix / CI

未处理问题（需人工确认）：
| # | Finding | Cx | 建议方案 | 原因 |
|---|---------|----|---------|----|
```

---

## 约束

- lint 0 issues 才 push；不 `--no-verify`；不 amend 已 push commit
- `git push` 用 `dangerouslyDisableSandbox: true`
- worktree merge 后提示用户手动 `git worktree remove`，不自动删除
