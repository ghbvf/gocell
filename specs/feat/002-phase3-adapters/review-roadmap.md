# Roadmap Alignment Review -- Phase 3: Adapters

> Reviewer: Product Manager (Roadmap Alignment)
> Spec: specs/feat/002-phase3-adapters/spec.md
> Date: 2026-04-05
> Verdict: CONDITIONAL PASS -- 10 conditions below

---

## Review Summary

Phase 3 spec 在 6 adapter + testcontainers + Docker Compose 核心交付上与 roadmap 高度对齐。主要问题集中在：(1) master-plan 定义的 VictoriaMetrics adapter 被静默替换为 Grafana dashboard 缺失，(2) FR-9~FR-11 范围蔓延风险需要明确的时间预算隔离，(3) DEFERRED 的 8 条中有 2 条（#60/#61）在 phase-charter 中自相矛盾标注为"Phase 3 后期或 Phase 4"，(4) adapter 接口设计中缺少对 Phase 4 examples 消费场景的前瞻性验证。

---

## RM-01: VictoriaMetrics Adapter 被静默丢弃，与 master-plan 不一致

**[范围偏移]**

| 属性 | 内容 |
|------|------|
| 优先级 | **P0** |
| 问题描述 | master-plan 4.4 Layer 4 明确列出 5 个 First-class Adapter：`postgres / redis / oidc / s3 / victoriametrics`。roadmap Phase 3 Week 9 也写明 "PostgreSQL / Redis / OIDC / S3 / VictoriaMetrics"。然而 spec.md 的 FR-1~FR-6 交付的是 `postgres / redis / oidc / s3 / rabbitmq / websocket`——VictoriaMetrics 被 rabbitmq + websocket 静默替换，无任何变更说明或 ADR 记录。roadmap 将 RabbitMQ 和 WebSocket 归类在 "Formal Adapter Family"（Layer 5 `adapters/family/`），而非 Layer 4 First-class Adapter，但 spec 把它们拉平到了 `adapters/` 顶层，模糊了层级边界。 |
| 建议修改 | 在 spec 第 6 节"范围排除"中新增一条：`VictoriaMetrics adapter 延迟至 Phase 4（理由：Phase 3 聚焦数据持久化和消息传递，指标推送优先级低于 outbox 全链路）`。同时在 spec 第 4.4 节对标参考表中记录这一偏离决策。如果确认 Phase 3 不做 VictoriaMetrics，需同步更新 master-plan 的 Phase 3 条目，避免两份文档冲突。 |

---

## RM-02: Adapter 目录结构偏离 master-plan 的 Layer 4/Layer 5 分层

**[范围偏移]**

| 属性 | 内容 |
|------|------|
| 优先级 | **P1** |
| 问题描述 | master-plan 5.4/5.5 节将 adapter 分为三层：`adapters/`（First-class）、`adapters/family/`（Formal Family）、`adapters/optional/`（Optional）。spec 的 FR-1~FR-6 将 rabbitmq 和 websocket 直接放在 `adapters/` 下，与 postgres/redis/oidc/s3 同级，消除了 master-plan 的 First-class vs Formal Family 区分。这会影响 Phase 4 开发者对 adapter 支持等级的预期——`adapters/rabbitmq/` 和 `adapters/postgres/` 同级暗示相同的维护承诺，但 master-plan 的层级设计意图是 Formal Family 允许更低的 SLA。 |
| 建议修改 | 二选一：(A) 遵循 master-plan，将 rabbitmq 和 websocket 放在 `adapters/family/` 子目录下，保留层级语义；(B) 如果确认取消 Family 层级（所有 6 个 adapter 等同对待），在 spec 中明确声明这是对 master-plan 的有意偏离，并在 master-plan 中同步修订。推荐方案 (B)——实际开发中子目录只增加 import 路径复杂度，且 6 个 adapter 的维护承诺差异不大。 |

---

## RM-03: FR-9~FR-11 范围蔓延风险——72 条 tech-debt + 安全 + 产品修复挤压 adapter 交付时间

**[范围偏移]**

