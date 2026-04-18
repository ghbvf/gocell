---
name: fix
description: "问题诊断与修复: 验证+根因+复杂度分级+修复方案+backlog登记。当用户说'这个问题存在吗''帮我分析这个bug''诊断一下这个模块''修复这个问题'时触发。支持单条问题和多方审查报告批量输入。"
argument-hint: "<问题描述|文件:行号|review报告路径>"
allowed-tools: [Read, Write, Edit, Glob, Grep, Bash, Agent, AskUserQuestion]
---

# 问题诊断与修复

接收一个问题描述（可以是 bug 报告、backlog 条目 ID、文件:行号、自然语言描述、
**或一份多方审查报告**），执行完整的诊断→根因→修复流程。

---

## 输入解析

用户输入可能是以下任一形式：

```
# 单一问题
/fix session refresh 有并发问题
/fix cells/access-core/slices/sessionrefresh/service.go:81
/fix P3-TD-10

# 多方审查报告（批量输入）
/fix docs/reviews/202604061401-pr39-six-role/findings.md
/fix --from-review stage-6  （读取最近一次 S6 review 产出）

# 示例
/fix "eventbus Close 和 Subscribe 有竞态"
/fix P3-TD-10
```

解析规则：
1. 如果包含 `文件路径:行号` → 直接定位到代码
2. 如果包含 backlog ID（如 `P3-TD-10`、`R1B1-01`）→ 从 `docs/backlog.md` 解析条目
3. 如果指向 review 文档（.md 文件含多条 findings）→ 进入**批量模式**
4. 如果是自然语言 → 用 Grep/Glob 在代码库中定位相关代码
### 批量模式（多方审查报告）

当输入是 review 文档时：
1. 解析文档中的所有 findings（按 ID、文件、描述提取）
2. 对每条 finding 执行阶段 1（验证是否存在）
3. **对每条 CONFIRMED finding 执行阶段 2.6（当前分支归属判定）**
4. 输出汇总表：状态 + 归属

   | 状态 | 归属 | 处理方式 |
   |------|------|---------|
   | CONFIRMED | IN_SCOPE | 在当前分支修 |
   | CONFIRMED | RELATED | 建议搭车修，标注"搭车" |
   | CONFIRMED | OUT_OF_SCOPE | **只记录到 backlog，不修** |
   | RESOLVED | — | 标注已修，跳过 |
   | CANNOT_VERIFY | — | 标注待确认，跳过 |

5. **只修 IN_SCOPE + RELATED 条目**，按分析结果决策：
   - IN_SCOPE + Cx1 + 满足自动执行条件 → 直接修
   - IN_SCOPE + Cx2 → 执行推荐方案（最小或彻底，由时机判断决定）
   - IN_SCOPE + Cx3/Cx4 → 只输出方案，标注"需人工决策"
   - RELATED + Cx1/Cx2 → 搭车修，标注"搭车"
   - OUT_OF_SCOPE → 登记 backlog，标注推荐归入的 batch
6. 按复杂度从低到高排序执行：先批量处理 Cx1，再逐条处理 Cx2，最后汇总 Cx3/Cx4 方案

---

## 阶段 1: 问题定位（必须完成）

### 1.0 Backlog 关联检查

修复开始前，先在 `docs/backlog.md` 中查找是否已有对应条目：

1. Grep 问题关键词 / backlog ID → 确认是否已登记
2. 已登记 → 读取条目，获取上下文（预估、依赖、状态）
3. 未登记 → 记录，修复完成后补登

### 1.1 找到问题代码

按精度递进：明确路径 → Read；模糊描述 → Grep 类型/方法签名 → Grep 错误码/注释 → Agent(Explore) 调用图。三层均无果 → AskUserQuestion。

### 1.2 追踪调用链 + 数据流

从问题代码向上（调用方）和向下（被调用方）追踪。跨 3+ 包用 Agent(Explore)。
同时追踪数据流：数据源 → 变换 → 消费者。

### 1.4 确认问题是否存在

| 状态 | 含义 | 下一步 |
|------|------|--------|
| **CONFIRMED** | 问题真实存在，可以复现 | → 进入阶段 2 |
| **RESOLVED** | 问题已被修复（给出证据：哪行代码、哪个 PR） | → 向用户报告，结束 |
| **CHANGED** | 代码重构过，问题形态变化 | → 向用户描述新形态，确认是否继续 |
| **CANNOT_VERIFY** | 无法确认（缺少上下文、需要运行时验证） | → AskUserQuestion 请求更多信息 |

