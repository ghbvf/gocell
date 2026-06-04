---
name: fix
description: "问题诊断与修复: 验证+根因+复杂度分级+修复方案+backlog登记。当用户说'这个问题存在吗''帮我分析这个bug''诊断一下这个模块''修复这个问题'时触发。输入优先 PR 号（自动读 PR 评论），也支持 issue 号 / 文件:行号 / 自然语言；多 findings 自动批量。"
argument-hint: "<#PR | #issue | 文件:行号 | 问题描述>"
allowed-tools: [Read, Write, Edit, Glob, Grep, Bash, Agent, AskUserQuestion]
---

# 问题诊断与修复

> 真源 = GitHub Issues + Project v2 #3；label / 评级 rubric（P + Cx）见 `.github/project-template/PROJECT.md`；issue/PR/label/评论原子操作规范见 `issues`（Part B）。

---

## 输入解析
优先级：**PR 号**（裸数字先按 PR 试 → `gh pr view <N> --json reviews,comments` 读评论提 findings（每条带 body/id/url/createdAt，对话评论已够——本仓 codex 不发 inline 评论）；**只取最新一轮**——按 createdAt 倒序，跳过自己贴的 `pm:ship`/`pm:fix`/`pm:pr-review` 留痕评论，只取最近一批 codex review/comment，**不回头处理上一轮已 triage 的 findings**）> **issue 号**（`gh issue view`，404 停）> **文件:行号** > **自然语言**（Grep/Glob）。

---

## 阶段 1: 问题定位

### 1.1 找到问题代码

按精度递进：明确路径 → Read；模糊描述 → Grep 类型/方法签名 → Grep 错误码/注释 → Agent(Explore) 调用图。三层均无果 → AskUserQuestion。

### 1.2 追踪调用链 + 数据流

从问题代码向上（调用方）和向下（被调用方）追踪。跨 3+ 包用 Agent(Explore)。
同时追踪数据流：数据源 → 变换 → 消费者。

### 1.3 确认问题是否存在

| 状态 | 含义 | 下一步 |
|------|------|--------|
| **CONFIRMED** | 问题真实存在，可以复现 | → 进入阶段 2 |
| **RESOLVED** | 问题已被修复（给出证据：哪行代码、哪个 PR） | → 向用户报告，结束 |
| **CHANGED** | 代码重构过，问题形态变化 | → 向用户描述新形态，确认是否继续 |
| **CANNOT_VERIFY** | 无法确认（缺少上下文、需要运行时验证） | → AskUserQuestion 请求更多信息 |

输出含：状态 / 位置 / 调用链 / 数据流 / 问题描述（自己总结，不照搬 backlog）。

### 1.4 复现测试（Reproduction Test First）

CONFIRMED 后、修复前，先构造一个能**复现问题**的测试用例：

1. 基于调用链和数据流分析，编写最小测试用例触发问题
2. 运行测试确认 FAIL（证明问题可复现）
3. 将此测试作为修复验收标准

| 场景 | 操作 |
|------|------|
| 已有测试可稍加修改复现 | 修改已有测试 + 确认 FAIL |
| 需新写测试 | 在 `_test.go` 新增 `TestXxx_Bug描述` |
| 并发问题 | 写 `go test -race` 可触发的竞态测试 |
| 无法在单测中复现（需运行时状态） | 标注 `RUNTIME_ONLY`，跳过此步 |

---

## 阶段 2: 根因分析 + 复杂度分级（CONFIRMED 后执行）

### 2.1 根因三维度

- **代码层面**: 哪行代码、哪个设计决策导致的
- **架构层面**: 是否系统性（Grep 同模式，1 处=局部，3+=架构缺陷）。架构缺陷 → AskUserQuestion 确认局部修还是系统性重构
- **历史层面**: git log 搜索同类已有修复，发现团队惯例，避免退化

### 2.2 影响范围

直接影响 / 间接影响（列出受影响文件）/ 同类问题（Grep 相同模式数量）

### 2.3 复杂度分级

**必须对问题做复杂度判定**，决定后续方案形态（方案数见 3.1）。**Cx1-4 定义单源在 `.github/project-template/PROJECT.md` §3.2**（= 改动量/实现风险，本技能不复制）。

> Cx1 表示"容易修"，不代表"不重要"。IN_SCOPE/OUT_OF_SCOPE 由文件归属（阶段 2.4）决定，与 Cx 等级无关——Cx1 也可以是 IN_SCOPE 且必须修。

