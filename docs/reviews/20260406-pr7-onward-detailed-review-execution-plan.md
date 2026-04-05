# PR #7 起详细 Review 执行计划

> 日期: 2026-04-06
> 仓库: `ghbvf/gocell`
> 适用范围: PR #7 ~ #32, Issue #18 ~ #27
> 关联文档:
> - `docs/reviews/20260406-pr7-onward-three-review-plans.md`
> - `docs/reviews/202604051500-032-phase3-pr7-12-six-role-review.md`
> - `docs/architecture/metadata-model-v3.md`

## 目标

这份计划不是“选一种 review 方法”, 而是把 4 条线一起跑通:

1. 模块 review: 先看系统边界、依赖图、跨模块不变量。
2. PR review: 再把问题落回具体变更单元。
3. Follow-up issue review: 把 `Issue #18 ~ #27` 当作 `PR #7 ~ #12` 的 forward-fix backlog 单独处理。
4. 权威裁决 review: 对高风险分歧点由 Architect、Kernel Guardian、Developer 做最终裁决。

结论: 模块 review 不能替代 PR review, 但模块 review 必须先于 PR review。

## 审查对象矩阵

| 模块 | 目录范围 | 主要 PR | 主要 issue | 主要风险 |
|---|---|---|---|---|
| M1 基础内核 | `src/pkg`, `src/kernel` | #7, #8, #17, #30, #31, #32 | #21, #22, #23 | 错误码、一致性语义、生命周期、治理规则 |
| M2 运行时 | `src/runtime` | #7, #16, #31, #32 | #24 | bootstrap、auth、shutdown、worker、可观测性 |
| M3 适配器-A | `src/adapters/postgres`, `src/adapters/redis`, `src/adapters/rabbitmq` | #10, #11, #12, #13, #31, #32 | #18, #20, #21, #22, #25, #26 | outbox、tx、lock、idempotency、delivery、reconnect |
| M4 适配器-B | `src/adapters/oidc`, `src/adapters/s3`, `src/adapters/websocket` | #14, #31, #32 | 无主 follow-up issue | 外部协议边界、抽象稳定性、扩展点 |
| M5 Cell/Contract/Journey | `src/cells/access-core`, `src/cells/audit-core`, `src/cells/config-core`, `src/contracts`, `src/journeys`, `src/assemblies` | #8, #13, #14, #15, #31, #32 | #24 | 契约闭环、事件传播、L2/L3 一致性 |
| M6 交付层 | `src/cmd`, `src/examples`, `src/tests`, `Makefile`, `.github`, `docker-compose.yml` | #9, #28, #29, #30, #32 | #19 | wiring、集成验证、脚本可靠性、文档可执行性 |

## 执行原则

### 范围原则

1. 不跳过模块 review。
2. 不跳过 PR review。
3. 不把 `Issue #18 ~ #27` 误算成 PR。
4. 不把 `PR #31/#32` 当成普通小 PR, 它们是集成型 PR。

### 并行原则

1. 每个阶段固定 `4-6` 个子 agent。
2. 每个 agent 只负责一个清晰对象: 一个模块、一个 PR、一个问题簇, 或一个整合任务。
3. 同一阶段内 agent 之间不能重复审同一对象。

### 输出原则

每个 agent 报告必须使用同一结构:

1. Scope: 本 agent 实际审查的范围。
2. Findings: `P0 / P1 / P2`。
3. Evidence: 代码路径、契约、测试、issue/PR 编号。
4. Dependencies: 上下游影响面。
5. Verdict: `APPROVE / BLOCKED / NEEDS-FOLLOWUP`。

### 裁决原则

1. 任一 `P0` 未闭合, 当前对象不能签核。
2. `Kernel invariant` 高于实现便利。
3. `Architecture boundary` 高于局部 workaround。
4. PR 结论与模块结论冲突时, 先回到模块结论核对, 再由权威角色拍板。

## 详细阶段

## Round 0: 基线与建图

### 目标

建立后续所有审查共享的事实基线, 防止不同 agent 基于不同理解工作。

