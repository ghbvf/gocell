---
name: issues
description: "GitHub Issues + Project v2 #3 项目管理单源技能。Part A：epic 拆解 + wave 实施顺序调度（找子任务 → blocked-by DAG → wave 1-4 滚动排序：OPEN 重排、已完成不动、超窗不入字段 → 写 Project Wave 字段 + 回填 epic body + 回评）。Part B：issue/PR 原子操作（建/改 backlog issue、area/type/pri label、PR 双轴状态 label 流转、统一 PR 评论格式 + 冲突预检/CI watch 跟进，ship/fix 共用）。非 epic issue 号 → 查代码判状态（只判不修，建议 /ship 或 close）。当用户要整理 epic 排 wave、建/改 backlog issue、贴 label、切 PR 状态、给 PR 留评论、核一个 issue 是否还成立时使用。"
argument-hint: "<epic #N | #issue（非epic→状态核查）| create-issue | edit-labels | pr-status | comment> [...]"
allowed-tools: [Read, Grep, Bash, Agent, AskUserQuestion]
---

# issues — 项目管理单源（Epic/Wave + Issue/PR/Label/评论）

> 真源 = GitHub Issues + Project v2 #3。**内容/结构 + 治理全在 `.github/project-template/`**：issue body → `backlog.md`/`epic.md`，PR body → `pull_request_template.md`，PR 评论 → `pr-comment.md`，label/字段/评级/流程 → `PROJECT.md`（索引见 `README.md`）。本技能只负责编排，不复制模版内容。
> 输入分派：**`epic #N` / 带 `epic` label 的 issue** → Part A（拆解 + wave 调度）；**普通 issue 号（无 `epic` label）** → 下方「非 epic issue 状态核查」；**动词**（create / edit / pr-status / comment）→ Part B 原子操作。
> create issue 前先 search 查重（幂等）。
> 仓库：`ghbvf/gocell`。Project v2：`--owner ghbvf --number 3`（title `gocell`）。

---

## 非 epic issue 状态核查（查代码判状态，只判不修）

输入普通 issue 号（无 `epic` label）时，不排 wave，而是查代码判断该 issue 是否仍成立：

1. `gh issue view <N> --json title,body,labels` 读问题描述 + body 的 Files。
2. 按 Files / 关键字 Read/Grep 定位代码；跨 3+ 文件时并行派 `Agent(Explore)` 核查。
3. 判状态（**只判不修**）：**存在** / **已修复**（给证据：哪行 / 哪 PR）/ **已变更**（形态变化）/ **无法确认**。
4. 输出状态 + 证据 + 建议：需修 → 建议 `/ship #<N>`（或定位到 file:line 后 `/fix`）；已修复 / 过期 → 建议 Part B 关闭（`gh issue close --reason ...`）。

---

# Part A — Epic 拆解 + Wave 实施顺序

> 负责 epic 级「找子任务 → 排 wave → 写 Project Wave 字段 + epic body 实施顺序 + 回评」。issue/label/评论原子操作见 Part B。

## A1. 读 epic → 关键字查找相关 issues → 关联 → 汇总子 issues

1. **读 epic**：`gh issue view <epic#> --json number,title,body,labels`（确认 epic label；从 title + 目标/范围提取关键字）。
2. **关键字查找相关 issues**：`gh issue list --search "<关键字>" --state open --json number,title,labels`，挑出属于本 epic 的候选；用 AskUserQuestion 确认候选集（不擅自全关联）。
3. **关联到 epic**（建 sub-issue 关系，已是 sub-issue 的跳过）：
   ```bash
   id=$(gh api repos/ghbvf/gocell/issues/<child#> --jq .id)   # 取整数 id（非 number）
   gh api --method POST repos/ghbvf/gocell/issues/<epic#>/sub_issues -F sub_issue_id=$id
   ```
4. **汇总子 issues**：`gh api repos/ghbvf/gocell/issues/<epic#>/sub_issues --jq '.[]|{number,title,state}'`（含新关联）；对每个 OPEN 子任务读 label（area/type/pri）+ body 的 `Blocked-by: #NNN`（多行/逗号分隔，无声明=无前置）。

> 子任务跨 3+ 包或描述模糊时，用 `Agent(Explore)` 核实归属 / 状态 / `Blocked-by` 再汇总（**wave 内实施顺序的并行分析在 A2 第 6 步，见下**）。

## A2. 建 blocked-by DAG + wave 滚动排序（Wave 1-4 有界）

**滚动 + 有界算法**（每次更新 epic 都重跑；longest-path layering，作用域 = epic 的 **OPEN** 子任务）：