| 属性 | 内容 |
|------|------|
| 优先级 | **P0** |
| 问题描述 | roadmap 为 Phase 3 分配 14 天（Day 64-77），核心目标是 6 adapter + testcontainers。spec 在此基础上叠加了 FR-9（8 条安全修复）、FR-10（6 个子模块覆盖约 50 条 tech-debt）、FR-11（4 条产品修复）、FR-12（5 项文档）、FR-13（4 项 DevOps）。总计 14 个 FR，其中 adapter 核心仅占 FR-1~FR-8（8 个），非 adapter 工作占 FR-9~FR-14（6 个）。phase-charter 也承认纳入约 72 条 tech-debt。这意味着 Phase 3 的实际工作量远超 roadmap 预估的 14 天 adapter 开发。如果 tech-debt 修复与 adapter 开发争夺资源，adapter 质量或完成度可能受损。 |
| 建议修改 | (1) 在 spec 中新增"交付波次"章节，将 FR 分为两个 Wave：Wave 1（FR-1~FR-8, FR-13.1~13.4）= adapter 核心 + DevOps，Wave 2（FR-9~FR-12, FR-14）= tech-debt + 安全 + 文档 + 测试补全。明确 Wave 1 是 Phase 3 Gate 的硬性前提，Wave 2 溢出可延至 Phase 3.5 或 Phase 4 早期。(2) 在风险表中增加一条："72 条 tech-debt 挤压 adapter 进度——缓解：Wave 1 优先，Wave 2 P2/P3 级 tech-debt 允许 DEFERRED"。 |

---

## RM-04: DEFERRED 8 条中 #60/#61 自相矛盾——phase-charter 标注 "Phase 3 后期或 Phase 4" 但 spec 列为范围排除

**[验收标准缺失]**

| 属性 | 内容 |
|------|------|
| 优先级 | **P1** |
| 问题描述 | spec 第 6 节"范围排除"明确写 "Phase 2 DEFERRED 的 8 条高风险重构不在本 Phase 范围"，引用编号 #54, #56-59, #60-62。但 phase-charter 的延迟处理表中，#60（configsubscribe unmarshal 失败 ACK）和 #61（auditappend publish 失败仅 log）的计划修复 Phase 列是 "Phase 3 后期或 Phase 4"，并非纯粹 DEFERRED。这两条与 adapter 直接相关（#60 需要 RabbitMQ DLQ，#61 需要 outbox），在 Phase 3 adapter 就绪后修复是合理的。但 spec 的排除声明与 charter 的 "Phase 3 后期" 表述矛盾。 |
| 建议修改 | 将 #60 和 #61 从 spec 第 6 节排除列表中移除，纳入 FR-10.2 架构修复子模块（或新建 FR-10.7）。验收标准：#60 — configsubscribe unmarshal 失败路由至 DLQ（依赖 FR-5.4 ConsumerBase）；#61 — auditappend publish 改用 outbox.Writer 事务内写入（依赖 FR-1.4）。如果时间不允许，保留 DEFERRED 但统一 charter 和 spec 的表述为 "DEFERRED to Phase 4"。 |

---

## RM-05: Phase 4 examples 消费场景缺少前瞻性接口验证

**[开发者体验]**

| 属性 | 内容 |
|------|------|
| 优先级 | **P1** |
| 问题描述 | Phase 4 的 3 个 examples（sso-bff / todo-order / iot-device）是 GoCell 对外展示的第一接触点。sso-bff 需要 oidc adapter + postgres adapter + rabbitmq adapter 协同工作；todo-order 需要 postgres adapter + rabbitmq adapter；iot-device 需要 websocket adapter + s3 adapter。但 spec 未定义任何 "adapter 组合使用" 的集成测试场景——FR-8 的 testcontainers 测试都是单 adapter 或 outbox 链路测试，没有 "多 adapter 注入同一 Assembly 并协同工作" 的验证。如果 Phase 3 不验证组合场景，Phase 4 可能在 assembly 层注入时发现接口不兼容（如 TxManager 和 outbox.Writer 的 ctx 传递、graceful shutdown 顺序冲突）。 |
| 建议修改 | 在 FR-8 中新增 FR-8.5：**Assembly 组合集成测试**——至少一个 testcontainers 测试验证 "postgres Pool + TxManager + outbox.Writer + rabbitmq Publisher + redis IdempotencyChecker 同时注入 CoreAssembly，执行 Start -> 业务写入 -> outbox relay -> consume -> Stop" 全生命周期。这个测试直接对应 Phase 4 sso-bff 的核心链路，可提前暴露组合问题。 |

---

## RM-06: Adapter 配置注入模式未标准化，可能约束 Phase 4 的 examples 灵活性