**输出格式：**

```markdown
## 诊断结果

**状态**: CONFIRMED / RESOLVED / CHANGED / CANNOT_VERIFY
**位置**: `文件路径:行号范围`
**调用链**:
  HTTP POST /api/v1/xxx
    → handler.XXX() [handler.go:30]
      → service.XXX() [service.go:69]
        → repo.XXX() [repo.go:42]
**数据流**:
  request.refreshToken → session object → new token pair → DB update
**问题描述**: （用自己的话总结，不是照搬 backlog）
```

### 1.5 复现测试（Reproduction Test First）

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

**必须对问题做复杂度判定**，这决定了后续方案的形态：

| 等级 | 判定标准 | 方案形态 |
|------|---------|---------|
| **Cx1 简单** | 改 1-2 个文件，不跨包，不改接口 | 直接修 |
| **Cx2 中等** | 改 3-5 个文件，跨 1-2 个包，接口不变 | 最小修复 + 可选彻底方案 |
| **Cx3 复杂** | 改 5+ 个文件，跨 3+ 个包，或需改 kernel 接口 | 必须给出三级方案（最小/彻底/重构） |
| **Cx4 架构级** | 需要新增/重构子模块，或改变数据流方向 | 只做方案设计，不直接执行 |

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

| 结果 | 判定条件 | 下一步 |
|------|---------|--------|
| **IN_SCOPE** | finding 涉及的文件在当前分支 diff 中，或 PR 描述明确包含该 finding ID | 在当前分支修复 |
| **RELATED** | finding 涉及的文件不在 diff 中，但与当前分支的功能主题直接相关（如同一子系统的遗留问题） | 建议在当前分支一并修复，但标注为"搭车" |
| **OUT_OF_SCOPE** | finding 涉及完全不同的模块/子系统 | 记录到 backlog，不在当前分支修 |

**快速判定规则：**
- 当前分支 diff 包含 finding 文件 → IN_SCOPE
- 当前分支 diff 不包含但同包 → RELATED
- 完全不同的包 → OUT_OF_SCOPE

**输出格式：**

```markdown
## 根因分析

**代码层面**: ...
**架构层面**: 局部问题 / 架构缺陷（说明理由）
**历史层面**: ...
**复杂度**: Cx1 / Cx2 / Cx3 / Cx4
**当前分支归属**: IN_SCOPE / RELATED / OUT_OF_SCOPE（理由: ...）
**影响范围**:
  - 直接: ...
  - 间接: ... (列出受影响的文件)
  - 同类: ... (Grep 发现的相同模式，数量)
**历史修复**: (git log 发现的相关修复，或"无")
```

---

## 阶段 3: 修复方案设计

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

**Q0: 是否属于当前分支？**（阶段 2.6 判定结果优先）

| 归属 | 时机决策 |
|------|---------|
| **IN_SCOPE** | 在当前分支/PR 修，进入 Q1 判断优先级 |
| **RELATED** | 建议搭车修，但如果改动量大可 defer |
| **OUT_OF_SCOPE** | 记录到 backlog，**不在当前分支修**，跳过 Q1-Q3 |

**Q1: 推荐现在做还是后面做？**（仅 IN_SCOPE / RELATED 继续）

| 推荐 | 判定条件 |
|------|---------|
| **现在做** | 安全漏洞 / 运行时崩溃 / 阻塞其他工作 / 改动量 ≤ 50 行 |
| **本迭代做** | 有明确影响但不紧急 / 改动量 50-200 行 / 不阻塞他人 |
| **下迭代做** | 设计级问题 / 改动量 200+ 行 / 需要先完成其他前置工作 |
| **记录不做** | 理论风险但实际不触发 / 修复代价远大于收益 |

**Q2: 能不能现在做？** 检查：backlog 依赖、活跃分支冲突、kernel 接口消费方。

**Q3: 最小修复的有效期？** 给出彻底方案的建议时间窗口。

### 3.3 详细修复计划

文件级改动清单 + 验证命令（`go build` / `go test` / `go test -race`）。

### 3.4 执行决策（自动，不逐条问用户）

| 复杂度 | 条件 | 决策 |
|--------|------|------|
| Cx1 + IN_SCOPE + ≤2文件 + 不改 kernel 接口/migration/bootstrap | 全满足 | **[AUTO-FIX]** 直接修 |
| Cx1 + 不满足上述 | — | 记录报告，不修 |
| Cx2 + IN_SCOPE + 能做 | — | 执行推荐方案 |
| Cx2 + 不能做（有前置依赖） | — | 记录报告，标注阻塞 |
| Cx3/Cx4 | 任何 | 只输出方案，标注"需人工决策" |
| 任何 + OUT_OF_SCOPE | — | 记录 backlog，不修 |

