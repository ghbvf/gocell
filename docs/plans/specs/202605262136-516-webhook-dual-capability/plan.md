# Implementation Plan: Webhook 双向能力（Receiver + Dispatcher）

**Branch**: `516-webhook-dual-capability` | **Date**: 2026-05-26 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `specs/516-webhook-dual-capability/spec.md`
**Sibling**: ship 技能产出的技术拆分 `docs/plans/202605262200-047-kernel-webhook-implementation-plan.md`（与本 plan 互为对照，本 plan 偏 spec-kit user-story 映射，ship plan 偏 PR-by-PR diff 行数与文件清单）

## Summary

接入双向 webhook 能力（接收 + 推送）到 GoCell 框架。**主要技术决策**：放在 `kernel/webhook/`（纯计算 + 接口）+ `runtime/webhook/`（HTTP / outbox 集成），新增 `contracts/webhook/` 契约 kind 与 `tools/contractgen` + `tools/cellgen` 派生支持，使业务 Cell 在 slice.yaml 声明即可，无需手写注册代码。安全侧依赖 HMAC-SHA256 + 5min 双向 timestamp + Claimer 防重 + 自定义 SafeDialContext SSRF 守卫 + 拒绝 redirect。复用 `pkg/errcode` / `pkg/redaction` / `kernel/idempotency` / `kernel/outbox` ConsumerBase。范围切为 6 个 PR，每个 ≤ 2000 行 diff。

## Technical Context

**Language/Version**: Go（仓库当前 toolchain）
**Primary Dependencies**: 标准库（`crypto/hmac`、`crypto/sha256`、`net`、`net/http`）+ `pkg/errcode` + `pkg/redaction` + `kernel/idempotency` + `kernel/outbox` + `kernel/healthz`；外部依赖**零**（kernel/ 层硬约束）
**Storage**: Source 密钥 in-memory `SourceRegistry`（初版）+ `SourceStore` interface（后续可替换 configcore / vault）；出站事件复用现有 outbox（PG store）；幂等键复用 `kernel/idempotency.Claimer`（InMem / Redis）
**Testing**: table-driven unit（kernel/webhook）+ conformance（kernel/webhook/webhooktest）+ HTTP integration（`-tags=integration`，runtime/webhook）+ archtest（`tools/archtest`，5 条 invariants）+ Svix 官方 HMAC test vectors（fixtures/）
**Target Platform**: Linux server（与现有运行时一致）
**Project Type**: Go 库（GoCell 框架内部能力扩展，不引入新 binary）
**Performance Goals**: receiver p99 额外开销 < 5ms（FR-mapping SC-005）；dispatcher 退避重试最大间隔 ≤ 10h（FR-mapping SC-006）
**Constraints**: kernel/ 不依赖 runtime/、adapters/、cells/；kernel/ 覆盖率 ≥ 90%；新增 archtest invariants ≥ Medium 评级（Soft 严禁立项）
**Scale/Scope**: 6 PR / ≈ 9300-10700 行 diff / 3 新 ADR / 5 archtest invariants / 12 errcode sentinel / 6 redaction sensitive key

## Constitution Check

Gate（基于 `.specify/memory/constitution.md` + CLAUDE.md + `.claude/rules/gocell/*.md`）：

| 章程条款 | 检查点 | 状态 |
|---------|--------|------|
| 分层规则 | `kernel/webhook/` 只依赖 stdlib + pkg/ + kernel 内层；`runtime/webhook/` 依赖 kernel + runtime/http | ✓ |
| 一致性等级 | Receiver = L1（本地事务 + Claimer claim）；Dispatcher = L2（outbox publisher，与现有 outbox 一致） | ✓ |
| Cell 间通信 | 业务 Cell 通过 `cell.Registrar` 声明，不直接 import 其他 Cell | ✓ |
| AI-robust | 所有新 archtest invariants 评级 ≥ Medium（双向锁），Soft 不立项 | ✓ |
| 错误处理 | errcode New 用 const literal message；runtime 数据走 WithDetails/WithInternal；新增 12 个 sentinel 经 `errcode.New` | ✓ |
| 可观测性 | 密钥/签名走 IsSensitiveKey；span error 经 RedactError；新增 `webhook_secret` 等 6 key 扩列 | ✓ |
| API 版本 | v1.0 GA 前 wire 契约直接演化；webhook contract 用 `webhook.<source>.<topic>.v1` 命名 | ✓ |
| 契约扇出闭环 | 6 个 PR 的 implementation matrix 在 PR body 强制；conformance test 跨实现一致；governance scan 列出受影响契约 | ✓ |
| 文档命名 | 本 plan 在 spec-kit 路径下；3 新 ADR 用 `yyyyMMddHHmm-编号-` 格式（PR-3/PR-4/PR-5 内落地） | ✓ |
| 参考框架 | commit message 标 `ref: svix/svix-webhooks ...` 等 6 源 | ✓ |
| AI-robust Hard 范本 | 复用现有范本（typed marker funnel / single sanctioned holder / sealed construction），不扩 §Hard 范本目录 | ✓ |