**[开发者体验]**

| 属性 | 内容 |
|------|------|
| 优先级 | **P1** |
| 问题描述 | NFR-6 要求 "每个 adapter 的配置通过 struct（Config）注入"，但 spec 未定义统一的配置加载协议。6 个 adapter 各自定义 `Config` struct，每个 struct 独立从环境变量读取，缺少一个 "从统一配置源（如 config-core 的 YAML + env overlay）构造全部 adapter Config" 的标准模式。Phase 4 的 examples 需要在 main.go 中手动构造 6 个 Config struct，如果没有统一模式，每个 example 的 bootstrap 代码都会写一遍环境变量解析。spec 4.2 节的注入点示例展示了 `cfg.Postgres` / `cfg.Redis` 的分段结构，但这个 `cfg` 的来源和格式未定义。 |
| 建议修改 | 在 FR-13 或 NFR-6 中新增要求：定义 `adapters/config.go`（或在 `runtime/bootstrap/` 中扩展），提供统一的 `AdapterConfigs` struct，包含所有 6 个 adapter 的 Config 子段，支持从环境变量或 YAML 文件一次性加载。Phase 4 的 examples 只需 `cfg := bootstrap.LoadAdapterConfigs()` 一行即可获取全部配置。如果认为这超出 Phase 3 范围，至少在 FR-12.4 的 adapter 配置参考文档中提供一个推荐的 "统一 Config struct" 模式，供 Phase 4 采用。 |

---

## RM-07: tech-debt 分层中 P2-可选 30 条的溢出策略不够具体

**[验收标准缺失]**

| 属性 | 内容 |
|------|------|
| 优先级 | **P1** |
| 问题描述 | phase-charter 将 80 条 tech-debt 分为 P0（16 条）、P1（26 条）、P2（30 条）、P3（8 条 DEFERRED）。product-context S7 的成功标准是 "80 条中至少 60 条 RESOLVED"。这意味着 P0（16）+ P1（26）= 42 条必须完成，P2（30 条）中至少需要完成 18 条才能达到 60 条阈值。但 spec 和 charter 对 P2 的 30 条没有进一步排序——哪 18 条优先？哪 12 条可以 DEFERRED？如果 P2 中治理规则（#28-29, #36-46）和运维/DX（#65-68, #76-78）同等优先级，实施时可能选择低价值但容易完成的条目来凑数量，忽略对 Phase 4 更有价值的条目。 |
| 建议修改 | 将 P2 的 30 条进一步拆分为 P2-High（对 Phase 4 有直接影响的条目，优先完成）和 P2-Low（可 DEFERRED 的条目）。建议 P2-High 候选：#63（config-core handler chi 耦合——影响 Phase 4 router 灵活性）、#65（statusRecorder 重复——影响 Phase 4 middleware 扩展）、#52（contract ID 格式不一致——影响 Phase 4 scaffold 使用体验）、DX-02（doc.go 补全——影响 Phase 4 godoc 可读性）。P2-Low 候选：#38（map 遍历顺序不稳定——cosmetic）、#45（Catalog O(n*m)——性能在当前规模无影响）、#47（StatusBoardEntry YAML tag——命名约定，不影响功能）。 |

---

## RM-08: Grafana Dashboard 模板在 roadmap Phase 3 Week 10 中定义但 spec 未覆盖

**[范围偏移]**

| 属性 | 内容 |
|------|------|
| 优先级 | **P2** |
| 问题描述 | roadmap Phase 3 Week 10 的描述包含 "Grafana dashboard 模板"。master-plan 2.2 也将 Grafana 列为运维看板组件，7 节明确 "cell health / outbox lag" dashboard。但 spec 的 FR-1~FR-14 没有任何一条覆盖 Grafana dashboard 交付。这可能是有意排除（与 VictoriaMetrics 一同延迟），但 spec 未显式声明。 |
| 建议修改 | 在 spec 第 6 节"范围排除"中新增：`Grafana dashboard 模板延迟至 Phase 4（理由：依赖 VictoriaMetrics adapter 或 Prometheus 端点稳定后再设计 dashboard）`。或者，如果认为 dashboard 可以基于 slog 计数日志实现轻量版，则在 FR-12 中新增 FR-12.6 定义最小 Grafana JSON 模板。 |

---

