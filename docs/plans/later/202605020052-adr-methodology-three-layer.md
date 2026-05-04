# ADR-202605020052: 方法论三层关系（v3 / constitution / CLAUDE.md）

## Status
Accepted (2026-05-02)

## Context

GoCell 项目同时存在三层"治理性文档"，三者之间的关系若不显式定义，会引发以下问题：
- 落地 PR 不知道引用哪个作为权威
- 三层之间的冲突无解决路径
- 新加治理规则不知应该放在哪一层
- 引入外部方法论（如 v3）时与项目宪法不知如何协调

三层文档：

1. **`~/Documents/methodology/v3.md`**（外部，跨项目）：《Vibe Coding 工程化治理 v3》—— 跨项目、跨技术栈的高阶方法论
2. **`.specify/memory/constitution.md`**（项目宪法）：GoCell 项目自身的"九大核心原则 + 17 条红线 + 六条真相"
3. **`CLAUDE.md`**（实施细则）：AI 协作约束、工作流、命名规范、依赖选择原则

引入 v3 时必须澄清这三者的角色与冲突解决顺序。

## Decision

### 三层各自定位

- **v3**：**高阶参考方法论**（advisory layer）
  - 提供推理框架与能力金字塔
  - **不是规范**，不直接产生约束
  - 跨项目通用，本项目可选择性吸收
  - 修订门槛：v3 自身有 changelog；项目反向反馈触发 v3.x 演进

- **constitution**：**项目宪法**（normative layer）
  - GoCell 项目的最高规范来源
  - 红线（RL-XX）、真相、原则在此定义
  - 修改需要 ADR
  - 修订门槛：高（每次修订记入 ADR）

- **CLAUDE.md**：**协作细则**（operational layer）
  - AI 与人协作的具体约束
  - 工作流、命名、Sandbox 提权等
  - 修改门槛：低（PR 即可）

### 冲突解决顺序

```
实践证据（PR review / test / bug 报告）> constitution > CLAUDE.md > v3
```

**理由**：
- **实践证据居首**：v3 是 1 周前刚产出的探索性理论，**未经任何工程实践验证**。任何 PR review、test 失败、生产 bug 报告都比 v3 更接近真相
- constitution 是项目最高规范，违反 constitution 即违反项目根本
- CLAUDE.md 是项目级具体规范，落实 constitution 的执行约束
- **v3 排在最后**：是参考方法论，不是规范

**重要前提**：v3 是 advisory（参考）不是 normative（规范）。本项目可选择性吸收，**有权在实践中推翻 v3 任何主张**（详见下节"质疑 v3 的机制"）。

**例外**：当 v3 提出比 constitution / CLAUDE.md 更严格的约束，**经实践验证后**可选择吸收并升级到 constitution / CLAUDE.md，但：
- 升级前必须有 GoCell 实践证据支撑（不是因为 v3 说所以照做）
- 升级动作本身需要 ADR
- 若实践证据相反，应反向修订 v3，而非升级到 constitution

### 质疑 v3 的机制（实践对理论的反向校准）

v3 不是教条。GoCell 实践有权且应该质疑 v3。机制：

| 触发器 | 处理方式 |
|---|---|
| GD 落地中发现 v3 某主张不工作 | 立即在 [v3 ↔ GoCell 对照索引](202605020052-001-v3-gocell-mapping.md) "主张验证清单" 中标 "refuted" |
| GD 落地中发现 v3 某主张需要修改 | 标 "modified"，记录修改内容 |
| GD 落地完整通过 v3 主张 | 标 "survive" |
| 一个 GD 完成时所有主张全 "survive" | **预警**：可能未真正质疑 v3，要求复审 |
| 多个主张被 "refuted" | GD6 季度评审决定 v3 → v3.1 修订 |

**底线**：constitution / CLAUDE.md 是 GoCell 项目内的规范；v3 是外部参考。冲突时项目规范优先，且实践对项目规范也可质疑（通过 ADR）。

### 治理规则的归属判断

新规则应放在哪一层？

| 情况 | 归属 | 例子 |
|---|---|---|
| 影响项目根本（违反即不允许构建） | constitution 红线 | 拟新增 RL-18 "无 lifecycle 字段不允许构建" |
| 影响 AI/人协作（工作流、命名、依赖） | CLAUDE.md | "改公共签名要 integration-tag build" |
| 跨项目通用，本项目正在吸收 | v3.md（外部）+ 引用到 constitution | 自主权梯度 R1-R5 |
| 单一 ADR 决策 | docs/architecture/<adr> | 本文件 |

### 关于 v3 的"自我演进"

GoCell 实施 v3 过程中发现的 v3 不足，应**回写到 v3.md** 形成 v3.1。这是 v3 自身的治理。

回写机制（GD4 之后启用）：
- 每个 GD 末尾自动开 `v3-feedback` issue
- GD6 季度评审汇总 → 决定 v3.x 修订内容

### 关于 v3 与项目 readiness 的关系

**首要原则**：项目自身的 v1.0 发布优先于 v3 落地。

如果 v3 某条建议与 GoCell v1.0 发布冲突 → 暂缓吸收，先发布。

### 维护责任

| 文档 | 修订触发器 | 决策方式 |
|---|---|---|
| v3.md | GD4 之后 `v3-feedback` issue 累积 | GD6 季度评审 |
| constitution.md | 项目根本变化（架构方向、合规要求等）| ADR |
| CLAUDE.md | 工作流/规范变化 | PR review |

## Consequences

### 正面
- 三层关系清晰，落地 PR 知道引用哪一层
- v3 不直接绑定项目，project agility 不受外部方法论变化影响
- v3 自身可基于 GoCell 实施反馈演进（v3 → v3.1）
- 新 GD 批次启动时，按"治理规则归属判断"表决定文档归宿，无歧义

### 负面
- 增加文档维护成本（三层都要维护）
- 团队理解成本上升（需要分清在哪一层做修改）

### 中性
- v3 落地路径（GD1-GD6）需要每个批次明确"本批升级哪一层"
- 跨语言推广时，目标项目的 constitution / CLAUDE.md 等价物可能命名不同，但三层结构应保持

## 跨语言推广注意

本 ADR 是 GoCell 实例化的具体决策。其他语言/框架（iOS / .NET / C++ / TS 等）实例化时：

- v3.md 不变（共享外部参考）
- "项目宪法"对应：iOS Info.plist 项目配置 / .NET 项目级规范 / C++ 项目 README / TS package.json + ESLint config
- "协作细则"对应：各项目的 contributor guide / coding style 等

三层结构本身可复制，具体形态因栈而异。

## 相关文档

- [`~/Documents/methodology/v3.md`](file:///Users/shengming/Documents/methodology/v3.md)（外部 v3）
- [项目宪法](../../../.specify/memory/constitution.md)
- [CLAUDE.md](../../../CLAUDE.md)
- [v3 ↔ GoCell 逐节映射](202605020052-001-v3-gocell-mapping.md)
- [GD1-GD6 路线图](202605020052-030-vibe-coding-v3-landing-roadmap.md)
