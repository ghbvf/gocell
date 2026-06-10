---
name: pr-monitor
description: "Claude 自动循环等待 fix 侧：ScheduleWakeup 驱动的 in-session PR 状态监控，human-in-loop 可随时中断。默认 report 模式（#1657，仅观察+提示）；auto 模式（#1663）在 Cx1-only + IN_SCOPE + ≤2 文件 + 非 kernel/migration/bootstrap/并发边界内自动调用 /fix。路由依据为 PR label（非 block 字段）。在 pr-status/ready 或 PR 关闭时自动终止。"
argument-hint: "<PR#> [--mode report|auto] [--fix-engine claude|codex] [--role fix|review]"
allowed-tools: [Bash, Read, Skill, Agent]
disable-model-invocation: true
---

# pr-monitor — Claude 自动循环等待（fix 侧）

> **适用场景**：需要 Claude in-session 持续监控一个 PR，观察 review findings 并在满足条件时自动（或提示人工）调用 `/fix`。
> 由 `/loop` 用户指令触发，或由 `ship/fix` 完成钩子调用。
>
> **loop 原语**：`ScheduleWakeup(seconds)`，clamps [60, 3600]。tick 状态（pr / cursor / tickCount / mode / fixEngine）由 host LLM 在 ScheduleWakeup wakeup payload 中 tick-to-tick 携带，**不写文件**。

---

## §0 角色与边界

**loop substrate**：Claude in-session ScheduleWakeup 自踏步（human 全程在场，可随时 Ctrl-C 中断）。

**主要角色**：fix 侧监控（默认）；`--role=review` 时切换为 review 侧（见 §7）。

**engine knob 说明**：`--fix-engine` 选择 **fix 侧**引擎（`claude|codex`）——这是与 codex-pr-router 的 `GOCELL_ROUTER_REVIEW_ENGINE`（review 侧引擎）**完全独立的两个轴**，不要混用或合并。

**路由依据**：所有分支判定基于 **PR label**（`gh pr view <N> --json labels,state`），**不基于** block 字段中的 `next.agent`（block 字段仅供 §6.3 熔断判定参考）。

### 两种模式

| 模式 | Issue | 行为 |
|------|-------|------|
| **report**（默认） | #1657 | 仅观察 + 窗口打印，不自动执行任何修改，不切 label，不调 /fix |
| **auto** | #1663 | 在 §6.4 全部条件成立时自动 in-session 调用 /fix；其余情况仍只报告 |

**report 模式绝不做**：不写代码、不切 label、不贴评论、不调 `/fix`。

---

## §1 输入解析

```bash
# PR 号：strip 前导 '#', 断言为正整数
PR="${1#\#}"
[[ "$PR" =~ ^[0-9]+$ ]] || { echo "error: invalid PR number: $1"; exit 1; }

# flag defaults
MODE="report"        # report | auto
FIX_ENGINE="claude"  # claude | codex
ROLE="fix"           # fix | review

# 解析剩余参数
shift
while [[ $# -gt 0 ]]; do
  case "$1" in
    --mode=*)      MODE="${1#--mode=}" ;;
    --fix-engine=*) FIX_ENGINE="${1#--fix-engine=}" ;;
    --role=*)      ROLE="${1#--role=}" ;;
    *)             echo "unknown flag: $1" >&2; exit 1 ;;
  esac
  shift
done
```

**PR 存在性验证**：

```bash
gh pr view "$PR" --json number,state,labels \
  || { echo "error: PR #$PR not found or gh auth failed"; exit 1; }
```

---

## §2 游标初始化

**首次 tick**：cursor = 当前所有评论中 `created_at` 的最大值（这样启动我们的 ship/fix 评论本身不会被当作新 findings 重复触发）。

> 注意：`gh api repos/.../issues/<N>/comments` REST 端点返回 **snake_case** 字段（`created_at`），
> 而 `gh pr view --json comments` 返回 camelCase（`createdAt`）。这里统一使用前者，
> cursor 初始化和 §3 过滤器均使用 `created_at`。

```bash
# 初始化 cursor（首 tick 时执行）
CURSOR=$(gh api repos/ghbvf/gocell/issues/${PR}/comments \
  --jq '[.[].created_at] | max // ""')
TICK_COUNT=0
```

**Tick 载荷**（ScheduleWakeup wakeup payload 中携带；结构由 host LLM 在每次调度时维护）：

