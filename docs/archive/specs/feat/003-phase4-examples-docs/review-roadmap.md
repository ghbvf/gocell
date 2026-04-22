# Roadmap Review Report -- Phase 4: Examples + Documentation

## Reviewer: Roadmap Planner
## Date: 2026-04-06
## Branch: `feat/003-phase4-examples-docs`
## Input:
>   - specs/feat/003-phase4-examples-docs/spec.md
>   - specs/feat/003-phase4-examples-docs/phase-charter.md
>   - docs/product/roadmap/202604050853-000-gocell框架补充计划.md
>   - docs/design/master-plan.md
>   - specs/feat/002-phase3-adapters/tech-debt.md (12 items)
>   - specs/feat/002-phase3-adapters/kernel-review-report.md (3 must-fix)
>   - specs/feat/002-phase3-adapters/product-review-report.md (3 must-fix)
>   - docs/design/capability-inventory.md

---

## Executive Summary

Phase 4 是 GoCell v1.0 的最终 Phase（Days 78-91）。spec.md 定义了 10 个 FR，覆盖 3 个示例项目、README/模板、tech-debt 关闭、CI 工作流和文档完善。整体方向与 PRD 和 roadmap 高度对齐。本次审查识别出 8 条建议，其中 2 条 P0（阻塞级）、3 条 P1（重要）、3 条 P2（建议）。

核心判断：Phase 4 的范围在技术债务关闭和示例/文档交付两大主线之间取得了合理平衡，但存在以下风险：(1) master-plan 承诺的 7 个 kernel 子模块在 Phase 0-3 未实现且 Phase 4 未提及，需显式记录为 v1.0 scope cut 或 v1.1 延迟；(2) testcontainers 集成测试与 3 个示例项目同处一个 Phase，工作量叠加存在交付风险；(3) VictoriaMetrics adapter 从 master-plan 一等适配器降至 DEFERRED 但缺少正式的 roadmap 回灌记录。

---

## Findings

### R-01: master-plan 承诺的 7 个 Kernel 子模块未实现，Phase 4 未提及，需显式 scope cut 声明

**Priority**: P0
**Category**: `[依赖缺失]` + `[优先级质疑]`

**问题描述**:

master-plan (Section 5.1 Layer 1: Kernel) 定义了以下 kernel 子模块作为 v1.0 内容（Phase 1 Week 3 交付），但截至 Phase 3 完成后均未实现，Phase 4 spec.md 也未将其列入 In Scope 或 Out of Scope：

| 子模块 | master-plan 描述 | 当前状态 |
|--------|-----------------|---------|
| `kernel/webhook/` | receiver（幂等+签名验证）+ dispatcher（outbox+重试） | 未实现 |
| `kernel/reconcile/` | 最终状态收敛运行时 | 未实现 |
| `kernel/replay/` | projection rebuild + checkpoint | 未实现 |
| `kernel/rollback/` | rollback metadata + kill switch | 未实现 |
| `kernel/consumed/` | consumed marker 显式化 | 未实现 |
| `kernel/trace/` | caller trace 三层 | 未实现 |
| `kernel/wrapper/` | traced sync/event/command wrapper | 未实现 |

同时 runtime 层也有缺失：

| 子模块 | master-plan 描述 | 当前状态 |
|--------|-----------------|---------|
| `runtime/scheduler/` | Cron/定时任务 | 未实现 |
| `runtime/retry/` | retry / timeout / backoff 独立运行时 | 未实现 |
| `runtime/security/tls/` | TLS / mTLS hook | 未实现 |
| `runtime/security/keymanager/` | 密钥管理接口 | 未实现 |

master-plan Section 10 定义 v1.0 = "Kernel(45+ 项能力) + 3 Cell + 8 Journey + 5 一等 adapter + 2 正式 adapter + 3 examples"。当前 kernel 能力清单（capability-inventory.md）记录 11 个 kernel 包，而 master-plan 定义了 18 个。缺口 7 个包意味着 v1.0 scope 已经隐性缩减约 39%，但没有正式的 scope cut 决策记录。

**PRD 对齐分析**:

master-plan 明确将 webhook/reconcile/rollback 标注为 "v2 新增" 并安排在 Phase 1 Week 3。Section 14 的定位声明中将 "outbox/replay/reconcile + verify + journey/status + webhook/feature-flags" 列为 GoCell 的核心差异化。如果 v1.0 缺少这些能力，需要修改 master-plan 的 v1.0/v1.1 边界定义。

**建议修改方式**:

1. 在 phase-charter.md 的 "Out of Scope" 表中显式列出这 7+4 个子模块，标注延迟理由（例如："Phase 1-3 实施中优先保障 core path，高级 kernel 子模块延迟至 v1.1"）
2. 在 spec.md FR-9（文档完善）中增加一个子模块 FR-9.4：更新 master-plan Section 10 的 v1.0/v1.1 边界表，将实际 scope 与计划 scope 的差异正式记录
3. 在 capability-inventory.md Phase 4 更新时，增加 "v1.0 Scope Cut" 附录，列明未交付能力及延迟版本

---

### R-02: VictoriaMetrics 从一等适配器降级缺少正式 roadmap 回灌

**Priority**: P1
**Category**: `[范围蔓延]`（反向：范围缩减未记录）

**问题描述**:

master-plan Section 5 Layer 4 将 VictoriaMetrics 列为 5 个一等适配器之一（与 PostgreSQL/Redis/OIDC/S3 并列）。Phase 3 用 InMemoryCollector 替代，phase-charter.md 仅在 Out of Scope 表中简单记录 "延迟至 v1.1"。但 roadmap 文档（202604050853-000-gocell框架补充计划.md）的 Phase 3 部分仍然写着 "Week 9 -- First-class Adapters: PostgreSQL / Redis / OIDC / S3 / VictoriaMetrics"，未更新。

**PRD 对齐分析**:

master-plan Section 7 专门有时序与可观测章节，推荐 VictoriaMetrics 作为时序存储（"70x 压缩，push+pull"）。Grafana dashboard 模板（FR-5.6）定义了 "outbox lag 面板、HTTP 延迟面板"，但没有 VictoriaMetrics adapter 作为数据源，dashboard 模板将无法连接真实数据。这造成模板与实际能力之间的断裂。

**建议修改方式**:

1. 更新 roadmap 文档 Phase 3 部分，将 VictoriaMetrics 从 Week 9 移除，标注 "DEFERRED to v1.1, replaced by InMemoryCollector in v1.0"
2. FR-5.6 Grafana dashboard 模板的描述中增加说明：数据源使用 Prometheus 兼容接口（InMemoryCollector 或未来 VictoriaMetrics adapter），v1.0 不包含时序存储 adapter
3. 在 master-plan Section 10 v1.0 行中将 "5 一等 adapter" 修正为 "4 一等 adapter + InMemoryCollector"

---

### R-03: Optional adapter 接口桩延迟决策合理，但 roadmap 回灌缺失

**Priority**: P2
**Category**: `[范围蔓延]`（反向）

**问题描述**:

roadmap Phase 4 Week 12 明确列出 "Optional adapter 接口留桩（MySQL / Kafka / SSE / gRPC / search / notification / tenant）"。phase-charter.md 将其列为 Out of Scope 并给出理由 "空接口无评估价值"。这个决策是合理的。

但 master-plan Section 5 Layer 6 定义了 8 个 Optional Adapter 目录结构，Section 10 将其列在 v1.1。roadmap 文档的 Phase 4 部分仍然包含 "Optional adapter 接口留桩"，未标注延迟。

**PRD 对齐分析**:

延迟决策本身与 PRD 不冲突（master-plan 已将 optional adapter 的实现放在 v1.1），但接口桩是 Phase 4 spec 的 roadmap 承诺，需要正式记录延迟。

**建议修改方式**:

1. 在 roadmap 文档 Phase 4 Week 12 中标注 "Optional adapter 接口留桩: DEFERRED -- 空接口无评估价值，延迟至有真实需求时"
2. 无需修改 spec.md（已在 Out of Scope 表中处理）

---

### R-04: WinMDM POC 集成延迟合理，但应在 Phase 结束时做 roadmap 回灌

