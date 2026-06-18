---
name: pr-monitor
description: "PR 状态自动接力检查器：ship/fix 收尾约 10min 后必须启动；读取外部 app/review 已产生的 label + 最新机器块，在 needs-fix + 机器可判定 Cx1/Cx2 + 未熔断时 dispatch /fix。文件级/禁止域安全裁决交由 /fix 的 [AUTO-FIX] 门把关；pr-monitor 自身不贴评论、不切 label。"
argument-hint: "<PR#> --mode=auto [--role fix|review]"
allowed-tools: [Bash, Read, Skill, Agent]
---

# pr-monitor — PR 状态自动接力检查器（fix 侧）

> **适用场景**：ship/fix 推完 PR 后，延迟约 10 分钟必须启动一次 `/pr-monitor <PR#> --mode=auto`。外部 app 负责实时监听 `pr-status/needs-review-again` / `pr-status/needs-check-fix` 并执行 review/check；本技能只检查这些流程产出的 label + 机器块，并在满足自动门时接力 `/fix`。
>
> **单 tick 模型**：每次调用只做一次检查就返回；不携带 tick payload、不写文件，状态全部从 PR 实时读取（label + 最新机器块）。

---

## §0 角色与边界

**主要角色**：fix 侧监控（默认）；`--role=review` 时可主动触发 review 侧（见 §4）。

**路由依据**：所有自动分支判定基于 **PR label + 最新 fresh canonical 机器块**（`gh pr view <N> --json state,labels` + `bash hack/automation/pr-meta.sh extract <N>`）。label 表示当前状态，机器块证明状态来源与下一跳契约；二者必须一致。

`--mode=auto` 是唯一支持的运行模式；保留该 flag 是为了让 ship/fix 的收尾命令形态稳定。

---

## §1 输入解析

```bash
PR="${1#\#}"; [[ "$PR" =~ ^[0-9]+$ ]] || { echo "error: invalid PR number: $1"; exit 1; }
MODE=auto; ROLE=fix
shift; while [[ $# -gt 0 ]]; do case "$1" in
  --mode=auto)    MODE=auto ;;
  --role=*)       ROLE="${1#--role=}" ;;
  *) echo "unknown flag: $1" >&2; exit 1 ;;
esac; shift; done
```

---

## §2 每 tick 逻辑（顶层控制流）

每次 `/pr-monitor` 调用按序执行，做完即返回：

1. **读 PR 状态（一次 gh，兼存在性校验）**：
   ```bash
   STATE=$(gh pr view "$PR" --json state,labels --jq '{state:.state, labels:[.labels[].name]}') \
     || { echo "error: PR #$PR not found / gh auth failed"; exit 1; }
   ```
2. **§3.1 终止检查**（优先；命中即打印结束语并返回）。
3. 按 `$ROLE` 分支：`--role=review` → §4；否则执行自动接力（§3.2-3.5）。
4. 返回；单次调用到此结束。

> **无 cursor / 无时间戳追增量**：「有无待修 findings」由 **label + 最新机器块**判定——`pr-status/needs-fix` 在即「review 给了结论待修」，幂等可重报。

---

## §3 fix 侧逻辑

### §3.1 终止条件（命中即结束）

| 条件 | 判定 | 窗口输出 |
|------|------|---------|
| `pr-status/ready` ∈ labels | label 含 | "PR #N 已 ready，监控结束" |
| PR state != OPEN | `state != "OPEN"` | "PR #N 已关闭（state=$STATE），监控结束" |
| §3.2 熔断触发 | block `cycle.exhausted` / round≥3 | 见 §3.2 |

> ready/closed/熔断 是终止出口。ship/fix 经延迟单次调用本技能、跑完即止。

### §3.2 熔断判定（任一成立 → 不 dispatch）

```bash
BLOCK=$(bash hack/automation/pr-meta.sh extract "$PR" 2>/dev/null); EC=$?
# EC=0 有效 fresh block → 读 findings.byCx / cycle.exhausted / next.agent
# EC=2 无 block / EC=3 stale → 用 round 兜底
ROUND=$(bash hack/automation/pr-meta.sh round "$PR" 2>/dev/null || echo 0)
```

熔断条件：block `cycle.exhausted == true`，或 block `next.agent == "human"`，或 `ROUND >= 3`（maxRounds）。
命中 → 窗口打印 "PR #N 熔断：review↔fix 已达 3 轮上限，转人工处理（`gh pr view <N> --web`）"，返回。

### §3.3 Claude 自动 /fix 触发（dispatch 门 = 机器可判定条件）

pr-monitor 只凭**机器可判定**的事实（label + 最新机器块）决定是否 dispatch `/fix`；**文件级 / 禁止域安全裁决在 dispatch 之后由 `/fix` 自己的 [AUTO-FIX] 门把关**（fix §3.4——它能读 `git diff --name-only` + 逐个 finding 文件，pr-monitor 读不到）。