```json
{
  "pr": "<PR#>",
  "cursor": "<ISO8601 timestamp>",
  "tickCount": 0,
  "mode": "report|auto",
  "fixEngine": "claude|codex",
  "role": "fix|review"
}
```

> `role` 必须随 payload 携带，否则重唤醒后 `--role` 丢失，loop 会静默回退为默认 fix 角色。

---

## §3 每 tick 逻辑（顶层控制流）

每次 ScheduleWakeup 唤醒后按以下顺序执行：

1. 读取 PR 当前状态（一次 gh 调用）：

   ```bash
   STATE=$(gh pr view "$PR" --json state,labels,comments \
     --jq '{state:.state, labels:[.labels[].name], commentCount:(.comments|length)}')
   ```

2. **§4 终止检查**（优先，发现终止条件即停，不继续）
3. 拉取增量评论（`createdAt > cursor`，仅信任来源）：

   ```bash
   NEW_COMMENTS=$(gh api repos/ghbvf/gocell/issues/${PR}/comments \
     --jq --arg cur "$CURSOR" \
     '[.[] | select(.created_at > $cur and
       (.author_association == "OWNER" or .author_association == "MEMBER" or .author_association == "COLLABORATOR") and
       (.body | test("pm:pr-review|pm:fix|codex review")))]')
   ```

4. 推进 cursor = 最新评论 `createdAt`（若 `NEW_COMMENTS` 非空）
5. 按 `$ROLE` / `$MODE` 分支：`--role=review` → §7；否则按 `$MODE` → §5（report）或 §6（auto）
6. 递增 `TICK_COUNT`；`ScheduleWakeup(1800)` 调度下次 tick（终止条件已在步骤 2 中止）；
   wakeup payload 携带 `{pr, cursor, tickCount, mode, fixEngine, role}` 全部字段（见 §2），
   确保 `role` 在每次 re-entry 时正确恢复。

---

## §4 终止条件（任一成立即停，不再 ScheduleWakeup）

| 条件 | 判定 | 窗口输出 |
|------|------|---------|
| `pr-status/ready` 在 label 中 | `echo $LABELS \| grep pr-status/ready` | "PR #N 已 ready，监控结束" |
| PR 状态 != OPEN | `state != "OPEN"` | "PR #N 已关闭（state=$STATE），监控结束" |
| tickCount >= 48（约 24h） | `[[ $TICK_COUNT -ge 48 ]]` | "监控超时（48 ticks ~24h），请人工检查 PR #N" |
| §6.3 熔断触发 | `cycle.exhausted == true` or round >= 3 | 见 §6.3 |

> **auto /fix 触发（§6.4）不是终止条件**：/fix 完成后 loop 切换到等待 `--check` 结论（`pr-status/needs-check-fix`），继续 ScheduleWakeup。终止只在上表条件之一成立时发生。

---

## §5 report 模式（#1657，默认）

**每 tick 逻辑**：

```
if pr-status/needs-fix IN labels AND NEW_COMMENTS 非空 AND 含 findings marker:
    → STOP loop（不再 ScheduleWakeup）
    → 窗口打印 to-fix 清单（见下）
    → 提示人工运行: /fix <N>
else:
    → 窗口打印: "tick $TICK_COUNT — PR #N 无新 findings，下次检查 in ~30min"
    → ScheduleWakeup(1800)
```

**findings 清单提取**（text-scrape 最新 pm:pr-review `<details>` Finding 行）：