**不可自动执行**: 并发语义变更、接口签名修改、新依赖、数据流方向变更、Cx2+。

**仅以下情况用 AskUserQuestion**: 测试失败且 4 轮回退无法修正；修复中发现新问题超出 scope。

### 3.5 执行前任务清单（阶段 3 → 4 门禁）

**用 TaskCreate 注册每项任务**，执行时 TaskUpdate 更新状态（✔/◼/◻）。

规则：
- 所有 finding 都创建 task，OUT_OF_SCOPE 标注 `[→ backlog]`
- 单条 Cx1 IN_SCOPE → 跳过清单直接修；批量或 Cx2+ → 必须创建
- 最后两项固定：`commit + push` + `更新主仓库 backlog（不提交）`
- 创建后立即执行，不等确认

---

## 阶段 4: 执行修复

### 4.1 Commit 格式

在当前分支直接修改。Commit: `fix(<scope>): <问题简述>` + 根因 + 复杂度 + Refs + Co-Authored-By。
scope 按层：kernel/runtime/cells/pkg。安全约束：只 add 修复文件（不 add -A）；不 amend。

### 4.4 执行代码修改（逐编辑测试循环）

对每个任务，执行 Edit-Test Loop + 状态更新：

1. **TaskUpdate → in_progress**（开始处理当前任务）
2. Read 目标文件
3. Edit / Write 修改代码
4. `go build ./...` — 编译检查
5. `go test ./修改的包/...` — **立即运行测试**（含阶段 1.5 的复现测试）
6. 如果测试失败：
   - 分析失败原因
   - 如果是当前编辑引入 → 立即修正，重回步骤 3
   - 如果是暴露了后续步骤的依赖 → 记录，继续下一步骤
7. 测试通过 → **TaskUpdate → completed** → 进入下一个任务

### 4.5 最终测试

全部修改完成后，运行完整测试：

```bash
go build ./...
go test ./path/to/modified/package/...
go test -race ./path/to/modified/package/...  # 涉及并发时
go test ./kernel/...                            # 改了 kernel 时
```

### 4.6 测试失败处理（分层回退）

| Round | 策略 |
|-------|------|
| 1-2 | 在当前方案上迭代修正 |
| 3 | `git stash` + 切换到备选方案重新执行 |
| 4 | 回滚（`git checkout -- <文件>`），Cx1 标 ESCALATE，Cx2 降级到最小修复标遗留 |

### 4.7 验证修复

重新执行阶段 1 的定位逻辑，确认：
- 原问题代码已被替换
- 数据流已正确保护
- 测试覆盖了问题场景

### 4.8 Git 收尾（测试通过后自动执行）

分两步：先提交分支代码，再更新主仓库 backlog。

**步骤 1: 提交当前分支代码**
1. `git add` 修复涉及的代码文件（不含 backlog）
2. 按 4.1 的关联模式执行 commit → push → PR

**步骤 2: 更新主仓库 backlog（不提交）**
1. 编辑主仓库 `docs/backlog.md`：
   - IN_SCOPE 已修的 finding → 标 `✅`，追加 PR 编号
   - OUT_OF_SCOPE finding → 新增条目到对应 Batch，标注来源
   - 发现的新问题 → 新增条目，标注 `(discovered via /fix <原问题>)`
   - 未登记的问题被修复 → 补登 + 标 `✅`
3. **不 commit、不 push** — backlog 更新留在主仓库工作区，由用户统一提交
4. **TaskUpdate → completed**（"backlog 更新" 任务）

---

## 阶段 5: 输出 + 验证

- 诊断报告（未修）
- 修复报告（已修）
- 批量验证（审查报告）

**Backlog 验证**（4.8 已执行，此处 grep 确认）：
- FIXED finding 在 backlog 标了 `✅` + PR 编号
- OUT_OF_SCOPE finding 已登记到对应 Batch
- 新发现的问题已追加

---

## 沟通规则

**默认按分析结果自动决策。** 仅以下情况用 AskUserQuestion：
- 无法定位问题代码
- 测试失败且 4 轮回退后仍无法修正
- 修复过程中发现新问题超出原始 scope
- Cx3/Cx4 用户追加了 `--auto` 参数（矛盾，需确认意图）