**Priority**: P2
**Category**: `[范围蔓延]`（反向）

**问题描述**:

roadmap Phase 4 Week 12 包含 "WinMDM 引用 GoCell 的 POC"。phase-charter.md 将其列为 Out of Scope（"外部项目关注点"）。这个决策合理。

但 master-plan Section 12 "代价" 中提到 "两仓库维护 -- gocell 和 WinMDM 版本同步"，且 Phase 2 完成（Day 63）即可并行 WinMDM 集成。Phase 4 结束意味着 GoCell v1.0 完成，此时应明确 WinMDM 集成的后续路径。

**PRD 对齐分析**:

master-plan 将 WinMDM 集成定位为 "Phase 2 完成即可并行"，但实际上 Phase 2 和 Phase 3 都未启动。Phase 4 结束后需要明确：WinMDM 集成是 v1.0 GA 后的独立工作流，还是 v1.1 的一部分。

**建议修改方式**:

1. 在 FR-9（文档完善）中增加 FR-9.4 或在 Phase 关闭报告中明确：WinMDM 集成不在 GoCell v1.0 范围内，建议在 GoCell v1.0 发布后由 WinMDM 团队按需引用
2. 不需要在 Phase 4 spec 中增加任何实施工作

---

### R-05: testcontainers 集成测试 (FR-6) 与 3 个示例项目同 Phase 交付，工作量叠加风险

**Priority**: P0
**Category**: `[优先级质疑]`

**问题描述**:

Phase 4 的 14 天（Days 78-91）需要同时交付：
- 3 个完整的端到端示例项目（FR-1/2/3），每个含 main.go + docker-compose + README + curl 文档
- testcontainers 集成测试全套（FR-6），含 postgres/redis/rabbitmq + 全链路 outbox 测试
- RS256 安全迁移（FR-7），涉及 runtime/auth + access-core 3 个 slice 修改
- outboxWriter fail-fast（FR-7.3），涉及 Cell.Init 改造
- CI workflow（FR-8）
- README + 6 个模板（FR-4/5）
- 文档更新（FR-9）

testcontainers 集成测试（FR-6）本身是 Phase 3 的核心承诺，因 Docker 环境限制延迟至 Phase 4。这意味着 Phase 4 实际上在做 "Phase 3 的验证补全 + Phase 4 的新交付"。Phase 3 kernel-review-report 标注了 CONDITIONAL PASS，条件就是 "集成测试验证缺口必须在 Phase 4 早期闭合"。

按 roadmap 的时间分配（14 天），3 个示例项目每个至少需要 2-3 天（含接线、测试、文档），总计 6-9 天。testcontainers + RS256 + outbox fail-fast 至少需要 3-4 天。CI + README + 模板至少 2-3 天。叠加后已超出 14 天。

**PRD 对齐分析**:

roadmap 原计划 Phase 4 只有 "Examples + 文档 + Optional 接口"，不包含 testcontainers 集成测试和安全加固。这些是 Phase 3 延迟到 Phase 4 的额外负载。如果 Phase 4 因超载而 cut scope，最可能被牺牲的是 IoT-Device 示例（P2 优先级）或项目模板（P3 优先级），这会削弱 Phase 4 的"完整框架产品"目标。

**建议修改方式**:

1. 在 spec.md 或 phase-charter.md 中明确 **Wave 优先级**：
   - Wave 1（必须先完成）: FR-6 testcontainers + FR-7 安全加固 + FR-7.3 outbox fail-fast -- 这是 Phase 3 CONDITIONAL PASS 的闭合条件
   - Wave 2（核心交付）: FR-2 todo-order + FR-4 README -- 这是 Phase 4 Gate 的直接验证路径
   - Wave 3（完整交付）: FR-1 sso-bff + FR-3 iot-device + FR-5 模板 + FR-8 CI + FR-9 文档
2. 明确当 14 天不足时的降级策略：iot-device 可简化为 skeleton + README（不含完整 WebSocket 集成），模板可减少为 3 个核心模板（ADR + cell-design + runbook），Grafana dashboard 模板可延迟
3. 考虑将 FR-6.5（Outbox 全链路测试）标注为 Wave 1 中的最高优先级单项，因为它是 Phase 3 三个 NOT_VERIFIED 成功标准（S1/S2/S3）的闭合点

