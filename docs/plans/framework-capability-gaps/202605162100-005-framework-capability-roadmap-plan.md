# 041 框架能力缺口路线计划（W0–W10）

**生成日期**：2026-05-16
**性质**：roadmap 级规划（非单 PR 实施计划）。每个 Wave 立项时必须再产出独立的 PR 级实施计划 + ADR。
**来源分析**（同目录，本计划的真值源，禁止在本文重述结论）：
- [`202605131500-001-ddd-scope-analysis.md`](202605131500-001-ddd-scope-analysis.md) — DDD 适用范围（结论：收敛到 `cells/`）
- [`202605131500-002-framework-capability-inventory.md`](202605131500-002-framework-capability-inventory.md) — 现有能力基线（A–P 16 域）
- [`202605131500-003-capability-interaction-patterns.md`](202605131500-003-capability-interaction-patterns.md) — 8 场景能力串联（现状）
- [`202605131500-004-capability-gap-analysis.md`](202605131500-004-capability-gap-analysis.md) — **35 缺口 + W0–W10 路线（任务全集在此）**

**触发**：用户 2026-05-16 要求把分支 `claude/analyze-ddd-api-design-7ucLo` 的四份分析文档落入 develop `docs/plans/`，并据 004 的 W0–W10 建立路线计划。

**与现有 plan 的关系（边界划清，避免 scope 碰撞）**：

| 既有 plan | 轨道 | 与本计划关系 |
|---|---|---|
| `202605101839-029-master-roadmap.md` | 关键路径 12/12 ✅ + errcode/K/G 残余 + F4/F5 | 不同轨道。029 是**收尾既有承诺**；本计划是**新增框架能力**，独立 lane |
| `202605082145-034-pg-corecell-b-route-plan.md` | accesscore PG 链 | 不同轨道。本计划不触 accesscore 实施路线 |
| `202605112000-036` / `037` / `037r2` | archtest/governance Wave 推进 | 正交。036 系列守"现有能力的约束 enforcement"；本计划是"新能力抽象"。新能力落地时其 enforcement 进 036 体系，不在此重复 |
| `202605121830-038` / `039` / `040` | P0/P1 阻塞 + archtest pass funnel | 正交。本计划不含 backlog 阻塞项 |

> 结论：本计划开辟独立的 **「框架能力扩展」轨道**，与现有收尾/治理/PG 轨道无文件域重叠，可并行但**优先级低于关键路径残余与 P0/P1 阻塞**。

---

## 0. 立项前置决策门（必须先答，未答不得进 W0）

004 §总判断给出战略二选一，这是本路线的**根门**，需 architect / product 显式裁决：

> **DG-0：框架接下来「做深现有能力精度」还是「做广能力组合」？**
> - 004 论点：前者已处 marginal returns，后者直接决定 L3+ 能否落地，ROI 更高。
> - 本计划**假设选「做广组合」**展开 W0–W10；若裁决为「做深精度」，本计划整体降级为 backlog 候选池，不排期。

**DG-1：AI-rebust 立项门（CLAUDE.md §AI 协作章程硬约束）**
W0–W10 多数项是「新增框架抽象 / 新增 enforcement 机制」，受 `.claude/rules/gocell/ai-collab.md` 约束：

- 每个 Wave 立项时，其引入的 archtest / codegen funnel / type marker / sealed interface 必须给 **AI-rebust 三档评级**；**Soft 严禁立项**。
- funnel 类抽象（缺口 4 Command Bus / 缺口 6 Schema Registry / 缺口 22 AuthZ↔contract 等）必须**分别**给「上游 Hard / 下游 Hard」两栏评级，仅一侧 Hard 不构成闭环。
- 引入的隐式约束（wire envelope 字段集 / 跨 yaml↔code 关系）必须**同 PR 闭环**三件套：静态守卫 + 文档契约 + 回归测试（参见 memory `feedback_constraint_self_close`）。

**DG-2：DDD 边界门（001 结论）**
W0–W10 全部落 `kernel/` `runtime/` `pkg/` `tools/`（框架层），**严禁**借机在 kernel/runtime 引入"领域服务/聚合"。每 Wave 实施计划须自证不违反 001 §关键边界。

---

## 1. Wave 编排（依赖序，来源 004 §落地优先级）

每行的「内容/收益」是 004 的结论指针，不复述；本表只补充**立项门 + 依赖 + scope 状态**。