1. **节点 = OPEN 子任务**；CLOSED（已完成）子任务**排除**——不参与排序、不动其 Wave 字段（见 A3）。
2. 有向边 `blocker → dependent`（来自 `Blocked-by`），**仅当 blocker 也 OPEN**；blocker 已 close = 依赖已满足 → 删该边。**这是「滚动」的来源**：前置完成后 dependent 自动前移到更早 wave。
3. 检测环：若有环，AskUserQuestion 让用户裁定断哪条边（不静默）。
4. **wave 分层**（longest-path layering，`v` = OPEN 节点）：
   - `wave(v) = 1` 若 v 无 OPEN blocker；
   - `wave(v) = 1 + max(wave(b) for b in OPEN blockers(v))` 否则。
5. **有界 cap = Wave 4**：`wave(v) > 4` 的节点标记 **超窗**——不落 Project Wave 字段（A3 清空/不写）。Project Wave 只有 1-4 四档。
6. **wave 内排序 = 对每个 Wave 内任务并行分析，确认实施顺序**：跨 wave 顺序已由依赖锁定；wave 内无硬 `Blocked-by`，顺序是自由度。对每个 Wave（成员 ≥2）**并行**派 `Agent(Explore)` 分析其中每个任务的 scope / 触碰文件 / 产出↔消费 / 风险，主 agent 汇总后定该 wave 实施顺序：**基础性产出**（被同 wave 他者隐式消费）先 → **同文件任务相邻**防写冲突 → 默认 `pri`(p0>p1>p2>p3) → `Cx`(小先) → issue#。单任务 wave 跳过分析。
7. 输出每个 OPEN 子任务的 `(wave, wave 内序)` 或「超窗」。

呈现给用户的 dry-run 表（只列 OPEN；超窗与已完成单列）：

```
Wave 1: #aaa(P1/Cx1) #bbb(P2/Cx2)
Wave 2: #ccc(P1/Cx2, blocked-by #aaa)
Wave 3: #ddd(P2/Cx3)
Wave 4: #eee(P2/Cx2)
超窗(>W4，不写字段): #fff(依赖链 >4)
已完成(不动): #ggg #hhh
```

## A3. 写 Project v2 Wave 字段（单源真值，滚动）

> Wave 字段是 epic 子任务排序的**机器单源**；epic body 段是派生视图。**只写 OPEN 子任务的 Wave 1-4**；CLOSED 不动；超窗清空。写前确认字段 / option / item 合法。

```bash
# 发现 Wave 字段 id + option id（single-select；固定 Wave 1-4 四档）
gh project field-list 3 --owner ghbvf --format json \
  | python3 -c "import sys,json;[print(f['id'],f['name'],[ (o['id'],o['name']) for o in f.get('options',[])]) for f in json.load(sys.stdin)['fields'] if f['name']=='Wave']"

# 找子任务对应的 project item id（按 content issue 号）
gh project item-list 3 --owner ghbvf --format json \
  | python3 -c "import sys,json;[print(i['id'],i['content'].get('number')) for i in json.load(sys.stdin)['items'] if i.get('content')]"

# 写 Wave（仅 wave ∈ 1-4 的 OPEN 子任务）
gh project item-edit --project-id PVT_kwHOBjsrB84BYQ3m \
  --id <ITEM_ID> --field-id <WAVE_FIELD_ID> --single-select-option-id <WAVE_N_OPTION_ID>

# 超窗（wave>4）的 OPEN 子任务：清空 Wave 字段（防滚动后残留旧值）
gh project item-edit --project-id PVT_kwHOBjsrB84BYQ3m \
  --id <ITEM_ID> --field-id <WAVE_FIELD_ID> --clear
```

**滚动写入规则**：
- **OPEN 且 wave ∈ [1,4]** → 写对应 Wave option。
- **OPEN 且 wave > 4（超窗）** → `--clear`（清旧值），不占 Wave 1-4。
- **CLOSED（已完成）** → **完全不动**（不写、不清；保留其历史 Wave 值）。

**写入门（人工核对，非机器门）**：每条写入前确认 `WAVE_N_OPTION_ID`（N∈1-4）在 field-list 输出的 option 集中、
`ITEM_ID` 在 item-list（属本 project）。Wave 字段固定 4 档；缺档先在 Project UI 补齐 Wave 1-4 再写。子任务未入
project（无 item）时先 `gh project item-add 3 --owner ghbvf --url <issue-url>`。

## A4. 回填 epic body 实施顺序段 + 回评