---

### R-06: Phase 4 Gate 定义与 PRD Section 8 Phase 4 Gate 一致，但验证可操作性不足

**Priority**: P1
**Category**: `[依赖缺失]`

**问题描述**:

Phase 4 Gate 定义为："一个未接触过 GoCell 的 Go 开发者按 README 指引在 30 分钟内创建第一个 cell + slice + journey 并跑通。"

这与 master-plan Section 8 Phase 4 Gate 完全一致（"新项目 30 分钟内创建第一个 cell + slice + journey 并跑通"）。但该 Gate 的验证存在两个问题：

1. **独立验证困难**: 成功标准 SC-1 的验证方式标注为 "手动验证"。在自动化 CI 流水线（FR-8）中无法自动执行 30 分钟主观体验测试。Phase 4 结束时谁来扮演 "未接触过 GoCell 的 Go 开发者"？
2. **Gate 依赖链过长**: 30 分钟教程（FR-4.5）依赖 todo-order 示例（FR-2）可运行，todo-order 依赖 docker-compose（FR-8.2）健康，docker-compose 依赖 testcontainers 验证（FR-6）确认基础设施可用。任一环节断裂都会导致 Gate 失败。

**PRD 对齐分析**:

master-plan 的 Gate 定义本身是合理的产品验收标准。问题在于执行层面需要补充可量化的代理指标。

**建议修改方式**:

1. 增加 Gate 的自动化代理指标：
   - `cd examples/todo-order && docker compose up -d && go run . &` 在 CI 中可自动执行
   - `curl -X POST localhost:PORT/api/v1/orders -d '{"item":"test"}'` 返回 201
   - `gocell scaffold cell --id=test-cell --type=core --level=L1 --team=test && go build ./...` 编译通过
2. 将 30 分钟主观验证保留为 "产品验收"（user-signoff 阶段），但 Gate PASS/FAIL 判断基于自动化代理指标

---

### R-07: FR-1 sso-bff 示例与内建 access-core 的 HS256/RS256 过渡状态可能导致演示混乱

**Priority**: P1
**Category**: `[依赖缺失]`

**问题描述**:

FR-1（sso-bff 示例）演示 access-core 的完整登录流程，包括 JWT 签发。FR-7.1/7.2 要求 RS256 默认化并修改 access-core 三个 slice。这两个 FR 存在时序依赖：

- 如果 FR-7（RS256 迁移）先完成，FR-1 的 sso-bff 示例需要配置 RSA key pair 才能启动。docker-compose.yml 或启动脚本需要生成/挂载 RSA key pair，增加了示例的复杂度。
- 如果 FR-1 先完成但基于 HS256，之后 FR-7 将 access-core 改为 RS256 default，FR-1 的代码需要回改。

当前 spec.md 未声明 FR-7 和 FR-1 之间的实施顺序约束。

**PRD 对齐分析**:

master-plan 要求 "JWT RS256 钉扎"（Section 5 Layer 3 runtime/auth/jwt 描述为 "RS256 钉扎"），意味着示例项目应展示 RS256 作为最佳实践。如果 sso-bff 示例使用 HS256，会与 master-plan 的安全姿态不一致。

**建议修改方式**:

1. 在 spec.md 中显式声明 FR-7（RS256 迁移）必须在 FR-1（sso-bff 示例）之前完成
2. FR-1.1 的 docker-compose.yml 或 Makefile 中增加 RSA key pair 自动生成步骤（`openssl genrsa` 或内嵌 test key pair）
3. FR-1.7 的 curl 命令文档中说明 JWT 使用 RS256 签发，与框架默认安全配置一致

---

### R-08: 版本标记与 semver 策略未在 Phase 4 中声明

**Priority**: P2
**Category**: `[版本风险]`

**问题描述**:

Phase 4 是 v1.0 的最终 Phase。spec.md 和 phase-charter.md 均未提及：
- Phase 4 完成后是否 tag `v1.0.0`
- FR-7（RS256 默认化 + outbox fail-fast）是否构成 breaking change（相对于 Phase 3 的 API）
- `WithEventBus` 标注 Deprecated（FR-8.3）后的移除时间线

从 semver 角度分析：
- RS256 默认化（FR-7.1）：如果外部代码依赖 HS256 default 行为，切换 default 是 breaking change。但 GoCell 尚未发布 v1.0，所以 pre-release 阶段的行为变更不受 semver 约束。
- outbox fail-fast（FR-7.3）：L2+ Cell 缺少 outboxWriter 时从静默降级变为 fail-fast error，这是行为变更。同样，pre-release 不受约束。
- `WithEventBus` Deprecated：需要在 v1.0 release notes 中说明移除计划。

**PRD 对齐分析**:

master-plan Section 10 定义了 v1.0/v1.1/v2.0 版本边界，但未定义 semver 策略。CLAUDE.md 的 api-versioning 规则定义了 HTTP API 的版本策略（`/api/v1/`），但未覆盖 Go library API 的 semver。

**建议修改方式**:

1. 在 FR-9（文档完善）中增加 FR-9.4 或在 CHANGELOG 中明确：Phase 4 完成后 tag `v1.0.0`
2. 在 CHANGELOG 中记录 breaking changes（相对于 Phase 3）：RS256 默认化、outbox fail-fast
3. 在 `WithEventBus` 的 Deprecated 注释中增加 "Will be removed in v2.0"
4. 建议（非阻塞）：在 README 或 CONTRIBUTING.md 中增加 Go library semver 策略说明

---

## Summary Matrix

| ID | Priority | Category | Subject | Disposition |
|----|----------|----------|---------|-------------|
| R-01 | P0 | `[依赖缺失]` `[优先级质疑]` | 7 个 kernel 子模块未实现需显式 scope cut | 需在 Out of Scope 和文档中正式记录 |
| R-02 | P1 | `[范围蔓延]`（反向） | VictoriaMetrics 降级未回灌 roadmap | 更新 roadmap 文档和 FR-5.6 描述 |
| R-03 | P2 | `[范围蔓延]`（反向） | Optional adapter 接口桩延迟未回灌 roadmap | 更新 roadmap 文档 |
| R-04 | P2 | `[范围蔓延]`（反向） | WinMDM POC 延迟需要 post-v1.0 路径 | Phase 关闭时记录 |
| R-05 | P0 | `[优先级质疑]` | Phase 3 tech-debt + Phase 4 新交付工作量叠加 | 需要 Wave 优先级排序和降级策略 |
| R-06 | P1 | `[依赖缺失]` | Gate 验证缺少自动化代理指标 | 增加 CI 可执行的代理指标 |
| R-07 | P1 | `[依赖缺失]` | RS256 迁移与 sso-bff 示例的时序依赖 | 声明 FR-7 在 FR-1 之前完成 |
| R-08 | P2 | `[版本风险]` | v1.0 tag 和 semver 策略未声明 | 在 CHANGELOG/README 中补充 |

## Roadmap Feedback Backlog

Phase 4 结束时需要回灌 roadmap 的内容：

| Item | Action |
|------|--------|
| master-plan v1.0 scope 实际交付 vs 计划 | 更新 Section 10 v1.0 行，反映 11 kernel 包（非 18 包） |
| VictoriaMetrics adapter | 从一等 adapter 移至 v1.1，Section 5 Layer 4 更新 |
| Optional adapter 接口桩 | 从 Phase 4 移至 v1.1 按需，Section 5 Layer 6 更新 |
| WinMDM 集成 | 标注为 post-v1.0 独立工作流 |
| Phase 3 4 项 DEFERRED tech-debt (#4/#10/#11/#12) | 确认延迟到 v1.1/v2.0 |
| Phase 4 实际交付记录 | capability-inventory.md 最终版更新 |
| v1.0.0 release 清单 | tag + CHANGELOG + release notes |

---

*Report generated by Roadmap Planner based on cross-referencing master-plan.md, roadmap, phase-charter.md, spec.md, Phase 3 tech-debt/review reports, and capability-inventory.md.*