判定依据（按顺序检查）：
1. 修复涉及多少个文件？（`Grep` 搜索所有受影响的调用点）
2. 是否需要修改 `kernel/` 层的接口或类型？
3. 是否需要修改数据库 schema（migration）？
4. 是否影响 wire/bootstrap 层的组装逻辑？
5. 同类问题在其他模块是否重复出现？（1 处=局部，3+=系统性）

### 2.4 当前分支归属判定

判断问题是否属于**当前分支/PR 的修复范围**。默认在当前分支处理。

**判定方法：**
1. `git diff --name-only origin/develop...HEAD` — 获取当前分支改动的文件列表
2. 对比 finding 涉及的文件是否在这个列表中
3. 如果当前分支有关联 PR（`gh pr view --json title,body`），检查 PR 描述是否包含该 finding 的 ID 或关键词

**判定结果：**

| 结果 | 判定条件（按文件归属快速判） | 下一步 |
|------|---------|--------|
| **IN_SCOPE** | finding 文件在当前分支 diff 中，或 PR 描述含该 finding ID | 在当前分支修复 |
| **RELATED** | 不在 diff 中但同包 / 同子系统遗留 | 建议搭车修，标注"搭车" |
| **OUT_OF_SCOPE** | 完全不同的包 / 模块 | 不在当前分支修；按 §沟通规则闸门输出 issue 建议命令 |

输出含：代码/架构/历史三维度根因、复杂度、当前分支归属（含理由）、影响范围（直接/间接/同类）、历史修复。

---

## 阶段 3: 修复方案设计

### 方案设计原则（贯穿 3.0-3.4；进入阶段 4 / 输出 Cx3+ 方案 / 提交批量汇总前强制自检）

- **彻底**：根因级修复，不留 TODO/FIXME/follow-up；阶段 2.2 列出的"同类"问题一并纳入。自检"是否还藏 TODO、兼容代码、未列入的同类？"
- **不向后兼容**：直接改签名/删字段/换实现，不留 deprecation 别名、shim、双路径。自检"是否留了别名、旧字段、双路径？"
- **优雅简洁**：最少代码、最少抽象、最少新文件，不预设未来需求。自检"能否用更少代码、抽象、新文件达成？"

不通过 → 修订；必须保留的违反项 → 显式列入"遗留 / 取舍说明"，不得默默放行。默认走彻底方案；Cx2 最小修复仅在 3.2 明确"不能现在做"时启用，必须给升级窗口 + 按 §沟通规则闸门输出 issue 建议命令。批量时"搭车修"同样适用。

### 3.0 对标参考查询（Cx2+ 必须执行）

Cx2 及以上问题，**先查参考实现再动手**。三层按权威性递减：

1. **Go 标准库** → 有做法必须遵循，不自创。查 `docs/references/framework-comparison.md` "Go 标准库参考" 表
2. **组件官方库** → 遵循推荐模式 + 检查 Issues 已知陷阱。查同文件 "组件官方库参考" 表
3. **对标框架** → 参考，可偏离但须注明理由。查同文件 "按 GoCell 模块的参考映射"

**决策优先级**: 层 1 > 层 2 > 层 3 > `WebSearch "golang best practice"`

**何时跳过**: Cx1 全跳过；纯业务 bug 全跳过
**不可跳过**（即使 Cx2）: 并发/锁、连接池/生命周期、重连/重试/超时、密码学/认证、事件发布/消费

---

### 3.1 方案分级

| 等级 | 方案数 | 形态 |
|------|--------|------|
| Cx1 | 1 | 直接修，跳过比较 |
| Cx2 | 2 | A 最小修复 + B 彻底方案 |
| Cx3 | 3 | A 最小 + B 彻底 + C 重构 |
| Cx4 | 设计文档 | 只输出方案，不执行 |

每个方案须含：改动范围、原理、优缺点、遗留（仅最小修复）、预估改动量、参考来源（Cx2+ 必填）。

### 3.2 时机判断：现在做还是后面做

**必须给出明确的时机建议**，回答三个问题：

**Q0: 是否属于当前分支？** 取 2.4 归属结果：**OUT_OF_SCOPE** → 按 §沟通规则闸门输出建议命令，跳过 Q1-Q3；**IN_SCOPE / RELATED** → 进 Q1（RELATED 改动量大可 defer）。

**Q1: 推荐现在做还是后面做？**（仅 IN_SCOPE / RELATED 继续）