```bash
# epic body 的「实施顺序」段重生成（派生视图：仅 OPEN 的 Wave 1-4 + 超窗/已完成注记）
gh issue edit <epic#> --body "$(...更新 ## 实施顺序 段...)"

# 回评通知（一行 + 标记）
gh issue comment <epic#> --body "$(cat <<'C'
<!-- pm:epic-wave -->
🌊 Wave 滚动更新：OPEN 重排 Wave 1-4，已完成不动，超窗(>W4)暂不入字段。Wave 字段 = 单源，详见 Project #3 / 本 issue body 实施顺序段。
C
)"
```

## A5. 沟通规则

- 环检测命中 / wave option 缺档 / 子任务未入 project：停下 AskUserQuestion。
- DAG 排序结果先 dry-run 呈现，确认后再写 Project（写 live 状态）。
- 不改子任务代码 / 不关 issue / 不改 area-type-pri label（那是 Part B / `fix` 职责）。

---

# Part B — Issue / PR / Label / 评论

> issue/PR 的 `gh` 编排，是 issue/PR/label/评论**固定 gh 命令形态的单源**——ship/fix/pr-review 引用本部分，不重印命令。body 骨架见 `.github/project-template/` 的 `backlog.md` / `epic.md` / `pull_request_template.md`；PR 评论格式见 `pr-comment.md`；label / 字段 / 评级 rubric 见 `PROJECT.md`。本部分不复制模版内容。

## B1. 新建 backlog issue

三维 label 齐全（area + type + pri）+ `backlog`，全部 CLI 显式贴（必须显式 `--label pri-pX`）：

```bash
gh issue create \
  --label backlog --label pri-p2 --label area-eventing --label type-bug \
  --title "[<ID>] <简短标题>" --body-file <填好的 backlog.md>
# body 骨架单源 = .github/project-template/backlog.md（现状 / 修复方向 / Files / Trigger / Source）——本技能不复制其结构
```

> **由 review/fix finding（OUT_OF_SCOPE / 派生）成文时**：body 按 `backlog.md` 顶部的字段映射**无损**填充，不得一句话带过——否则后续无法据此修复。

- **area-XX**（1 个，8 选）：见 `.github/project-template/PROJECT.md` §2.1。
- **type-XX**（1 个，8 选）：见 §2.2。
- **pri-pX**：评级 rubric 见 `.github/project-template/PROJECT.md` §3。`/fix` 派生默认 `pri-p2`；`pri-p0` 仅 incident-driven，停下 AskUserQuestion 确认。
- **flag-cond**（可选）：条件延后型加此 label + body 写 `## Trigger`。

> P0 红线不得默认贴。area/type 漏贴时用 `gh issue edit <N> --add-label area-X --add-label type-X` 补。

## B2. 编辑 label / 关闭 issue

```bash
gh issue edit <N> --add-label area-data --remove-label area-eventing      # 改领域
gh issue edit <N> --add-label type-debt                                   # 加类型
gh issue close <N> --reason completed --comment "Fixed in PR #<NNN>"      # 修复闭合
gh issue close <N> --reason "not planned" --comment "<理由>"              # wontfix
```

epic 用 `epic` label + GitHub 原生 sub-issue（不手写 body task list）。子任务关联用 issue 页 "Create sub-issue"
或 `gh`（sub-issue API）。wave 排序见 Part A。

## B3. PR 状态 label 流转（编排）

> 两正交轴（pr-status 流转 / pr-review 结论）的取值与「何时切」语义见 `.github/project-template/PROJECT.md` §2.5 + §5（单源，不在此复制表）。**两轴各自互斥**：pr-status 恰好一个；pr-review `approved` XOR `changes-requested`——**切一侧必 `--remove-label` 同轴对侧**。本节只给切换命令。

```bash
# ship 后：待再审（首次交接，唯一使用 needs-review-again 的地方）
gh pr edit <N> --add-label pr-status/needs-review-again --remove-label pr-status/in-progress
# review 轮结论（默认 /pr-review 或 codex；review 轴互斥）
# 有 finding → changes-requested + needs-fix（5-state：review 轮 changes-requested 始终切 needs-fix）
gh pr edit <N> --add-label pr-review/changes-requested --add-label pr-status/needs-fix --remove-label pr-review/approved --remove-label pr-status/needs-review-again
# 无 finding → approved + pr-status/ready（无需 fix/check 的终态）
gh pr edit <N> --add-label pr-review/approved --add-label pr-status/ready --remove-label pr-review/changes-requested --remove-label pr-status/needs-review-again
# fix 后：待 --check 验证（fix 不直接到 ready）
gh pr edit <N> --add-label pr-status/needs-check-fix --remove-label pr-status/needs-fix
# --check 全修复：可合并（清 pr-status 前态 + review 轴对侧）
gh pr edit <N> --add-label pr-status/ready --add-label pr-review/approved --remove-label pr-status/needs-check-fix --remove-label pr-review/changes-requested
# --check 有未修/回归：回 fix（清 pr-status 前态 + review 轴对侧）
gh pr edit <N> --add-label pr-review/changes-requested --add-label pr-status/needs-fix --remove-label pr-status/needs-check-fix --remove-label pr-review/approved
```