```bash
# 从最新 findings 评论提取 Finding 行（格式：**F1** [P1·Cx2] `path/to/file.go:120` — ...）
LATEST_REVIEW=$(echo "$NEW_COMMENTS" | jq -r '.[-1].body // ""')
echo "$LATEST_REVIEW" | grep -oP '\*\*F\d+\*\*[^\n]*`[^`]+:\d+`[^\n]*' || \
echo "$LATEST_REVIEW" | grep -oP '\*\*F\d+\*\*[^\n]*'
```

> 旧 pattern `\bfile:line\b` 匹配字面字符串 "file:line"，永不命中真实 Finding 行（Finding 行
> 格式为 `` **F1** [Cx1] `path/to/file.go:42` — 描述 ``）。新 pattern 先匹配带有
> `` `path:lineno` `` 的 bold-F 行，fallback 匹配所有 bold-F 前缀行。

**窗口打印格式**：

```
PR #<N> — 需要修复（pr-status/needs-fix）
新 findings（来自 <评论 URL>）：
  F1 [Cx1] cells/foo/bar.go:42 — <描述>
  F2 [Cx2] ...
建议：/fix <N>
（report 模式；如需自动修复请以 --mode=auto 重启 pr-monitor）
```

> report 模式到此停止。不写任何代码，不切 label，不贴评论。

---

## §6 auto 模式（#1663 — "Auto fix 改为 Claude 自动循环等待"）

### §6.1 CI 判定

```bash
gh pr checks "$PR" --json name,bucket \
  | jq -r '.[] | select(.bucket=="fail") | .name'
```

若有失败 check：窗口打印失败列表 + "CI 修复是 producer 的 inline job；pr-monitor 仅报告，不自动 /fix CI"。**不调 /fix 修 CI**（决策 4：CI fix 由 producer 负责）。继续 ScheduleWakeup 等待 CI 恢复。

### §6.2 冲突判定（复用 issues B5）

```bash
MERGEABLE=$(gh pr view "$PR" --json mergeable,mergeStateStatus \
  | jq -r '.mergeable')
```

若 `MERGEABLE == "UNKNOWN"`：轮询（最多 5 次，间隔 10s）直到落定。

若 `MERGEABLE == "CONFLICTING"` 或 `mergeStateStatus == "DIRTY"`：

```bash
# 解冲突（在 PR 的已有 dev worktree 中执行；不新建 worktree）
# 优先复用 codex-pr-router 管理的 worktree（若 pr-monitor 在其中运行）；
# 否则查找 PR 分支对应的已有 dev worktree。
HEAD_REF=$(gh pr view "$PR" --json headRefName --jq .headRefName)
WT_PATH=$(git worktree list --porcelain | awk -v b="$HEAD_REF" '
  /^worktree / { wt=$2 }
  /^branch / && $2 == "refs/heads/"b { print wt; exit }
')
if [[ -z "$WT_PATH" ]]; then
  echo "pr-monitor: no existing worktree for branch $HEAD_REF; please resolve conflict manually" >&2
else
  git -C "$WT_PATH" fetch origin && \
    git -C "$WT_PATH" merge origin/develop --no-edit && \
    git -C "$WT_PATH" push
fi
```

解冲突后回 §6.2 重检。若无已有 worktree 可用：窗口打印冲突 + 建议人工解决（不新建 worktree，避免与 router 或已有 dev worktree 命名冲突）。

### §6.3 findings 消费 + 熔断判定

**优先用机器块**：

```bash
BLOCK=$(bash hack/automation/pr-meta.sh extract "$PR" 2>/dev/null)
EXIT_CODE=$?
# exit 0 → 有效 fresh block → 解析 byCx / cycle.exhausted / next.agent
# exit 3 → stale block → 降级 text-scrape
# exit 2 → 无 block → 降级 text-scrape
```

**熔断判定**（任一成立则不 dispatch，报告熔断）：

- `cycle.exhausted == true`（block 字段）
- `next.agent == "human"`（block 字段）
- text-scrape fallback 时：`bash hack/automation/pr-meta.sh round "$PR"` >= 3（maxRounds）

窗口打印：

```
PR #<N> 熔断：review↔fix 已达 3 轮上限，转人工处理。
建议：gh pr view <N> --web
```

停止 loop（终止，不 ScheduleWakeup）。

### §6.4 Claude 自动 /fix 触发条件（全部成立才触发）

| 条件 | 判定方法 |
|------|---------|
| `pr-status/needs-fix` 在 label 中 | label check |
| 有新 findings 评论（createdAt > cursor） | `NEW_COMMENTS` 非空 |
| 未熔断 | §6.3 通过 |
| **Cx1-only**：block `findings.byCx.cx2 == 0 && cx3 == 0 && cx4 == 0` 且 `cx1 > 0`；text-scrape fallback：finding 标签全为 `[Cx1]` | block 或 text-scrape |
| **IN_SCOPE**：finding 文件在 PR diff 中 | `gh pr diff $PR --name-only` |
| **≤2 文件**：受影响文件数 ≤ 2 | count diff files |
| **非禁止域**：不触及 kernel 接口/migration/bootstrap 初始化/并发语义 | 检查文件路径（kernel//*.go 且改导出接口；*/migrations/*.sql；bootstrap/run*.go；含 sync/atomic/channel send 的文件） |

参照 fix §3.4 [AUTO-FIX] 边界 + §不可自动执行清单：并发语义变更、接口签名修改、新依赖、数据流方向变更、Cx2+ 均**不可自动执行**。

**当 `--fix-engine=claude`（默认）且全部条件成立**：

```
# host LLM in-session 调用（via Skill 工具）
Skill("fix", args="<N>")
```

fix 完成后会贴 pm:fix + 切 `pr-status/needs-check-fix`。继续 ScheduleWakeup 等待 `/pr-review --check` 结果。

### §6.5 Cx2 → 不自动修（输出 backlog draft）

若 block/text-scrape 显示有 Cx2 findings（且无 Cx1 可独立处理）：

窗口打印：

```
PR #<N> 含 Cx2 findings，不自动修复（决策 3：Cx1 proven 后再开放 Cx2 auto-fix）。
建议人工: /fix <N>

backlog issue draft（Cx2 follow-up）:
gh issue create \
  --label backlog --label pri-p2 --label area-<XX> --label type-fu --label cx-2 \
  --title "[#<N>] Cx2 finding follow-up: <简述>" \
  --body-file <填好的 .github/project-template/backlog.md>
```

对 Cx3+/kernel/migration/并发：同样报告 + "建议人工 /fix"，不输出 backlog draft（Cx3+ 需人工决策，fix §3.1）。

### §6.6 engine-knob: `--fix-engine=codex`

当 `--fix-engine=codex`：pr-monitor 不在 session 内调用 /fix。

行为：确认 PR 需要 `ai/local-fix` label：

```bash
gh pr edit "$PR" --add-label ai/local-fix
```

窗口打印：

```
PR #<N> 已贴 ai/local-fix label，等待 codex-pr-router daemon（Batch 4）处理。
pr-monitor 继续监控，不在 session 内运行 /fix。
```

继续 ScheduleWakeup 等待 daemon 处理后的状态变更。

### §6.7 终止扩展：连续安静 ticks

在 `pr-status/ready` 出现前，若连续 N（≈ 2-3）个 tick 无新 findings 且无 label 变化，不提前终止——继续等待（ready 才是唯一正常出口）。超时兜底见 §4（48 ticks）。

---

## §7 alternate review 能力（`--role=review`）

当 `--role=review`：

```bash
# Claude engine（默认）
claude -p "/pr-review $PR"
```

`--fix-engine=codex` 时：**不**在 session 内跑 `/fix`，改由 codex-pr-router daemon 的 gated workspace-write 路径接管（daemon 轮询 `pr-status/needs-fix` ∧ `ai/local-fix`）。pr-monitor 只确认 PR 已带 `pr-status/needs-fix`（本就是把我们带到此处的触发标，**不切 needs-review-again** —— 那会错误路由回 review 侧），并提示「需贴 `ai/local-fix` label 该 PR 才会被 daemon 接管」，然后继续 loop 等 codex `--check` 结论。

review 结果由 /pr-review 技能贴评论 + 切 label，pr-monitor 继续 loop 等待 fix 侧响应。

---

## §8 沟通规则

**窗口打印是主输出**；pr-monitor 自身不贴 PR 评论（贴评论是 /fix 或 /pr-review 的职责）。

| 模式 | 允许的副作用 |
|------|------------|
| report | 无（只读 + 窗口打印） |
| auto | §6.4 满足时调 Skill("fix")；§6.2 冲突时 git merge + push；§6.6 时贴 ai/local-fix label |

**label 切换**：pr-monitor 不直接切 `pr-status/*`（由 /fix 或 /pr-review 完成）。唯一例外：§6.6 贴 `ai/local-fix`。

**不自动处理**的情况统一报告 + 建议人工命令，不 AskUserQuestion（human-in-loop 已在场）。

---

## 附：tick wakeup payload 示例

```json
{
  "pr": "1234",
  "cursor": "2026-06-07T10:30:00Z",
  "tickCount": 3,
  "mode": "auto",
  "fixEngine": "claude",
  "role": "fix"
}
```

host LLM 在每次 ScheduleWakeup 调度时将上述 JSON 作为 wakeup payload 传入，唤醒后从 payload 恢复状态，继续 §3 逻辑。
