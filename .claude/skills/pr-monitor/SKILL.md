---
name: pr-monitor
description: "PR 状态单 tick 检查器：观察一个 PR 的 review/check 进展并按 label 路由。默认 report 模式（#1657，仅观察+窗口提示）；auto 模式（#1663）在 Cx1-only + IN_SCOPE + ≤2 文件 + 非 kernel/migration/bootstrap/并发边界内自动调用 /fix。由 `/loop <interval> /pr-monitor <PR#>` 简单 loop 驱动（每 tick 无状态，只读 label + 机器块）；human-in-loop 可随时中断。pr-status/ready 或 PR 关闭时报告终止。"
argument-hint: "<PR#> [--mode report|auto] [--fix-engine claude|codex] [--role fix|review]"
allowed-tools: [Bash, Read, Skill, Agent]
disable-model-invocation: true
---

# pr-monitor — PR 状态单 tick 检查器（fix 侧）

> **适用场景**：ship/fix 推完 PR 后，持续观察 review/check 侧进展，在满足条件时自动（或提示人工）调用 `/fix`。
>
> **loop 模型（简单）**：本技能是**无状态单 tick**——每次调用只做一次检查就返回。循环交给内建 `/loop` 原语：
> `/loop 30m /pr-monitor <PR#>` 每 30min 重放同一行命令再调用一次（flag 随命令行原样保留，无需跨 tick 携带状态）。
> **不自己调 ScheduleWakeup、不携带 tick payload、不写文件**——每 tick 的状态全部从 PR 实时读取（label + 最新机器块）。
> human-in-loop 全程在场，可随时 Ctrl-C 停 `/loop`。
>
> **如何启动**：用户运行 `/loop 30m /pr-monitor <PR#>`（ship/fix 收尾时会打印这行建议）。单次 `/pr-monitor <PR#>` 也合法——只做一次检查就返回。

---

## §0 角色与边界

**主要角色**：fix 侧监控（默认）；`--role=review` 时切到 review 侧（见 §4）。

**engine knob**：`--fix-engine`（`claude|codex`）选 **fix 侧**引擎，与 codex-pr-router 的 `GOCELL_ROUTER_REVIEW_ENGINE`（review 侧引擎）是**两个独立轴**，不混用。

**路由依据**：所有分支判定基于 **PR label**（`gh pr view <N> --json state,labels`），不基于机器块 `next.agent`（block 字段仅供 §3.3 熔断判定参考）。

| 模式 | 行为 |
|------|------|
| **report**（默认，#1657） | 仅观察 + 窗口打印，**绝不**写代码 / 切 label / 贴评论 / 调 /fix |
| **auto**（#1663） | §3.4 全部条件成立时自动 in-session 调 /fix；其余仍只报告 |

---

## §1 输入解析

```bash
PR="${1#\#}"; [[ "$PR" =~ ^[0-9]+$ ]] || { echo "error: invalid PR number: $1"; exit 1; }
MODE=report; FIX_ENGINE=claude; ROLE=fix        # flag 默认值（每 tick 重解析）
shift; while [[ $# -gt 0 ]]; do case "$1" in
  --mode=*)       MODE="${1#--mode=}" ;;
  --fix-engine=*) FIX_ENGINE="${1#--fix-engine=}" ;;
  --role=*)       ROLE="${1#--role=}" ;;
  *) echo "unknown flag: $1" >&2; exit 1 ;;
esac; shift; done
```

> flag 在每个 `/loop` tick 重新解析（`/loop` 把同一行命令原样重放），不依赖跨 tick payload。

---

## §2 每 tick 逻辑（顶层控制流）

每次 `/pr-monitor` 调用（= 一个 `/loop` tick）按序执行，做完即返回：

1. **读 PR 状态（一次 gh，兼存在性校验）**：
   ```bash
   STATE=$(gh pr view "$PR" --json state,labels --jq '{state:.state, labels:[.labels[].name]}') \
     || { echo "error: PR #$PR not found / gh auth failed"; exit 1; }
   ```