| 推荐 | 判定条件 |
|------|---------|
| **现在做** | 安全漏洞 / 运行时崩溃 / 阻塞其他工作 / 改动量 ≤ 50 行 |
| **本迭代做** | 有明确影响但不紧急 / 改动量 50-200 行 / 不阻塞他人 |
| **下迭代做** | 设计级问题 / 改动量 200+ 行 / 需要先完成其他前置工作 |
| **记录不做** | 理论风险但实际不触发 / 修复代价远大于收益 |

**Q2: 能不能现在做？** 检查：已有 issue 依赖、活跃分支冲突、kernel 接口消费方。

**Q3: 最小修复的有效期？** 给出彻底方案的建议时间窗口。

### 3.3 详细修复计划

文件级改动清单 + 验证命令（`go build` / `go test` / `go test -race`）。

### 3.4 执行决策（自动，不逐条问用户）

| 复杂度                                                      | 条件 | 决策 |
|----------------------------------------------------------|------|-----|
| Cx1 + IN_SCOPE + ≤2文件 + 不改 kernel 接口/migration/bootstrap | 全满足 | **[AUTO-FIX]** 直接修 |
| Cx2 + IN_SCOPE + 能做                                      | — | 执行推荐方案 |
| Cx2 + 不能做（有前置依赖）                                         | — | 记录报告，标注阻塞 |
| Cx3/Cx4                                                  | 任何 | 只输出方案，标注"需人工决策" |
| 任何 + OUT_OF_SCOPE                                        | — | 不修，按 §沟通规则闸门输出建议命令 |

**不可自动执行**: 并发语义变更、接口签名修改、新依赖、数据流方向变更、Cx2+。

**何时用 AskUserQuestion**: 见文末 §沟通规则（默认自动决策，不逐条问）。

### 3.5 执行前任务清单（阶段 3 → 4 门禁）

**用 TaskCreate 注册每项任务**，执行时 TaskUpdate 更新状态（✔/◼/◻）。

规则：
- 所有 finding 都创建 task，OUT_OF_SCOPE 标注 `[→ 输出 issue 建议命令；等用户确认]`
- 单条 Cx1 IN_SCOPE → 跳过清单直接修；批量或 Cx2+ → 必须创建
- 最后两项固定：`commit + push` + `闭合/创建 GitHub issues`
- 创建后立即执行，不等确认

---

## 阶段 4: 执行修复

### 4.1 Commit 格式

在当前分支直接修改。Commit: `fix(<scope>): <问题简述>` + 根因 + 复杂度 + Refs + Co-Authored-By。
scope 按层：kernel/runtime/cells/pkg。安全约束：只 add 修复文件（不 add -A）；不 amend。

### 4.2 执行代码修改（逐编辑测试循环）

> **批量并行**：4+ 条 finding 时按 Cell 包（cells/kernel/runtime/adapters/pkg）聚类派发 `developer` sub-agent（**同包同 agent** 防写冲突，组内串行执行下面循环）；并发 4-9→2 / ≥10→3；≤3 条由主 agent 直接处理。triage 同理可按聚类并行（`Explore`）。

对每个任务，执行 Edit-Test Loop + 状态更新：

1. **TaskUpdate → in_progress**（开始处理当前任务）
2. Read 目标文件
3. Edit / Write 修改代码
4. `go build ./...` — 编译检查
5. `go test ./修改的包/...` — **立即运行测试**（含阶段 1.4 的复现测试）
6. 如果测试失败：
   - 分析失败原因
   - 如果是当前编辑引入 → 立即修正，重回步骤 3
   - 如果是暴露了后续步骤的依赖 → 记录，继续下一步骤
7. 测试通过 → **TaskUpdate → completed** → 进入下一个任务

### 4.3 最终测试

全部修改完成后，运行完整测试：

```bash
go build ./...
go test ./path/to/modified/package/...
go test -race ./path/to/modified/package/...  # 涉及并发时
go test ./kernel/...                            # 改了 kernel 时
```

### 4.4 测试失败处理（分层回退）

| Round | 策略 |
|-------|------|
| 1-2 | 在当前方案上迭代修正 |
| 3 | `git stash` + 切换到备选方案重新执行 |
| 4 | 回滚（`git checkout -- <文件>`），Cx1 标 ESCALATE，Cx2 降级到最小修复标遗留 |

### 4.5 验证修复

重新执行阶段 1 的定位逻辑，确认：
- 原问题代码已被替换
- 数据流已正确保护
- 测试覆盖了问题场景