**结论**: 章程门控全部通过；无 Complexity Tracking 需填。

## Project Structure

### Documentation (this feature)

```text
specs/516-webhook-dual-capability/
├── plan.md                     # 本文件
├── spec.md                     # spec-kit user-story 视角的 feature spec
├── tasks.md                    # spec-kit user-story 视角的 tasks 拆分
├── data-model.md               # 关键实体与状态机
├── research.md                 # 对标研究浓缩（ship 探索 → 此处摘要）
├── analyze-report.md           # spec/plan/tasks 一致性分析报告
├── checklists/
│   └── requirements.md         # spec quality checklist（已通过）
└── contracts/
    └── webhook-contract.md     # webhook contract kind 字段说明（schema 草图）
```

### Source Code (repository root)

GoCell 是 Go 单 module 多包仓库，遵循 CLAUDE.md 分层结构。本特性新增/修改路径：

```text
kernel/webhook/                                 # 纯计算：Signer/Verifier/Dispatcher/SSRF/Retry
├── doc.go
├── webhook.go                                  # types: Algorithm/DeliveryID/SourceID/Source/Headers
├── signer.go                                   # sealed Signer + hmacSigner（HMAC funnel 单点）
├── verifier.go                                 # hmacVerifier + 多签名头支持
├── source.go                                   # in-mem SourceRegistry + SourceStore interface
├── dispatcher.go                               # implements outbox.EntryHandler
├── ssrf.go                                     # SafeDialContext + CIDR 黑名单 + redirect deny
├── retry.go                                    # RetrySchedule + DefaultSvixSchedule
└── webhooktest/
    └── conformance.go                          # Signer/Verifier 跨实现一致性 suite

runtime/webhook/                                # HTTP middleware + 注册 funnel
├── doc.go
├── receiver.go
├── middleware.go
├── registry.go
├── probes.go
├── metrics.go
└── dispatch/
    ├── consumer.go                             # outbox consumer 包装（接 ConsumerBase）
    └── registry.go

contracts/webhook/                              # webhook contract kind 包文档
└── README.md

contracts/_schemas/
├── webhook-contract.schema.json                # JSON Schema
└── slice.schema.json                           # 扩 contractUsages[].role enum

tools/contractgen/parser/
├── webhook.go                                  # 解析 webhook kind + role
└── webhook_test.go

tools/cellgen/
├── cellgen.go                                  # 注入 webhook template 进生成流程
├── template/webhook_register.tmpl              # 生成 RegisterWebhookReceiver/Dispatch
└── testdata/
    ├── webhook-receive/                        # fixture
    └── webhook-dispatch/                       # fixture

tools/archtest/
├── webhook_hmac_funnel_test.go                 # WEBHOOK-HMAC-FUNNEL-01
├── webhook_ssrf_guard_test.go                  # WEBHOOK-SSRF-GUARD-01
├── webhook_signer_funnel_test.go               # WEBHOOK-SIGNER-FUNNEL-01 + SIGNED-STRING-FORM-01
├── webhook_idempotency_claimer_test.go         # WEBHOOK-IDEMPOTENCY-CLAIMER-01
└── webhook_yaml_invariants_test.go             # CONTRACT-YAML-WEBHOOK-FIELDS-FROZEN / MARKER-RETIRED

pkg/errcode/errcode.go                          # +12 sentinel（ERR_WEBHOOK_*）
pkg/redaction/redaction.go                      # +6 sensitive key

fixtures/
├── webhook-hmac-vectors.yaml                   # Svix 官方 test vectors
└── webhook-ssrf-deny.yaml                      # CIDR 黑名单 source of truth

docs/architecture/
├── 202605262300-adr-webhook-signing-algorithm.md
├── 202605262330-adr-webhook-ssrf-policy.md
└── 202605270000-adr-webhook-retry-default.md
```

**Structure Decision**: GoCell 是 Go 单 module，使用现有分层（kernel/ + runtime/ + contracts/ + tools/ + pkg/ + fixtures/ + docs/）。本特性不引入新顶层目录；所有新建目录是现有分层下的子包。

## User Story → PR 映射

每条 US 跨多个 PR 实现（因 PR 切片按"分层 + 行数预算"切，US 切片按"独立可交付价值"切，二者维度不同）：