### 子 agent 编组

| Agent | 负责对象 | 输出 |
|---|---|---|
| R0-A1 | PR 时间线与批次表 | PR/Issue 清单, 批次归属 |
| R0-A2 | Go import 依赖图 | 包级依赖总图 |
| R0-A3 | `slice.contractUsages + contract.yaml` 图 | Cell/Contract 依赖图 |
| R0-A4 | Journey/Assembly 路径图 | 物理打包与验收链路图 |
| R0-A5 | 现有审查资料整合 | 已知 findings 基线 |

### 输入

- `docs/architecture/metadata-model-v3.md`
- `docs/reviews/202604051500-032-phase3-pr7-12-six-role-review.md`
- `src/cells/**/slice.yaml`
- `src/contracts/**/contract.yaml`
- `src/journeys/**`
- `src/assemblies/**`
- GitHub PR #7 ~ #32
- GitHub Issue #18 ~ #27

### 输出

1. `PR / Issue -> 批次` 对照表
2. `模块 -> PR / Issue` 映射表
3. 代码 import 依赖图
4. Cell / Contract 依赖图
5. 初始风险热区列表

### Exit Gate

以下 3 个条件全部满足才进入 Round 1:

- `Issue #18 ~ #27` 已被明确标记为 follow-up lane
- Cell / Contract 依赖图能解释 `access-core / audit-core / config-core` 之间的关系
- `PR #31/#32` 被标记为集成型 PR, 不会在后续被当成普通 PR 处理

## Round 1A: 模块 Review 第一波

### 目标

先拿下基础规则和高风险适配器主干。

### 子 agent 编组

| Agent | 负责对象 | 核心问题 |
|---|---|---|
| R1A-A1 | M1 基础内核 | 治理规则、生命周期、不变量 |
| R1A-A2 | M2 运行时 | bootstrap、auth、shutdown、worker |
| R1A-A3 | M3-Postgres | tx、migrator、outbox writer/relay |
| R1A-A4 | M3-Redis | distlock、idempotency、cache |
| R1A-A5 | M3-RabbitMQ | publisher、subscriber、DLQ、reconnect |
| R1A-A6 | 综述整理 | 第一波模块 finding 汇总 |

### 输出

- M1 报告
- M2 报告
- M3 三份子报告
- 第一波跨模块风险汇总

### Exit Gate

以下问题必须有明确结论:

- outbox 与 idempotency 的责任分界
- migrator / rollback / reconnect / lock renewal 的风险级别
- runtime 对 kernel 抽象的依赖是否越界

## Round 1B: 模块 Review 第二波

### 目标

完成业务层和交付层闭环。

### 子 agent 编组

| Agent | 负责对象 | 核心问题 |
|---|---|---|
| R1B-A1 | M4 适配器-B | OIDC/S3/WebSocket 边界和扩展点 |
| R1B-A2 | M5-Access/Audit | 事件链路、审计闭环、契约一致性 |
| R1B-A3 | M5-Config/Assembly/Journey | 配置传播、契约闭环、验收路径 |
| R1B-A4 | M6 交付层 | examples、tests、scripts、docs |
| R1B-A5 | 模块交叉核对 | 复核 M1~M6 之间的接口缺位 |
| R1B-A6 | 综述整理 | 第二波模块 finding 汇总 |

### 输出

- M4 报告
- M5 两份子报告
- M6 报告
- 模块总图与热区图

### Exit Gate

以下问题必须形成明确判断:

- Cell 与 Contract 是否存在声明与实现不一致
- Journey 是否真的能覆盖高风险路径
- examples / cmd / tests 是否能证明主链路成立

## Round 1C: 模块级权威综述

### 目标

把模块结论固化成后续 PR review 的上位准则。

### 子 agent 编组

| Agent | 负责对象 | 产出 |
|---|---|---|
| R1C-A1 | Architect | 模块边界结论 |
| R1C-A2 | Kernel Guardian | 一致性与生命周期结论 |
| R1C-A3 | Developer | 正确性与测试结论 |
| R1C-A4 | Security | 凭证、协议、日志暴露结论 |
| R1C-A5 | QA | 集成测试与回归链路结论 |
| R1C-A6 | Scribe | 模块级审查基线文档 |