2. **§3.1 终止检查**（优先；命中即打印结束语并返回，提示用户停 `/loop`）。
3. 按 `$ROLE` 分支：`--role=review` → §4；否则按 `$MODE` → report（§3.2）或 auto（§3.3-3.7）。
4. 返回（**不调 ScheduleWakeup**）；下一 tick 由 `/loop` 调度。

> **无 cursor / 无时间戳追增量**：「有无待修 findings」由 **label + 最新机器块**判定（§3.2/§3.3）——`pr-status/needs-fix` 在即「review 给了结论待修」，幂等可重报，不怕 `/loop` 重放。

---

## §3 fix 侧逻辑

### §3.1 终止条件（命中即报告结束，提示停 `/loop`）

| 条件 | 判定 | 窗口输出 |
|------|------|---------|
| `pr-status/ready` ∈ labels | label 含 | "✅ PR #N 已 ready，监控可结束——请停止 /loop" |
| PR state != OPEN | `state != "OPEN"` | "PR #N 已关闭（state=$STATE），请停止 /loop" |
| §3.3 熔断触发 | block `cycle.exhausted` / round≥3 | 见 §3.3 |

> 无 tickCount / 48-tick 超时——human 全程在场，ready/closed 是唯一正常出口；嫌久直接停 `/loop`。

### §3.2 report 模式（默认）

读最新机器块判定有无待修 findings（**不 text-scrape 评论体**）：

```bash
BLOCK=$(bash hack/automation/pr-meta.sh extract "$PR" 2>/dev/null) || BLOCK=""
```

```
if pr-status/needs-fix ∈ labels:
    → 窗口打印 to-fix 摘要（从 BLOCK 的 findings.byCx 取 Cx 计数；
       逐条明细见 PR 上最新 pm:pr-review 评论的 <details>）
    → 提示人工：/fix <N>
else:
    → 窗口打印："tick — PR #N 无待修 label，下次检查由 /loop 调度"
```

> report 模式到此停。不写代码、不切 label、不贴评论。findings 明细不再 `grep -oP` 抓评论体——读结构化机器块，或让用户点开 PR 最新 pm:pr-review 评论。

### §3.3 熔断判定（auto 模式；任一成立 → 不 dispatch，报告熔断）

```bash
BLOCK=$(bash hack/automation/pr-meta.sh extract "$PR" 2>/dev/null); EC=$?
# EC=0 有效 fresh block → 读 findings.byCx / cycle.exhausted / next.agent
# EC=2 无 block / EC=3 stale → 用 round 兜底
ROUND=$(bash hack/automation/pr-meta.sh round "$PR" 2>/dev/null || echo 0)
```

熔断条件：block `cycle.exhausted == true`，或 block `next.agent == "human"`，或 `ROUND >= 3`（maxRounds）。
命中 → 窗口打印 "PR #N 熔断：review↔fix 已达 3 轮上限，转人工处理（`gh pr view <N> --web`）"，返回（请用户停 `/loop`）。

### §3.4 Claude 自动 /fix 触发（全部成立才触发）

| 条件 | 判定方法 |
|------|---------|
| `pr-status/needs-fix` ∈ labels | label check |
| 未熔断 | §3.3 通过 |
| **Cx1-only** | block `findings.byCx`：cx2 == 0 ∧ cx3 == 0 ∧ cx4 == 0 ∧ cx1 > 0 |
| **IN_SCOPE** | finding 文件 ∈ `gh pr diff $PR --name-only` |
| **≤2 文件** | 受影响文件数 ≤ 2 |
| **非禁止域** | 不触及 kernel 导出接口 / `*/migrations/*.sql` / `bootstrap/run*.go` / 含 sync·atomic·channel 的文件 |

参照 fix §3.4 [AUTO-FIX] 边界 + §不可自动执行清单：并发语义变更 / 接口签名修改 / 新依赖 / 数据流方向变更 / Cx2+ 均**不可**自动执行。

**`--fix-engine=claude`（默认）且全部条件成立** → host LLM in-session 调用：

```
Skill("fix", args="<N>")
```

> auto 模式的 `Skill("fix")` 是 **human-approved loop 内**的自动操作——用户已显式 `--mode=auto` 启动 `/loop`，非无监督自主执行；且仅在上表全部窄条件成立（Cx1-only / IN_SCOPE / ≤2 文件 / 非禁止域）时触发。

