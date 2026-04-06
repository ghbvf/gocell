---
name: review-fix-loop
description: "通用 Review-Fix 循环: 可参数化的审查范围/席位/轮次/修复模式"
argument-hint: "--scope <scope-expr> [--seats auto|all|1,2,5] [--rounds 1-3] [--fix-mode agent|manual] [--tech-debt on|off] [--output <dir>]"
allowed-tools: [Read, Write, Edit, Glob, Grep, Bash, Agent]
disable-model-invocation: true
---

# Review-Fix 循环

独立的审查-修复循环原语。复用 `.claude/agents/reviewer.md` 的 6 席位 Reviewer 体系，支持变更审查和区域审查两种模式。

**执行者**: 总负责人（或用户直接调用）

---

## 输入方式

支持**结构化参数**和**自然语言**两种输入。

### 结构化参数

| 参数 | 必需 | 默认 | 说明 |
|------|------|------|------|
| `--scope` | 是 | — | 审查范围表达式（见 Scope 类型） |
| `--seats` | 否 | `auto` | `all` 6 席位 / `auto` 按路径推断 / `1,2,5` 指定编号 |
| `--rounds` | 否 | `3` | 最大循环轮次（1-3） |
| `--fix-mode` | 否 | `agent` | `agent` 自动派发 fixer / `manual` 仅报告 |
| `--tech-debt` | 否 | `off` | `on` 时将 DEFERRED 的 P1/P2 产出到 tech-debt.md |
| `--output` | 否 | `.` | findings 输出目录（Phase 流程中用 `specs/{branch}/`） |

### 自然语言

如 `$ARGUMENTS` 不含 `--scope` 标志，按自然语言理解提取意图，映射到结构化参数。

**映射规则**:

1. 提到"暂存"/"staged" → `scope=staged`
2. 提到"工作区"/"未提交"/"working" → `scope=working`
3. 提到 PR 编号或 URL → `scope=pr:{url}`
4. 提到"最近 N 个 commit" → `scope=commits:HEAD~N..HEAD`
5. 提到具体路径或目录名 → `scope=module:{path}` 或 `scope=files:{path}/**`
6. 提到功能关键词但无具体路径 → `scope=feature:{keyword}`
7. 提到"分支"/"branch"但无上述 → `scope=branch`
8. 提到特定审查角度（安全/测试/架构等）→ 映射到对应席位编号
9. 提到"全面"/"全量"/"all" → `seats=all`
10. 未明确提到修复意图 → `fix-mode=manual`（自然语言场景默认仅报告）
11. 无法确定的参数 → 使用默认值

**示例**:

```
"帮我看看认证模块有没有安全问题"   → --scope module:runtime/auth --seats 2 --fix-mode manual
"review the last 3 commits"       → --scope commits:HEAD~3..HEAD --seats auto
"审查一下 PR 42"                  → --scope pr:{repo-url}/pull/42 --seats auto
"检查暂存区的变更"                → --scope staged --seats auto --rounds 1
"event-bus 相关代码全面审查"       → --scope feature:event-bus --seats all
"adapters 目录安全+运维审查"       → --scope module:adapters --seats 2,4
"看看最近改了什么，安全方面"       → --scope branch --seats 2
"cells/access-core 的测试够不够"   → --scope module:cells/access-core --seats 3
"这个分支和 main 对比一下"        → --scope branch:main --seats auto
"帮我全面审查一下当前分支"         → --scope branch --seats all
```

**确认机制**: 自然语言解析后，在执行前输出解析结果供用户确认：

```
解析结果:
  scope:    module:runtime/auth
  seats:    2 (安全/权限)
  rounds:   1
  fix-mode: manual
  output:   ./
确认执行？
```

S5/S6 等程序化调用使用结构化参数，不触发确认。

---

## Scope 类型

### 变更类 — 审查"改了什么"

| scope 表达式 | 说明 | 内部 diff 命令 |
|-------------|------|---------------|
| `pr:<url>` | 单个 PR | `gh pr diff {url}` |
| `branch` | 当前分支 vs develop | `git diff develop...HEAD` |
| `branch:<base>` | 当前分支 vs 指定 base | `git diff {base}...HEAD` |
| `diff:<base>..<head>` | 任意两点 | `git diff {base}..{head}` |
| `commits:<range>` | commit 范围 | `git diff {range}` |
| `staged` | 暂存区 | `git diff --staged` |
| `working` | 工作区未暂存 | `git diff` |

### 区域类 — 审查"代码现在的状态"

| scope 表达式 | 说明 | Reviewer 获取方式 |
|-------------|------|------------------|
| `files:<glob>` | 指定文件集合 | Glob 匹配后直接 Read |
| `module:<path>` | 模块目录 | 列出 `{path}/**/*.go` 文件后 Read |
| `feature:<keyword>` | 按关键词搜索 | Grep 搜索含 keyword 的 `.go` 文件后 Read |