### 输出

1. 模块级 blocking list
2. 后续 PR review 必须复核的规则清单
3. `Issue #18 ~ #27` 的模块归属表

## Round 2: PR Batch A Review

### 范围

`PR #7 ~ #12`

### 子 agent 编组

| Agent | 负责对象 | 模块锚点 |
|---|---|---|
| R2-A1 | PR #7 | M1 + M2 |
| R2-A2 | PR #8 | M1 + M5 |
| R2-A3 | PR #9 | M6 |
| R2-A4 | PR #10 | M3 |
| R2-A5 | PR #11 | M3 |
| R2-A6 | PR #12 | M3 |

### 核心动作

1. 把模块 review 的结论落回 PR。
2. 标记“这个问题是在 PR 中引入, 还是 PR 之后才暴露”。
3. 为每个 PR 建立 follow-up 映射。

### 输出

每个 PR 一份报告, 外加一份 Batch A 汇总:

- PR 结论
- 关联 issue
- forward-fix 优先级

## Round 2.5: Follow-up Lane Review

### 范围

`Issue #18 ~ #27`

### 子 agent 编组

| Agent | 负责对象 | 覆盖 issue |
|---|---|---|
| R2.5-A1 | RabbitMQ follow-ups | #18, #25, #26 |
| R2.5-A2 | Postgres follow-ups | #21, #22 |
| R2.5-A3 | Redis follow-up | #20 |
| R2.5-A4 | Auth + UID follow-ups | #23, #24 |
| R2.5-A5 | DevOps + 汇总入口 | #19, #27 |

### 核心动作

1. 判断这些 issue 是实现 bug、设计 bug, 还是验证缺口。
2. 建立 `issue -> 原始 PR -> 模块 -> 推荐修复 PR` 的映射。
3. 输出 forward-fix 修复顺序。

### 输出

1. 问题簇报告 5 份
2. follow-up 修复优先级表
3. P0/P1 修复包建议

## Round 3: PR Batch B Review

### 范围

`PR #13 ~ #17`

### 子 agent 编组

| Agent | 负责对象 | 模块锚点 |
|---|---|---|
| R3-A1 | PR #13 | M3 + M5 |
| R3-A2 | PR #14 | M3 + M4 + M5 |
| R3-A3 | PR #15 | M5 |
| R3-A4 | PR #16 | M2 + M5 |
| R3-A5 | PR #17 | M1 + M2 |

### 核心动作

1. 检查这些 PR 是否真正修补了 Batch A 暴露出的架构缝隙。
2. 检查它们有没有引入新的内核或运行时偏移。
3. 把 `Issue #18 ~ #27` 对这些 PR 的影响面重新核对一次。

### 输出

- 5 份 PR 报告
- 一份 Batch B 跨 PR 风险图

## Round 4: PR Batch C Review

### 范围

`PR #28 ~ #30`

### 子 agent 编组

| Agent | 负责对象 | 模块锚点 |
|---|---|---|
| R4-A1 | PR #28 | M6 |
| R4-A2 | PR #29 | M6 |
| R4-A3 | PR #30 | M1 + M2 + M6 |
| R4-A4 | 集成校验 | Batch A/B 与 Wave 4 之间的证据闭环 |

### 核心动作

1. 确认测试、文档、KG 验证是否真实覆盖前面问题。
2. 防止“写了文档/脚本但不能证明系统成立”的假闭环。

### 输出

- 3 份 PR 报告
- 1 份集成证据报告

## Round 5: PR Batch D Review

### 范围

`PR #31 ~ #32`

### 子 agent 编组

| Agent | 负责对象 | 角色 |
|---|---|---|
| R5-A1 | PR #31 | Phase 3 集成 reviewer |
| R5-A2 | PR #32 | Phase 3+4 集成 reviewer |
| R5-A3 | 回归链路 | end-to-end / assembly / journey reviewer |
| R5-A4 | 变化归并 | 合并 PR 对前序 findings 的覆盖率核对 |