| User Story | 主要承载 PR | 辅助 PR | 价值闭环时点 |
|-----------|-----------|---------|------------|
| **US1** Receiver | PR-1（kernel 内核）+ PR-2（contract/cellgen）+ PR-3（runtime 接入） | — | PR-3 合入后业务 Cell 可独立部署接收 webhook，US1 acceptance scenarios 全部可验证 |
| **US2** Dispatcher | PR-4（SSRF guard）+ PR-5（dispatcher 主体） | PR-1（共享 Signer）+ PR-2（cellgen RegisterWebhookDispatch） | PR-5 合入后业务 Cell 可独立部署向外推送，US2 acceptance scenarios 全部可验证 |
| **US3** Observability | PR-6（probes + metrics + redaction 二次扩列 + backlog closeout） | PR-1（archtest HMAC-FUNNEL）+ PR-4（archtest SSRF-GUARD）+ PR-5（archtest SIGNER-FUNNEL + SIGNED-STRING-FORM） | PR-6 合入后运维仪表盘 + 健康探针 + 全部 archtest 完成；archtest 部分在 PR-1/4/5 已可独立验证 |

**Checkpoint**:
- 完成 PR-1 + PR-2 + PR-3 → US1 MVP 可单独验证（向消费者交付"安全接收 webhook"价值）
- 再完成 PR-4 + PR-5 → US2 增量交付（"安全推送 webhook"）
- 完成 PR-6 → US3 增量交付（"运维可观测 + 治理闭环"）

## 6 PR 切片（行数预算与依赖）

详细文件清单与每条行数预算见姊妹文件 `docs/plans/202605262200-047-kernel-webhook-implementation-plan.md` §3。本节给出 spec-kit 视角的简表：

| PR | spec-kit Phase 映射 | 行数预算 | 依赖 | 主要 FR 覆盖 |
|----|--------------------|---------|------|------------|
| PR-1 Foundation | Phase 1 Setup + Phase 2 Foundational（部分） | ≤ 1900 | 无 | FR-002（签名/时间）/ FR-006（多密钥）/ FR-017（redaction key 扩列）/ FR-018（HMAC funnel archtest） |
| PR-2 Contract + codegen | Phase 2 Foundational（剩余） | ≤ 1700 | PR-1 | FR-001（声明式注册）/ FR-008（dispatcher 声明式）/ FR-019（contract schema） |
| PR-3 Receiver runtime | Phase 3 US1 实现 | ≤ 1900 | PR-1 + PR-2 | FR-001..FR-007 全部 / ADR signing-algorithm |
| PR-4 SSRF guard | Phase 4 US2 实现（基础） | ≤ 1700 | PR-1 | FR-009..FR-011 / FR-018（SSRF archtest）/ ADR ssrf-policy |
| PR-5 Dispatcher | Phase 4 US2 实现（主体） | ≤ 2000 | PR-1 + PR-2 + PR-4 | FR-012..FR-014 / FR-018（SIGNER + SIGNED-STRING-FORM archtest）/ ADR retry-default |
| PR-6 Observability + closeout | Phase 5 US3 + Phase 6 Polish | ≤ 1200 | PR-3 + PR-5 | FR-015..FR-017（最终 polish）/ KERNEL-WEBHOOK-01 closeout |

## Phase 顺序与并行机会

```
Phase 1 Setup (PR-1) ──┐
                       ├─→ Phase 2 Foundational (PR-2) ──┐
                       │                                  │
                       └─→ Phase 4 SSRF guard (PR-4)     │
                              │                           │
                              └─→ Phase 4 US2 主体 ──────┤
                                                          │
                                  Phase 3 US1 (PR-3) ────┤
                                                          │
                                                          └─→ Phase 5 US3 + Polish (PR-6)
```

**并行机会**：
- PR-2 与 PR-4 在 PR-1 合入后可并行启动（无文件交叉）
- PR-3（US1 runtime 接入）与 PR-4（US2 SSRF guard）可部分并行（不互依赖）
- PR-5 与 PR-3 之间无强串行依赖（仅共享 contract / cellgen，那由 PR-2 保证）

**同文件冲突**：经检查无（每个 PR 文件清单不交叉，详见姊妹 ship plan §3）。

## Phase 0 Research

研究已在 ship 技能阶段 1 完成（3 个并行 explorer agent），结果浓缩于 `research.md`：
- 对标：Svix / Stripe / GitHub / Slack / Watermill / Convoy
- 关键决策：HMAC-SHA256 / 5min 双向 / Svix 8 步 retry / 自定义 DialContext + CIDR 黑名单

## Phase 1 Design

**Data Model** → `data-model.md`：5 实体（Source / Endpoint / Delivery / DispatchTarget / RetrySchedule）+ 2 状态机（Delivery: pending/claimed/done/failed; OutboxEntry 复用 existing）

**Contracts** → `contracts/webhook-contract.md`：webhook contract kind 字段集草图（id / version / direction / signature / endpoints / events）

**Quickstart**：不需独立 quickstart.md（消费者接入路径在 spec FR-001/FR-008 已经明确，且 examples/ 中将由 PR-6 加 demo cell）

## Phase 2 Tasks

→ `tasks.md`（按 user story 拆 Phase 1/2/US1/US2/US3/Polish，共 ~60 task，每 task ≤ 1 个文件改动单元）

## Complexity Tracking

无 Constitution Check 违规，本节空。