**两类的 Reviewer 指令差异**:
- **变更类**: 关注回归风险、意图偏离、新引入的问题
- **区域类**: 关注代码质量、安全合规、架构一致性、技术债务（全量审视，不限于最近变更）

---

## 6 个命名 Reviewer 席位

| 编号 | 名称 | 审查焦点 |
|------|------|---------|
| S1 | 架构一致性 | DDD 分层、聚合边界、模块耦合 |
| S2 | 安全/权限 | 认证鉴权、数据暴露、攻击面 |
| S3 | 测试/回归 | 测试覆盖、回归风险、边界用例 |
| S4 | 运维/部署 | Docker/CI、migration 安全 |
| S5 | DX/可维护性 | 可读性、命名、复杂度 |
| S6 | 产品/用户体验 | 交互流程、错误提示、空状态 |

> 各席位的详细审查标准和阈值定义在 `.claude/agents/reviewer.md` 中（唯一真相源）。

---

## 操作步骤

### Step 0: 参数解析

1. 检测 `$ARGUMENTS` 是否含 `--scope` 标志
   - 是 → 按结构化参数解析
   - 否 → 按自然语言映射规则提取意图，生成结构化参数，输出解析结果等待确认
2. 填充未指定参数的默认值
3. 验证参数合法性（scope 格式、seats 编号 1-6、rounds 1-3）

### Step 1: 确定审查范围

**变更类**:

```bash
# 获取 diff 统计
{diff_cmd} --stat
```

```bash
# 获取变更文件列表（供 auto-seat 使用）
{diff_cmd} --name-only
```

其中 `diff_cmd` 按 scope 类型确定:

| scope | diff_cmd |
|-------|----------|
| `pr:{url}` | `gh pr diff {url}` |
| `branch` | `git diff develop...HEAD` |
| `branch:{base}` | `git diff {base}...HEAD` |
| `diff:{base}..{head}` | `git diff {base}..{head}` |
| `commits:{range}` | `git diff {range}` |
| `staged` | `git diff --staged` |
| `working` | `git diff` |

如 diff 为空（无变更），报告"无变更内容"并退出，状态 SUCCESS。

**区域类**:

```
scope=files:{glob}    → Glob 匹配获取文件列表
scope=module:{path}   → Glob 匹配 {path}/**/*.go
scope=feature:{keyword} → Grep 搜索含 keyword 的 .go 文件，取文件列表
```

设 `review_mode = "area"`。
如文件列表为空，报告"无匹配文件"并退出，状态 SUCCESS。

记录:
- 文件列表 → `file_list`
- 文件数量 → `file_count`
- 变更统计（变更类）→ `change_stat`

### Step 2: 席位选择

**`all`**: 激活 [S1, S2, S3, S4, S5, S6]

**`auto`**: 按文件路径匹配:

```
kernel/**                        → S1, S3
cells/**/handler*.go             → S1, S3, S6
cells/**                         → S1, S3, S5
runtime/auth/**                  → S2（强制）
runtime/**                       → S1, S2
adapters/**                      → S3, S4
contracts/**                     → S1, S6
cmd/**                           → S5
pkg/errcode/**                   → S5
examples/**                      → S5, S6
docker-compose*, Dockerfile*     → S4
.github/**                       → S4
*_test.go                        → S3
docs/**                          → S5
journeys/**, fixtures/**         → S3, S6
```

规则:
- 取所有匹配席位的并集
- ≥5 文件 → 强制加入 S1（跨切面集成风险）
- 变更类轻量 scope（`pr`/`staged`/`working`）→ 上限 3 席位，优先级: S2 > S1 > S3 > S4 > S5 > S6
- 区域类 → 无上限
- 并集为空 → 默认 S5

**`1,2,5`** 等显式指定: 直接解析为 [S1, S2, S5]

### Step 3: 循环执行（Round 1 .. max_rounds）

#### 3.1 派发 Reviewer Agent

对每个激活席位，派发一个 Agent（并行，`run_in_background=true`）。

**变更类 Reviewer prompt**:

```
角色: 席位 {N}: {seat_name} Reviewer
审查焦点: {focus from reviewer.md}
审查模式: 变更审查 — 关注回归风险、意图偏离、新引入问题。

启动后自行获取上下文:
1. 读取 specs/{branch}/kernel-constraints.md（如存在）
2. 运行以下命令获取变更内容:
   {diff_cmd}
3. 读取 specs/{branch}/spec.md（如存在）
4. 运行 git rev-parse HEAD 记录审查基准版本
5. 读取 specs/{branch}/product-context.md（席位 6 必读，如存在）

{IF round > 1: 聚焦范围: 只审查以下文件（上轮修复涉及的文件）: {fixed_files}}
{IF round == max_rounds: 只检查 P0。忽略 P1/P2。}

审查纪律: 直接审查代码变更和测试覆盖。不参考 Agent 对自身工作的描述。
不因"Agent 说它已测试"就跳过验证。自行确认事实。

产出格式:
每条 finding 必须包含:
- Finding ID: RF-{round}-S{seat}-{seq}（seq 从 01 起，两位数）
- 严重级别: P0(阻塞) / P1(重要) / P2(建议)
- 发现席位: {seat_name}
- 受影响文件: {文件路径:行号}
- 证据: {代码片段}
- 问题描述: {具体问题}
- 建议修复: {修复方案}
- 状态: OPEN

如无发现，回报: "席位 {N}: {seat_name} — 无发现"
```

**区域类 Reviewer prompt**:

```
角色: 席位 {N}: {seat_name} Reviewer
审查焦点: {focus from reviewer.md}
审查模式: 区域审查 — 全量审视代码质量、安全合规、架构一致性。

审查范围（逐一 Read 以下文件）:
{file_list}

启动后:
1. 逐一 Read 上述文件
2. 读取 specs/{branch}/kernel-constraints.md（如存在）
3. 运行 git rev-parse HEAD 记录审查基准版本

注意: 这不是 diff 审查。审查代码的当前状态，关注:
- 潜在的安全漏洞、架构退化、技术债务
- 是否违反 GoCell 分层约束（kernel/ 不依赖 runtime/adapters/cells/，cells/ 不依赖 adapters/，跨 Cell 走 contract）
- 代码质量、可维护性、测试覆盖

{IF round > 1: 聚焦范围: 只审查以下文件: {fixed_files}}
{IF round == max_rounds: 只检查 P0。忽略 P1/P2。}

审查纪律: 直接审查代码。不参考任何 Agent 自述。

产出格式:（同变更类）
```

**阻塞**: 等待全部 Reviewer 返回。逐一确认每个席位产出了 findings 或明确"无发现"。席位超时 → 重新派发该席位（1 次重试）。二次超时 → 标记该席位 N/A，继续。

#### 3.2 收集 findings

解析每个 Reviewer 的输出，按严重级别分组（P0 → P1 → P2），写入 `{output}/review-findings.md`。

- Round 1: 创建文件，写入 Metadata + Summary + Round 1 findings
- Round 2+: 追加新 Round section + Status Updates 表（记录上轮 findings 的状态变化）
- 每轮结束更新 Summary 表的统计数字

#### 3.3 裁决

- 无 P0 且无 P1 → 跳到 Step 4，退出状态 **SUCCESS**
- `fix-mode=manual` → 跳到 Step 4，退出状态 **MANUAL_PENDING**
- `fix-mode=agent` 且有 P0/P1 → 继续 3.4

裁决哪些修、哪些延迟:
- P0: **必须修复**
- P1: Round 1 可选修复，Round 2+ 延迟为 DEFERRED
- P2: 始终 DEFERRED 或 DISMISSED

#### 3.4 派发 Fixer Agent

将需要修复的 findings 按文件邻近度分组，派发 Fixer Agent（`run_in_background=true`）:

```
任务: 修复以下 Review Findings:

{每条 finding 的 ID、文件路径、问题描述、修复建议}

修复约束:
- 只修改与 findings 相关的文件
- 遵循 GoCell 编码规范（errcode 包、slog 日志、snake_case DB / camelCase JSON）

修复后逐步执行:

go build ./...

go test ./... -count=1

git add {affected files}

git commit -m "fix(review): address RF-{ids}"

回报格式:
- 每条 RF-{id} → FIXED / FAILED + 原因
- 修改的文件列表
```

等待 Fixer 返回。更新 review-findings.md 中对应 findings 的状态:
- FIXED: Fixer 报告修复成功
- FAILED: Fixer 报告无法修复（附原因）

#### 3.5 验证

```bash
go build ./...
```

```bash
go test ./... -count=1
```

构建或测试失败 → 视为回归，Fixer 需修复后重新提交。

#### 3.6 轮次判定

- 所有 P0 均 FIXED 或 DISMISSED → 跳到 Step 4，退出状态 **SUCCESS**
- 当前 round < max_rounds → 下一轮（Round 2 审查修复区域 + 回归风险，Round 3 仅 P0）
- 当前 round == max_rounds 且仍有 P0 OPEN/FAILED → 跳到 Step 4，退出状态 **ESCALATE**

### Step 4: tech-debt 产出

**仅 `--tech-debt on` 时执行。**

将所有 DEFERRED 状态的 P1/P2 findings 写入 `{output}/tech-debt.md`:

```markdown
# Tech Debt — {scope_label}

## Review Metadata
- **Baseline commit**: {hash}
- **Date**: {date}
- **Source**: review-fix-loop

## Classification
- [TECH]: Technical debt（代码质量、架构退化、测试缺失）
- [PRODUCT]: Product debt（降级体验、缺失功能、临时方案）

## Deferred Items

### TD-01 [TECH]: {标题}
- **Severity**: P1
- **Source**: RF-{id}
- **Affected files**: {paths}
- **Description**: {问题描述}
- **Status**: OPEN
- **Target**: {建议修复时机}

## Statistics
- [TECH] 新增: {N} 条
- [PRODUCT] 新增: {N} 条
```

每条 DEFERRED finding 按内容判断标记 `[TECH]` 或 `[PRODUCT]`:
- 代码质量、架构、测试 → `[TECH]`
- 用户体验、交互、产品功能 → `[PRODUCT]`

### Step 5: 最终产出 + 返回

1. 更新 `{output}/review-findings.md` 的 Summary 表和 Final Disposition 表
2. 输出结果摘要:

```
Review-Fix Loop 完成
━━━━━━━━━━━━━━━━━━━━
状态:     {SUCCESS / ESCALATE / MANUAL_PENDING}
轮次:     {completed_rounds}/{max_rounds}
Findings: P0={n} P1={n} P2={n}
P0 遗留:  {n}
Fixed:    {n}
Deferred: {n}
产出文件:
  - {output}/review-findings.md
  - {output}/tech-debt.md（如启用）
```

---

## review-findings.md 标准格式

```markdown
# Review Findings — {scope_label}

## Review Metadata
- **Baseline commit**: {hash}
- **Review date**: {date}
- **Review mode**: 变更审查 / 区域审查
- **Scope**: {scope expression}
- **Active seats**: {seat names}
- **Changes**: {N} files, +{ins} -{del}（变更类）/ {N} files reviewed（区域类）

---

## Summary

| Severity | Count | Open | Fixed | Deferred | Dismissed |
|----------|-------|------|-------|----------|-----------|
| P0 | {n} | {n} | {n} | {n} | {n} |
| P1 | {n} | {n} | {n} | {n} | {n} |
| P2 | {n} | {n} | {n} | {n} | {n} |

---

## Round 1 — 全量审查

### RF-1-S{seat}-01 [P0] {标题}
- **Seat**: 席位 {N}: {seat_name}
- **Affected file**: `path/to/file.go:42`
- **Evidence**:
  ```go
  // code snippet
  ```
- **Problem**: {描述}
- **Fix**: {修复建议}
- **Status**: OPEN

### RF-1-S{seat}-02 [P1] {标题}
...

---

## Round 2 — 聚焦审查

> Scope: 上轮修复涉及的文件 + 回归风险区域

### Status Updates

| Finding ID | Old Status | New Status | Note |
|------------|------------|------------|------|
| RF-1-S2-01 | OPEN | FIXED | commit {hash} |
| RF-1-S3-01 | OPEN | DEFERRED | moved to tech-debt |

### RF-2-S{seat}-01 [P0] {新发现}
...

---

## Round 3 — P0 only
...

---

## Final Disposition

| Finding ID | Severity | Title | Final Status |
|------------|----------|-------|--------------|
| RF-1-S2-01 | P0 | ... | FIXED |
| RF-1-S3-01 | P1 | ... | DEFERRED |
| RF-1-S5-01 | P2 | ... | DISMISSED |
```

### Finding ID 规范

格式: `RF-{round}-S{seat}-{seq}`
- `RF` = Review Finding
- `round` = 1/2/3
- `seat` = S1-S6（对应 6 个命名席位）
- `seq` = 两位数序号（01 起）

示例: `RF-1-S2-01`（Round 1，安全/权限席位，第 1 条）

### Finding Status 枚举

| 状态 | 含义 | 设置者 |
|------|------|--------|
| `OPEN` | 新发现，未处理 | Reviewer（初始） |
| `FIXED` | 已验证修复 | Reviewer（re-review）或 Fixer 报告 |
| `DEFERRED` | 移入 tech-debt | 裁决者 |
| `DISMISSED` | 误判或超范围 | 裁决者 |

---

## 退出状态

| 状态 | 含义 | 典型后续 |
|------|------|---------|
| `SUCCESS` | 无 P0 遗留 | S5: merge PR；S6: 进入 gate；standalone: 完成 |
| `ESCALATE` | max_rounds 耗尽仍有 P0 | S5: 拒绝 PR + 裁决；S6: 架构师裁决；standalone: 报告 |
| `MANUAL_PENDING` | fix-mode=manual，findings 已输出 | 用户手动修复后可再次调用 |