## RM-09: master-plan 中 Kernel 层的 webhook / reconcile / scheduler / rollback 在 Phase 3 的处置状态不明

**[范围偏移]**

| 属性 | 内容 |
|------|------|
| 优先级 | **P2** |
| 问题描述 | master-plan 5.1 Kernel 层列出了多个能力模块：`webhook/`（receiver + dispatcher）、`reconcile/`（状态收敛运行时）、`scheduler/`（cron/定时任务）、`rollback/`（metadata + kill switch）、`support/`（support bundle）。Phase 1 roadmap 将 webhook/reconcile/rollback 放在 Week 3（Day 22-28），但 Phase 2 tech-debt 中没有这些模块的条目，spec Phase 3 也未提及。这意味着这些模块要么在 Phase 1 已实现（但 Phase 1 的 30 文件 4600 行中似乎未包含），要么被静默延迟。如果 Phase 4 的 examples 需要 webhook 或 scheduler 能力，缺失状态需要明确。 |
| 建议修改 | 在 spec 中新增"Phase 间能力状态矩阵"小节，列出 master-plan Kernel 层所有模块在 Phase 0-3 的实现状态（done / partial / deferred / n/a）。对于 webhook、reconcile、scheduler、rollback、support 模块，明确标注当前状态和计划 Phase。这不增加 Phase 3 工作量，但为 Phase 4 规划提供清晰基线。 |

---

## RM-10: Cell Repository 实现的归属在 Phase 3 和 Phase 4 之间模糊

**[验收标准缺失]**

| 属性 | 内容 |
|------|------|
| 优先级 | **P1** |
| 问题描述 | spec 5.2 节明确写 "具体 Repository 实现由 Cell 内部 `internal/adapters/` 子包完成（或在 Phase 4 补全）"。但 Phase 4 roadmap 的描述是 "Examples + 文档 + Optional 接口"，没有提及 Cell Repository 实现。同时，FR-8.4 的 Journey 集成测试（J-audit-login-trail、J-config-hot-reload）需要真实的数据持久化才能端到端验证——如果 Cell Repository 仍然是 in-memory，这些 Journey 测试的 "真实端到端" 含义打折扣。product-context S3 的成功标准写 "不再依赖 in-memory stub"，这暗示 Phase 3 需要实现至少部分 Cell Repository（如 AuditRepository 的 PostgreSQL 实现，用于 J-audit-login-trail）。 |
| 建议修改 | 明确划线：(A) Phase 3 实现 J-audit-login-trail 和 J-config-hot-reload 所需的最小 Cell Repository PostgreSQL 实现（AuditRepository + ConfigRepository，放在 `cells/*/internal/adapters/postgres/`），作为 FR-8.4 的前置依赖；(B) 其余 Cell Repository（UserRepository、SessionRepository、RoleRepository、FlagRepository）延至 Phase 4 的 examples 中实现。在 spec 5.2 节将 "(或在 Phase 4 补全)" 改为具体清单。在 product-context S3 的验证方式中注明哪些 Repository 用真实 adapter、哪些仍用 in-memory stub。 |

---

## Summary Matrix

| ID | Title | Priority | Category |
|----|-------|----------|----------|
| RM-01 | VictoriaMetrics adapter silently dropped | P0 | Scope Drift |
| RM-02 | Layer 4/5 directory structure divergence | P1 | Scope Drift |
| RM-03 | FR-9~FR-11 scope creep risk (72 tech-debt items) | P0 | Scope Drift |
| RM-04 | DEFERRED #60/#61 contradiction between charter and spec | P1 | Missing AC |
| RM-05 | No multi-adapter assembly integration test for Phase 4 | P1 | DX |
| RM-06 | Adapter config injection pattern not standardized | P1 | DX |
| RM-07 | P2 tech-debt overflow strategy lacks specificity | P1 | Missing AC |
| RM-08 | Grafana dashboard template missing from spec | P2 | Scope Drift |
| RM-09 | Kernel webhook/reconcile/scheduler status unknown | P2 | Scope Drift |
| RM-10 | Cell Repository ownership between Phase 3 and 4 is ambiguous | P1 | Missing AC |

**P0 (must fix before implementation)**: 2 items (RM-01, RM-03)
**P1 (should fix before task breakdown)**: 6 items (RM-02, RM-04, RM-05, RM-06, RM-07, RM-10)
**P2 (can fix during implementation)**: 2 items (RM-08, RM-09)