## B4. PR 评论（编排）

留痕约定 / 标记规则见 `.github/project-template/PROJECT.md` §5；评论格式（`pm:ship` / `pm:fix` / `pm:pr-review` 三模板 + footer）见 `.github/project-template/pr-comment.md`。本节是贴评论命令 + **回显 comment id** 的单源（ship/fix/pr-review 引用本节，不重印）：

```bash
URL=$(gh pr comment <N> --body-file <填好的 pr-comment.md 模板>)   # stdout = https://github.com/ghbvf/gocell/pull/<N>#issuecomment-<id>（实测返回，成功 exit 0）
echo "✅ 已贴评论：$URL"                                            # 必须回显给用户；comment id = URL 尾段 #issuecomment-<id>
```

- stdout 即评论 URL（含 `#issuecomment-<id>`）——**贴完必须捕获并回显**，便于用户跳转 / 引用该评论。
- 命令非 0 退出 → 报错退出，不静默跳过。
- footer 格式见 `.github/project-template/pr-comment.md`（PR#/工具/分支/worktree/session，AI 自填）。

## B5. PR 冲突预检 + CI 跟进（ship/fix 共用）

push 后流程分**两阶段**：① 冲突预检（阻塞，贴评论前必过）→ 立即收尾（贴评论 + 切 label，不等 CI）→ ② CI 异步收敛（收尾后再跑，结果贴独立 pm:ci 评论）。

**① 冲突预检**（阻塞，必须先于收尾）：`gh pr view <N> --json mergeable,mergeStateStatus`。`mergeable` 由 GitHub **异步计算**，刚 push 常返回 `UNKNOWN`——**轮询几次（~5-10s 间隔）直到落定** `MERGEABLE` / `CONFLICTING`，单查 UNKNOWN 无效。`CONFLICTING`（或 `mergeStateStatus=DIRTY`）→ 先解冲突：`git -C <wt> fetch origin && git -C <wt> merge origin/develop --no-edit`（解冲突 → commit → push）→ 回本步重检。`MERGEABLE` → 立即进行收尾（贴评论 + 切 label），**不等 CI**。

**② CI 异步收敛**（收尾评论 + label 切换之后再跑）：本仓 PR CI 五个 check 并行跑，**典型 ~5-6 min**（实测最慢 PR Check 中位 ~4.6 / 峰值 ~5.5 min；Governance ~4-5 min；Race / Static ~3 min；govulncheck ~40s）。

```bash
# 阻塞轮询直到所有 check 完成（exit 0=全绿 / 8=pending / 非0=有失败；--fail-fast 见首个失败即退）
gh pr checks <N> --watch --interval 30 --fail-fast       # Bash timeout 设 ~600000ms（10min 工具上限）；预计 6min 内返回
# 失败 → 列失败 check（精确到 PR head，bucket=fail）+ run 链接
gh pr checks <N> --json name,bucket,link --jq '.[] | select(.bucket=="fail") | [.name,.link] | @tsv'
gh run view <run-id> --job <job-id> --log-failed         # link=/actions/runs/<run-id>/job/<job-id>：中段 run-id、末段 job-id（裸 job-id 给 gh run view 会 404）
```

- **等待上限 ~12-15 min**（~2.5× 典型，吸收 runner 排队）。超 Bash 10min 上限用后台轮询兜底（`run_in_background` 跑 watch，或循环 `gh pr checks <N> --json bucket --jq 'any(.[]; .bucket=="pending")'` 间隔 30-60s）；超上限仍 pending → 停下报告，不无限等。
- 失败 → 回 `fix` 修复循环（定位 → 修 → commit → push → 重新预检），**最多 3 轮**；**3 轮仍红 → 停下交人工，不 AskUserQuestion**。
- CI 收敛后（全绿或 3 轮红）贴独立 `pm:ci` 评论（`verdict=ci-green` 或 `verdict=ci-failed`），不合并进主 pm:ship / pm:fix 评论。

## B6. 沟通规则

- label 编辑 / PR 状态切换 / 评论：按流程自动执行，不逐条问。
- CI watch / 取失败日志：自动执行；CI 修复 3 轮仍红 → 贴 PR 评论留痕 + 停下交人工（不 AskUserQuestion）。