### 4.6 Git 收尾（测试通过后自动执行）

分三步：先提交分支代码，再等 CI 绿，再操作 GitHub。issue 写入不产生 git diff，无需"不 commit"概念。

**步骤 1: 提交当前分支代码**
1. `git add` 修复涉及的代码文件
2. 按 4.1 commit → push；**PR 已存在（输入是 PR 号 / 分支已有 PR）则不重建**，仅当前分支尚无 PR 时才 `gh pr create`

**步骤 2: 冲突预检 + CI 绿 gate（有 push 时；命令 + 时长见 `issues` B5）**

push 后先验无文件冲突、再等 CI 收敛再收尾（典型 ~5-6 min）：冲突 → 先 merge origin/develop 解冲突再 push；CI 失败则回阶段 1-4 修复循环（定位 → 修 → commit → push）再等，**最多 3 轮**；**3 轮仍红 → 直接贴 pm:fix 评论留痕（含 CI 失败摘要 + 已尝试轮次）+ 停下交人工，不 AskUserQuestion**。**CI 未绿不切 `pr-status/ready`、不贴"全清"评论**（保持当前 pr-status）。纯 issue 路径（无代码 push）跳过本步。

**步骤 3: GitHub 收尾（按输入类型，无 issue 查找；命令形态见 `issues` Part B）**

- **输入是 issue 号 + 已修** → 关闭该 issue（`gh issue close`，reason completed，comment 引用 `PR #<NNN>`；N 即输入，不查找）。
- **输入是 PR 号 + 修完** → 贴 fix 评论（命令 + **回显 comment URL/id** 见 `issues` B4；用 `.github/project-template/pr-comment.md` 的 `<!-- pm:fix -->` 模板：findings triage + 修复结果 + 遗留，**每条带 `file:line` + 证据/根因/建议/方案种子入 `<details>`（无损，供下次 fix / 人工 / 建 issue 读）**，含 footer）；**OUT_OF_SCOPE finding 也写满无损详表 + 建 issue 命令草稿**（形态见 pr-comment.md 的 F3 OOS 示范），不只计数；再 `gh pr edit` 切 `pr-status/ready`（全清）或 `pr-review/changes-requested`（仍有 Cx3/Cx4 或 OUT_OF_SCOPE 遗留）。
- **未修 / 待办 finding**（OUT_OF_SCOPE / `/fix` 派生）→ §沟通规则闸门输出 `gh issue create` 建议命令（确认后跑，留 open；label = `backlog` + `pri-pX` + `area-XX` + `type-XX`；body 按 `.github/project-template/backlog.md` 的字段映射**无损**填充——现状←证据+三维根因+影响 / 修复方向←三级方案种子 / Files←file:line 全集 / Source←`PR #<N> finding <Fk>`，**不得一句话带过**；条件延后型加 `flag-cond` + Trigger，派生注明 `Discovered via /fix #<original>`）。

Priority：review finding 用原 `[P0-P3]`；`/fix` 派生默认 `pri-p2`；`pri-p0` 仅 incident（线上故障/数据完整性/CVE），停下 AskUserQuestion 确认。建 issue 必须显式 `--label pri-pX`。完成后 **TaskUpdate → completed**。

---

## 阶段 5: 输出 + 验证

**窗口打印诊断 / 修复报告是主输出**；pm:fix 评论（4.6 已贴）是无损留痕，两者都要做（对齐 `pr-review` 的"窗口=主输出、评论=留痕"约定）。

- 诊断报告（未修）
- 修复报告（已修）
- 批量验证（审查报告）

**验证**（4.6 已执行，此处复核，不再查找）：issue-输入 → 核对该 issue 已 closed + comment 引用 PR（`gh issue view <N>`）；PR-输入 → 核对 fix 评论 + `pr-status` 已切；未修/派生 → create 建议命令已输出待确认。

---

## 沟通规则

**默认按分析结果自动决策。** 仅以下情况用 AskUserQuestion：
- 无法定位问题代码
- 测试失败且 4 轮回退后仍无法修正
- 修复过程中发现新问题超出原始 scope
- **任何 `gh issue create` 调用前**（OUT_OF_SCOPE finding / /fix 派生新问题）：先反思确认问题是否PR相关，Cx1问题搭车修，如真实OUT_OF_SCOPE先输出建议命令 + **无损 body 草稿**（按 backlog.md 字段映射，见 4.6 步骤3），再create issues。
- pri-p0 红线升级（incident-driven 或安全 CVE）