| dispatch 门（全部机器可判定，全部成立才 dispatch） | 判定方法 |
|------|---------|
| `pr-status/needs-fix` ∈ labels | label check |
| 未熔断 | §3.2 通过 |
| fresh canonical review 块 | `extract` 成功且 latest block `kind == "pr-review"` |
| review 结论一致 | block `verdict == "changes-requested"` |
| 下一跳一致 | block `next.agent == "claude"` 且 `next.command == "/fix"` |
| 触发 label 一致 | block `next.triggerLabel == "pr-status/needs-fix"` 且该 label 仍在 PR 上 |
| 同 head 约束 | block `next.requiresSameHeadSha == true`（`extract` 已比对 live headSha，stale 会失败） |
| **Cx1/Cx2 window** | block `findings.byCx`：cx3 == 0 ∧ cx4 == 0 ∧ (cx1 + cx2) > 0 |

> **为什么 dispatch 门不查 IN_SCOPE / ≤2 文件 / 禁止域**：这些是**文件级**事实，机器块只有 `findings.byCx` 聚合计数（无文件清单），pr-monitor 读不到——把读不到的事实写进门只会是**不可执行的门禁**。它们改由 dispatch 后的执行体自限：Claude `Skill("fix")` 侧靠 fix §3.4 [AUTO-FIX]（`IN_SCOPE + ≤2 文件 + 不改 kernel 接口/migration/bootstrap/并发语义`，越界 surface + 转人工）。**端到端「能否自动改」= 此处 Cx1/Cx2 机器门 ∧ 执行体侧文件级 instruction-level 自限**（后者非机器强制门，越界靠 fix skill 自觉转人工），缺一不放行。

**dispatch 门全部成立** → host LLM in-session 调用：

```
Skill("fix", args="<N>")
```

> `Skill("fix")` 是 pr-monitor 单次调用内的自动操作；经 dispatch 门（needs-fix / 未熔断 / Cx1/Cx2 window）+ fix 侧文件级 instruction-level 自限双重收窄 + 3 轮 review↔fix 熔断（Hard 机器读）兜底。

fix 会贴 pm:fix + 切 `pr-status/needs-check-fix`；pr-monitor 本次单次调用到此结束。后续 `/pr-review --check` 由外部 app 监听触发，再由 fix 收尾延迟约 10 分钟启动下一次 pr-monitor 接力。

### §3.4 不自动修的情况（只报告，不 AskUserQuestion）

- **`pr-status/needs-review-again`**：窗口打印 "PR #N 待外部 app 执行首轮 review；如需手动兜底，运行 `/pr-monitor <N> --mode=auto --role=review`"。
- **`pr-status/needs-check-fix`**：窗口打印 "PR #N 待外部 app 执行 `/pr-review --check`；如需手动兜底，运行 `/pr-monitor <N> --mode=auto --role=review`"。
- **无 `pr-status/needs-fix`**：窗口打印 "PR #N 暂无待修 label，本次接力结束"。
- **Cx3+/kernel/migration/并发语义**：窗口打印 "PR #N 含 Cx3+ findings，不自动修（需人工决策，fix §3.1）"。
  **不打印 backlog 草稿**——OOS finding 的建 issue 已由 `/fix` 自动完成（pm:oos 自动建 issue + 回填 #N，见 fix 4.6 step 3）。

### §3.5 冲突解（复用 issues B5）

```bash
MERGEABLE=$(gh pr view "$PR" --json mergeable,mergeStateStatus --jq '.mergeable')
```

`UNKNOWN` → 轮询（≤5 次，间隔 10s）落定。`CONFLICTING` / `mergeStateStatus==DIRTY` → 在 PR 的**已有** dev worktree 内解（不新建）：

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

解完后返回；下一次接力调用回 §3.1 重检。

---

## §4 alternate review 能力（`--role=review`）

review 角色 in-session 按当前 `pr-status` 跑 review/check（Claude review 引擎）：

```bash
if [[ " ${LABELS[*]} " == *" pr-status/needs-check-fix "* ]]; then
  claude -p "/pr-review $PR --check"
else
  claude -p "/pr-review $PR"
fi
```

review 结果由 /pr-review 贴评论 + 切 label；fix 侧接力仍由后续 `/pr-monitor <PR#> --mode=auto` 完成。

---

## §5 沟通规则

**窗口打印是主输出**；pr-monitor 自身不贴 PR 评论（贴评论是 /fix 或 /pr-review 的职责）。

| 路径 | 允许的副作用 |
|------|------------|
| fix 侧自动接力 | §3.3 满足时 `Skill("fix")`；§3.5 冲突时 git merge + push |
| `--role=review` | §4 调 `claude -p "/pr-review"`；review 贴 pm:pr-review 评论 + 切 label 由 /pr-review 完成（非 pr-monitor 自身） |

**label 切换**：pr-monitor 不直接切 `pr-status/*`（由 /fix 或 /pr-review 完成）。

**不自动处理**的情况统一报告，不 AskUserQuestion。