| Wave | 缺口 | 依赖 | scope 状态 | 立项门 |
|:-:|---|---|---|---|
| **W0** | 10/11/23 — 扩 `outbox.Entry.Headers` + 定义 wire envelope schema | 无 | **scope 已知**（004 §强枢纽节点已定字段族：Principal / Correlation / Time-Causality） | DG-0=做广 → 即可立项；envelope 字段集是 Hard funnel 候选（codegen 单源派生） |
| **W1** | 5 After-Commit Hook · 7 Health Dependency · 11 观测反查 | W0 | scope 已知（004 给设计骨架 + archtest 白名单要求） | After-Commit 必须 archtest 限制只允许 transient 副作用（004 明列陷阱）；评级前置 |
| **W2** | 1 HTTP Idempotency middleware + RecordedResponse Store | W1（缺口 5） | scope 已知 | Store 复用 Redis KeyNamespace（已有 `REDIS-KEY-NAMESPACE-01`）；中间件位序需 ADR |
| **W3** | 4 Command Bus + codegen + outbox/idempotency 协同 | W2（缺口 1） | scope 已知 | **双向锁评级**：codegen 派生 typed Command/Handler（上游 Hard）+ dispatch 调用点（下游 Hard） |
| **W4** | 3 In-Process Contract + assembly 拓扑感知 transport | codegen 扩展 | scope 已知 | 与现有 contractgen 单源对齐；transport bind 由 assembly.yaml 决定，不得 runtime fallback |
| **W5** | 6 Schema Registry（codegen hash + runtime check）· 15 Version Skew | W4（codegen 线） | scope 已知 | 「约定→机制」升级，AI-rebust Hard 目标项；004 §总判断点名为系统性升级机会 |
| **W6** | 2 L3 Saga/Workflow Engine | W1–W4 | **部分 scope 未知**（补偿原语 / 超时语义 / 声明 vs 编程式边界 = 设计张力，004 未收敛） | 立项前需独立 ADR 收敛设计张力；**A 类 scope-blocker 风险**，不可凭猜 |
| **W7** | Auth→Audit middleware · 观测反查链路 · 22 AuthZ↔contract | W0/W1 | scope 已知 | 22 是 contract.yaml 加 `authz` 字段 → codegen 派生（双向锁评级） |
| **W8** | 27 Outbound HTTP/Webhook · 26 Drain · 32 Restart-safe | W0 | scope 已知 | outbound 走 outbox kind=outbound-call，复用既有 relay/circuit |
| **W9** | 25 Migration↔Deploy · 20 Secret Rotation 协议 | — | scope 已知 | Migration↔code 兼容矩阵进 CI gate；Rotation 进 `kernel/crypto` Rotatable 接口 |
| **W10** | Projection/Replay runtime | W5+W6 | scope 未知（依赖 W6 设计收敛） | 暂不排期，W6 完成后重评 |
| 后置 | EventBus→WS bridge · 多租户 · 全套 DX 工具链（29/30/33） | 业务驱动 | — | 不进路线，等业务信号（004 §边际收益递减已明确） |

**核心串联线**：W0 → W1 → W2 → W3 → W6 是递进依赖链，前者是后者基础设施。**W0 是最高 ROI 杠杆点**（004：解锁近半数缺口）。

**第二批/第三批余项**（缺口 8/9/12/13/14/16/17/18/19/21/24/28/31/34/35）按 004 §批次性质归类，**不进固定 Wave**，作为 backlog 候选池，由对应 Wave 顺带或业务信号触发时单独立项评估。

---

## 2. 推进纪律

1. **单 Wave = 单实施计划 + 单 ADR**：本文件不替代 Wave 级实施计划。每 Wave 立项另起 `docs/plans/yyyyMMddHHmm-NNN-waveN-*.md` + 对应 ADR。
2. **不留软回退**（memory `feedback_no_soft_fallback`）：W0 envelope / W4 transport bind / W5 schema check 均为单一上下文内部基建，禁止 `${VAR:-default}` 式回退、禁止 double-write 新旧序列、禁止前置 status 探针。
3. **scope 未知项保持休眠**（037r2 A/B 二分纪律）：W6 / W10 在设计张力 ADR 收敛前是 A 类 scope-blocker，**不得凭猜先做**（PR #445 反模式）。
4. **优先级**：本轨道整体让位于 029 关键路径残余与 038 P0/P1 阻塞；reviewer 容量空档穿插推进。
5. **范围完整**（memory `feedback_no_lazy_deferral`）：进入某 Wave 后，与之紧密耦合的小工作纳入同波，不甩 backlog。

---

## 3. 下一步（待用户/architect 裁决，不自动推进）

- [ ] **DG-0 裁决**：做广组合 vs 做深精度（决定本计划是否激活）
- [ ] DG-0=做广 → 起草 **W0 实施计划 + wire envelope ADR**（字段集 codegen 单源 + Hard funnel 评级举证）
- [ ] DG-0=做深 → 本计划转 backlog 候选池，登记 `docs/backlog/` 后归档

> 本计划仅落规划，未做任何代码改动；四份分析文档已原样拷入本目录作为真值源。