### 核心动作

1. 检查合并 PR 是否真的覆盖了前序 PR 和 follow-up issue。
2. 检查是否存在“个别 PR 修了, 合并 PR 又回退”的情况。
3. 检查 examples / docs / tests 是否与最终集成状态一致。

### 输出

- PR #31 报告
- PR #32 报告
- 集成回归报告
- findings 覆盖率矩阵

## Round 6: 权威裁决

### 目标

所有有分歧或高风险的问题, 在这里完成最终定性。

### 子 agent 编组

| Agent | 角色 | 主要职责 |
|---|---|---|
| R6-A1 | Architect | 拍板分层、边界、抽象归属 |
| R6-A2 | Kernel Guardian | 拍板一致性、生命周期、不变量 |
| R6-A3 | Developer | 拍板实现正确性、维护成本、测试缺口 |
| R6-A4 | Security | 拍板凭证、协议、日志、攻击面 |
| R6-A5 | QA | 拍板验证充分性与回归风险 |
| R6-A6 | Scribe / Arbiter | 形成统一裁决文档 |

### 裁决输入

- 模块总图
- 每个 PR 报告
- follow-up lane 报告
- 汇总 blocking list

### 裁决输出

1. 最终 `BLOCKED / CONDITIONAL / APPROVED` 清单
2. 必修修复项
3. 可延后修复项
4. 必补测试项
5. 不建议进入 kernel 的候选项

## Round 7: 修复分包与回归计划

### 目标

把 findings 转成可执行修复波次, 避免一口气混修。

### 建议修复包

| 修复包 | 范围 | 目标 |
|---|---|---|
| Fix Pack A | 所有 P0 | 数据丢失、安全、重复消费、错误 ACK |
| Fix Pack B | P1 正确性 | rollback、lock renewal、atomicity、reconnect |
| Fix Pack C | P1 设计/接口 | kernel/runtime 抽象缺口、契约归属 |
| Fix Pack D | P2 + tests/docs/ops | 覆盖率、脚本、文档、验证链路 |

### 回归顺序

1. 先回归 `M3` 适配器主链路
2. 再回归 `M2 + M5` 的 runtime/cell 主链路
3. 再回归 `M6` 交付层与 examples
4. 最后回归合并 PR 视角的全链路

## 各阶段固定交付物

| 阶段 | 交付物 |
|---|---|
| Round 0 | 基线与依赖图 |
| Round 1A/1B/1C | 模块报告、模块 blocking list、模块审查基线 |
| Round 2 | `PR #7 ~ #12` 报告与 Batch A 汇总 |
| Round 2.5 | follow-up lane 报告与修复优先级 |
| Round 3 | `PR #13 ~ #17` 报告与 Batch B 汇总 |
| Round 4 | `PR #28 ~ #30` 报告与集成证据报告 |
| Round 5 | `PR #31/#32` 报告与 findings 覆盖矩阵 |
| Round 6 | 权威裁决文档 |
| Round 7 | 修复分包计划与回归计划 |

## 最终结果必须回答的问题

执行完这套计划后, 最终文档必须能明确回答:

1. 当前系统的核心不变量是什么。
2. 哪些问题是模块级系统性问题。
3. 哪些问题是由具体 PR 引入。
4. `Issue #18 ~ #27` 应该分别挂到哪个修复包。
5. 哪些抽象应该进入 kernel/runtime。
6. 哪些内容应留在 adapter/cell/example 层。
7. 哪些 PR 可以被视为“已审完并可归档”。
8. 哪些集成风险仍未闭合。

## 推荐执行顺序

如果按真实执行节奏推进, 推荐顺序固定为:

1. Round 0
2. Round 1A
3. Round 1B
4. Round 1C
5. Round 2
6. Round 2.5
7. Round 3
8. Round 4
9. Round 5
10. Round 6
11. Round 7

不要把 Round 2 提前到 Round 1 之前, 否则 PR review 会失去统一准绳。