fix 会贴 pm:fix + 切 `pr-status/needs-check-fix`；下个 `/loop` tick 继续等 `/pr-review --check` 结论（非终止）。

### §3.5 不自动修的情况（报告 + 建议人工，不 AskUserQuestion）

- **Cx2+**：窗口打印 "PR #N 含 Cx2+ findings，不自动修（决策 3：Cx1 proven 后再开放 Cx2 auto-fix）。建议人工 /fix <N>"。
  **不再打印 backlog 草稿**——OOS finding 的建 issue 已由 `/fix` 自动完成（pm:oos 自动建 issue + 回填 #N，见 fix 4.6 step 3）。
- **Cx3+/kernel/migration/并发语义**：同样报告 + "建议人工 /fix"（Cx3+ 需人工决策，fix §3.1）。

### §3.6 冲突解（auto 模式；复用 issues B5）

```bash
MERGEABLE=$(gh pr view "$PR" --json mergeable,mergeStateStatus --jq '.mergeable')
```

`UNKNOWN` → 轮询（≤5 次，间隔 10s）落定。`CONFLICTING` / `mergeStateStatus==DIRTY` → 在 PR 的**已有** dev worktree 内解（不新建，避免与 router 命名冲突）：

```bash
HEAD_REF=$(gh pr view "$PR" --json headRefName --jq .headRefName)
WT_PATH=$(git worktree list --porcelain | awk -v b="$HEAD_REF" \
  '/^worktree / {wt=$2} /^branch / && $2 == "refs/heads/"b {print wt; exit}')
if [[ -n "$WT_PATH" ]]; then
  git -C "$WT_PATH" fetch origin && git -C "$WT_PATH" merge origin/develop --no-edit && git -C "$WT_PATH" push
else
  echo "pr-monitor: 无 PR 分支对应的已有 worktree，请人工解冲突" >&2
fi
```

解完下个 tick 回 §3.1 重检。

### §3.7 engine-knob：`--fix-engine=codex`

不在 session 内调 /fix。确认 PR 需 `ai/local-fix` label：

```bash
gh pr edit "$PR" --add-label ai/local-fix
```

窗口打印 "PR #N 已贴 ai/local-fix，等待 codex-pr-router daemon 处理"；下个 `/loop` tick 继续等 daemon 后的状态变更。

---

## §4 alternate review 能力（`--role=review`）

`--role=review` 且 `--fix-engine=claude`（默认）：

```bash
claude -p "/pr-review $PR"
```

`--fix-engine=codex`：**不**在 session 内跑 /fix，改由 codex-pr-router daemon 的 gated workspace-write 路径接管（daemon 轮询 `pr-status/needs-fix` ∧ `ai/local-fix`）。pr-monitor 只确认 PR 已带 `pr-status/needs-fix`（**不切 needs-review-again**——那会错误路由回 review 侧），提示「需贴 `ai/local-fix` daemon 才接管」，下个 tick 继续等 codex `--check` 结论。

review 结果由 /pr-review 贴评论 + 切 label；下个 `/loop` tick 继续等 fix 侧响应。

---

## §5 沟通规则

**窗口打印是主输出**；pr-monitor 自身不贴 PR 评论（贴评论是 /fix 或 /pr-review 的职责）。

| 模式 | 允许的副作用 |
|------|------------|
| report | 无（只读 + 窗口打印） |
| auto | §3.4 满足时 `Skill("fix")`；§3.6 冲突时 git merge + push；§3.7 贴 `ai/local-fix` label |
| `--role=review` | §4 调 `claude -p "/pr-review"`；review 贴 pm:pr-review 评论 + 切 label 由 /pr-review 完成（非 pr-monitor 自身） |

**label 切换**：pr-monitor 不直接切 `pr-status/*`（由 /fix 或 /pr-review 完成）。唯一例外：§3.7 贴 `ai/local-fix`。

**不自动处理**的情况统一报告 + 建议人工命令，不 AskUserQuestion（human-in-loop 已在场）。
