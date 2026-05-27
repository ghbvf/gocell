---
description: "Task list for webhook 双向能力 feature implementation"
---

# Tasks: Webhook 双向能力（Receiver + Dispatcher）

**Input**: Design documents from `specs/516-webhook-dual-capability/`
**Prerequisites**: plan.md ✓ / spec.md ✓ / research.md ✓ / data-model.md ✓ / contracts/webhook-contract.md ✓

**Tests**: 全部包含（GoCell 项目宪法硬约束：kernel/ ≥ 90% 覆盖率 / archtest invariants 必跟随）

**Organization**: 按 user story 分组（US1 receiver / US2 dispatcher / US3 observability），每个 US 内部按 Setup → Test → Implementation → Integration 顺序，跨 PR 边界用 PR 标签明示。

## Format: `[ID] [P?] [Story] [PR] Description`

- **[P]**: 不同文件且无依赖，可并行
- **[Story]**: US1 / US2 / US3
- **[PR]**: PR-1 .. PR-6（按 plan.md 切片）

---

## Phase 1: Setup (Shared Infrastructure)

- [ ] T001 [PR-1] 在 `pkg/errcode/errcode.go` 新增 12 个 ERR_WEBHOOK_* sentinel（INVALID_SIGNATURE / TIMESTAMP_EXPIRED / DUPLICATE_DELIVERY / INVALID_HEADER / ALGORITHM_UNSUPPORTED / SOURCE_NOT_FOUND / SSRF_BLOCKED / DELIVERY_FAILED / DELIVERY_TIMEOUT / PERMANENT_FAILURE / BODY_TOO_LARGE / CONFIG_INVALID），message 用 const literal
- [ ] T002 [P] [PR-1] 在 `pkg/errcode/errcode_test.go` 加 12 个 sentinel 的 Kind / HTTPStatus 映射 case
- [ ] T003 [PR-1] 在 `pkg/redaction/redaction.go` `sensitiveKeyPattern` 末尾追加 6 个 key 正则（`webhook[_-]?secret` / `x[_-]?signature` / `x[_-]?hub[_-]?signature` / `x[_-]?webhook[_-]?signature` / `svix[_-]?signature` / `hmac[_-]?key`）
- [ ] T004 [P] [PR-1] 在 `pkg/redaction/redaction_test.go` 加 6 个新 key 的 IsSensitiveKey 命中 + free-form mask case

---

## Phase 2: Foundational — Blocking prerequisites

**Goal**: kernel/webhook 内核 + contract schema + cellgen 派生能力齐备，US1/US2 才可开工

### kernel/webhook 内核（US1 + US2 共用，承载于 PR-1）

- [ ] T005 [PR-1] 创建 `kernel/webhook/doc.go` 包文档（API surface、AI-robust 评级清单、引用 ADR）
- [ ] T006 [PR-1] 创建 `kernel/webhook/webhook.go` typed types（Algorithm const / DeliveryID / SourceID / Source / Headers struct）
- [ ] T007 [PR-1] 创建 `kernel/webhook/signer.go` sealed `Signer` interface + `hmacSigner` 实现（HMAC funnel 单 sanctioned holder）
- [ ] T008 [PR-1] 创建 `kernel/webhook/verifier.go` `hmacVerifier`：raw body / 双向 timestamp 校验 / 常时比较 / 多签名头支持
- [ ] T009 [PR-1] 创建 `kernel/webhook/source.go` in-memory `SourceRegistry` + 可替换 `SourceStore` interface
- [ ] T010 [P] [PR-1] 创建 `fixtures/webhook-hmac-vectors.yaml` Svix 官方 5 + 自构 3 个 test vector
- [ ] T011 [PR-1] 创建 `kernel/webhook/webhook_test.go` typed 类型 / Source 校验 table 测试
- [ ] T012 [P] [PR-1] 创建 `kernel/webhook/signer_test.go` HMAC sign 输出 + sealed 包外不可实现验证（用 archtest 已有 unexported method assert 模板）
- [ ] T013 [P] [PR-1] 创建 `kernel/webhook/verifier_test.go` table-driven：valid / 边界（5min 边界）/ 错误（12 sentinel 全覆盖）/ timing-attack property（多次 wrong sig 响应时间方差小于阈值）
- [ ] T014 [P] [PR-1] 创建 `tools/archtest/webhook_hmac_funnel_test.go` WEBHOOK-HMAC-FUNNEL-01：下游 callsite allowlist（`hmac.New` 限定 signer.go）+ 上游 sealed marker + 5 条反向盲区自检（B1/B2/B3/B5 + alias）

### Contract schema + codegen 派生（US1 + US2 共用，承载于 PR-2）

- [ ] T015 [PR-2] 创建 `contracts/_schemas/webhook-contract.schema.json` JSON Schema
- [ ] T016 [PR-2] 扩 `contracts/_schemas/slice.schema.json` 的 `contractUsages[].role` enum：追加 `webhook-receive` / `webhook-dispatch`，每个 role 必填字段（handler / sourceID / targetSelector）
- [ ] T017 [PR-2] 创建 `contracts/webhook/README.md` 包文档
- [ ] T018 [PR-2] 创建 `tools/contractgen/parser/webhook.go` 解析 webhook kind contract + role
- [ ] T019 [P] [PR-2] 创建 `tools/contractgen/parser/webhook_test.go` 解析正常 / 缺字段 / role 校验
- [ ] T020 [PR-2] 创建 `tools/cellgen/template/webhook_register.tmpl` 生成 `reg.RegisterWebhookReceiver` / `reg.RegisterWebhookDispatch` 调用
- [ ] T021 [PR-2] 修改 `tools/cellgen/cellgen.go` 集成 webhook template 进 `cell_gen.go` 生成流程
- [ ] T022 [P] [PR-2] 创建 `tools/cellgen/cellgen_test.go` 端到端：slice.yaml fixture → cell_gen.go diff 比对
- [ ] T023 [P] [PR-2] 创建 `tools/cellgen/testdata/webhook-receive/` fixture（slice.yaml + expected cell_gen.go）
- [ ] T024 [P] [PR-2] 创建 `tools/cellgen/testdata/webhook-dispatch/` fixture
- [ ] T025 [PR-2] 创建 `tools/archtest/webhook_yaml_invariants_test.go` CONTRACT-YAML-WEBHOOK-FIELDS-FROZEN + WEBHOOK-MARKER-RETIRED

**Checkpoint**: Phase 1 + Phase 2 完成后（PR-1 + PR-2 合入），US1 / US2 实施可并行启动

---

## Phase 3: User Story 1 - Receiver 安全接收外部回调（Priority: P1） 🎯 MVP

**Goal**: 业务 Cell 在 slice.yaml 声明 webhook-receive 后，部署即可安全接收外部回调（HMAC + timestamp + Claimer 三重保护）

**Independent Test**: 单独部署只声明 webhook receiver 的最小 Cell，验证 spec.md US1 的 5 条 Acceptance Scenarios

**承载 PR**: PR-3

### Tests for User Story 1 (TDD：先写后跑必失败)

- [ ] T026 [P] [US1] [PR-3] 创建 `kernel/webhook/webhooktest/conformance.go` Signer/Verifier 跨实现一致性 suite（参考 `kernel/outbox/outboxtest.Features` 模式）
- [ ] T027 [P] [US1] [PR-3] 创建 `runtime/webhook/receiver_test.go` unit：handler 路径分支（成功 / 签名失败 / timestamp 失败 / Claimer 各分支 / body 超大）
- [ ] T028 [P] [US1] [PR-3] 创建 `runtime/webhook/receiver_integration_test.go`（`-tags=integration`）：httptest 全链路 5 Acceptance Scenario（有效 → 200 / 无效签名 → 401 / 重复 delivery → 200 idempotent / 5min 外 → 401 / body 过大 → 413）

### Implementation for User Story 1

- [ ] T029 [US1] [PR-3] 创建 `runtime/webhook/doc.go` 包文档
- [ ] T030 [US1] [PR-3] 创建 `runtime/webhook/receiver.go` HTTP handler：BodyLimit → header 提取 → verifier → claimer 两阶段 → business handler 调用 → Commit/Release
- [ ] T031 [US1] [PR-3] 创建 `runtime/webhook/middleware.go` WebhookVerify middleware（与 Recovery / BodyLimit / Metrics chain 顺序定义）
- [ ] T032 [US1] [PR-3] 创建 `runtime/webhook/registry.go` `RegisterWebhookReceiver(spec, handler)` 与 `cell.Registrar` 接缝
- [ ] T033 [US1] [PR-3] 创建 `docs/architecture/202605262300-adr-webhook-signing-algorithm.md` ADR：HMAC-SHA256 唯一合法 / 拒 SHA-1 降级 / 密钥轮换双 secret 过渡

**Checkpoint**: US1 完成（PR-3 合入）→ MVP 可独立部署交付

---

## Phase 4: User Story 2 - Dispatcher 安全向外推送（Priority: P2）

**Goal**: 业务 Cell 在 slice.yaml 声明 webhook-dispatch 后，outbox 事件自动签名 + SSRF 防御 + 退避重试 + 死信路由

**Independent Test**: 单独部署只声明 dispatcher 的最小 Cell，验证 spec.md US2 的 5 条 Acceptance Scenarios

**承载 PR**: PR-4（SSRF guard）+ PR-5（dispatcher 主体）

### Tests for User Story 2

- [ ] T034 [P] [US2] [PR-4] 创建 `kernel/webhook/ssrf_test.go` table-driven：IPv4/IPv6 全黑名单 CIDR + DNS rebinding（fakeDNSResolver）+ redirect deny + scheme 校验 + WithAllowLoopback opt-in
- [ ] T035 [P] [US2] [PR-4] 创建 `fixtures/webhook-ssrf-deny.yaml` CIDR 黑名单 source of truth
- [ ] T036 [P] [US2] [PR-4] 创建 `kernel/webhook/ssrf_fixtures_test.go` 验证 ssrf.go 内 CIDR 列表与 fixture 双向一致（防漂移）
- [ ] T037 [P] [US2] [PR-5] 创建 `kernel/webhook/dispatcher_test.go` table-driven：retry schedule / 2xx Ack / 4xx Reject / 5xx Requeue / SSRF Reject / signing header inject / body 反向 mask
- [ ] T038 [P] [US2] [PR-5] 创建 `kernel/webhook/retry_test.go` 8 步 schedule + clockmock 验证
- [ ] T039 [P] [US2] [PR-5] 创建 `runtime/webhook/dispatch/consumer_integration_test.go` `httptest` fake target：outbox entry → POST 投递 / 2xx Ack / 5xx requeue / SSRF deny / 签名互验

### Implementation for User Story 2

- [ ] T040 [US2] [PR-4] 创建 `kernel/webhook/ssrf.go` `SafeDialContext`：自定义 net.Dialer，dial 前 + dial 后 IP CIDR 检查 / redirect deny / scheme allowlist / WithAllowLoopback opt-in
- [ ] T041 [US2] [PR-4] 创建 `tools/archtest/webhook_ssrf_guard_test.go` WEBHOOK-SSRF-GUARD-01：Hard 下游（Dispatcher.client field type check）+ Medium 上游（ban http.DefaultClient / net.Dial / net.DialTCP 在 webhook 包）+ 反向 B1/B5
- [ ] T042 [US2] [PR-4] 创建 `docs/architecture/202605262330-adr-webhook-ssrf-policy.md` ADR：CIDR 黑名单 source of truth / DNS rebinding 防御 / redirect 策略 / WithAllowLoopback dev-only / Convoy + safeurl 对照
- [ ] T043 [US2] [PR-5] 创建 `kernel/webhook/dispatcher.go` Dispatcher struct + outbox.EntryHandler impl + signing 注入 + SSRF client 必填依赖
- [ ] T044 [US2] [PR-5] 创建 `kernel/webhook/retry.go` `RetrySchedule` + `DefaultSvixSchedule` + HandleResult 映射（2xx Ack / 4xx Reject / 5xx Requeue / SSRF Reject）
- [ ] T045 [US2] [PR-5] 创建 `runtime/webhook/dispatch/consumer.go` 把 dispatcher 包装为 outbox consumer（接 ConsumerBase / Settlement）
- [ ] T046 [US2] [PR-5] 创建 `runtime/webhook/dispatch/registry.go` `RegisterWebhookDispatch(spec, targetSelector)` 与 `cell.Registrar` 接缝
- [ ] T047 [US2] [PR-5] 创建 `tools/archtest/webhook_signer_funnel_test.go` WEBHOOK-SIGNER-FUNNEL-01 + WEBHOOK-SIGNED-STRING-FORM-01：Medium 双向锁 + golden Svix vector + 反向 B3
- [ ] T048 [US2] [PR-5] 创建 `docs/architecture/202605270000-adr-webhook-retry-default.md` ADR：Svix 8 步固定 / retry budget / DLX 路由 / delivery timeout 30s

**Checkpoint**: US2 完成（PR-4 + PR-5 合入）→ "推送"价值独立交付

---

## Phase 5: User Story 3 - Observability + 治理（Priority: P3）

**Goal**: 运维仪表盘 + 健康探针 + 全部 archtest 治理 + 审计出口二次脱敏 + backlog 关闭

**Independent Test**: 触发各失败路径，验证 spec.md US3 的 5 条 Acceptance Scenarios

**承载 PR**: PR-6（archtest 部分在 PR-1/4/5 已分散落地）

### Tests for User Story 3

- [ ] T049 [P] [US3] [PR-6] 创建 `runtime/webhook/metrics_test.go` metric 注入验证 + label cardinality 上限
- [ ] T050 [P] [US3] [PR-6] 创建 `tools/archtest/webhook_idempotency_claimer_test.go` WEBHOOK-IDEMPOTENCY-CLAIMER-01：Medium，receiver struct 必有 `claimer` 字段且打 `gocell:"required"` tag + B4 反向（无 reflect Claim 调用）

### Implementation for User Story 3

- [ ] T051 [US3] [PR-6] 创建 `kernel/healthz/webhook_probes.go` typed const ProbeName（`webhook_receiver_ready` / `webhook_dispatcher_ready`）
- [ ] T052 [US3] [PR-6] 创建 `runtime/webhook/probes.go` `cell.RegisterWebhookHealthProbes(reg, receiver/dispatcher)` 共享 funnel
- [ ] T053 [US3] [PR-6] 创建 `runtime/webhook/metrics.go` OTel metrics：`webhook_deliveries_total{result,source}` / `webhook_signature_failures_total{source,reason}` / `webhook_delivery_duration_seconds` / `webhook_idempotency_hits_total`
- [ ] T054 [US3] [PR-6] 若 audit 路径会衍生 webhook payload，扩 `cells/auditcore/.../audit_payload.go` 出口经 `pkg/redaction.RedactPayload` 二次脱敏（与 audit ADR 一致）
- [ ] T055 [US3] [PR-6] 在 `examples/ssobff`（或新 demo cell）加一个 webhook receiver slice，演示 cellgen 派生流程
- [ ] T056 [US3] [PR-6] 把 `docs/backlog/20260520/cap-x-cross.md` 中 `KERNEL-WEBHOOK-01` 状态从 🟡 改 ✅
- [ ] T057 [US3] [PR-6] 在 `docs/backlog.md` 登记 5 项 follow-up（Ed25519 / Vault Transit / Source 持久化 / Circuit breaker 全状态机 / W3C Trace Context 注入）
- [ ] T058 [US3] [PR-6] 把 `docs/plans/202605262200-047-kernel-webhook-implementation-plan.md` 移至 `docs/plans/archive/` 加 `-closeout` 后缀
- [ ] T059 [US3] [PR-6] 把 `specs/516-webhook-dual-capability/` 内 spec/plan/tasks 状态从 Draft 改 Completed

**Checkpoint**: US3 完成（PR-6 合入）→ feature 整体 closeout

---

## Phase 6: Polish & Cross-Cutting

- [ ] T060 [PR-6] 全部 PR 合入后跑 `make verify` 全绿 + CI 16-shard archtest 全绿
- [ ] T061 [PR-6] 对照 `specs/516-webhook-dual-capability/checklists/requirements.md` 重跑一次 checklist 验收（全部 [x]）
- [ ] T062 [PR-6] 对照 `specs/516-webhook-dual-capability/spec.md` SC-001..SC-009 全部可量化验证通过（部分如 SC-005 p99 / SC-006 重试间隔需在 PR-6 跑性能 / 端到端 retry 验证）

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 Setup** (T001-T004) ← 无依赖，可与 Phase 2 部分任务并行
- **Phase 2 Foundational** (T005-T025) ← 阻塞 Phase 3/4/5
  - kernel 内核（T005-T014, PR-1） ← 阻塞 Contract codegen（T015-T025, PR-2）的部分 fixture（生成的代码会引用 errcode sentinel）
  - 但 T015-T017（schema）可与 T005-T014 并行
- **Phase 3 US1** (T026-T033) ← 依赖 PR-1 + PR-2 全合入
- **Phase 4 US2** (T034-T048)
  - SSRF (T034-T036, T040-T042, PR-4) ← 仅依赖 PR-1
  - Dispatcher 主体 (T037-T039, T043-T048, PR-5) ← 依赖 PR-1 + PR-2 + PR-4
- **Phase 5 US3** (T049-T059) ← 依赖 PR-3 + PR-5（probe / metrics 引用 receiver / dispatcher 类型）
- **Phase 6 Polish** (T060-T062) ← 依赖前面全部

### User Story Dependencies

- US1 独立可交付（PR-3 合入即可）
- US2 与 US1 完全无业务依赖（不同包路径，不同状态机）
- US3 部分功能依赖 US1/US2 完成（probe / metrics）；治理 archtest 在 US1/US2 PR 内已部分落地

### Parallel Opportunities

- T001-T004 之间全 [P] 可并行
- T015-T017（contract schema）与 T005-T014（kernel 内核）可并行
- 一旦 Phase 2 完成（PR-1 + PR-2 合入），PR-3（US1）+ PR-4（US2 SSRF）可并行启动
- PR-4 与 PR-3 完成后启动 PR-5
- PR-6 在 PR-5 后启动

### Within Each PR

- 测试（含 archtest）MUST 先于实现写并跑 FAIL
- TDD：Tests → Implementation → Integration test
- godoc 与 doc.go 在该 PR 完成时补齐
- 每 PR commit message 标 `Refs: KERNEL-WEBHOOK-01` + `ref: <framework> <file>`

---

## Parallel Example: Phase 2 Foundation

PR-1 内可并行启动 4 个 developer agent（同一 worktree，串行 git commit）：

```
Agent A: T005, T006, T011 (webhook.go + types test)
Agent B: T007, T012 (signer.go + signer_test.go)
Agent C: T008, T013 (verifier.go + verifier_test.go)
Agent D: T009, T010, T014 (source.go + fixture + archtest)
```

PR-2 内：

```
Agent A: T015, T016, T017 (schema + slice schema + README)
Agent B: T018, T019 (contractgen parser + test)
Agent C: T020, T021 (cellgen template + cellgen.go)
Agent D: T022, T023, T024, T025 (cellgen test + fixtures + archtest)
```

---

## Implementation Strategy

### MVP Path（仅 US1 receiver）

完成 Phase 1 → Phase 2 → Phase 3，US1 已可独立交付：业务消费者能安全接收 webhook。
此时 PR 序列 = PR-1 + PR-2 + PR-3（共 ≈ 5500 行 diff），最短交付路径。

### Incremental Delivery

1. PR-1 + PR-2 → Foundation 就绪
2. PR-3 → US1 receiver 交付（"安全接收" MVP）
3. PR-4 + PR-5 → US2 dispatcher 交付（"安全推送"）
4. PR-6 → US3 observability + closeout（"治理 + 仪表盘"）

每个 PR 合入即可独立 demo / 验收，不需等下一个 PR。

### Parallel Team Strategy

发挥并行机会：

- Phase 1 + Phase 2 由两个 developer 并行（PR-1 + PR-2 互不阻塞）
- Phase 3（PR-3）与 Phase 4 的 SSRF 子集（PR-4）并行
- PR-5 与 PR-6 的 archtest 部分可在 PR-5 内并行（不互阻塞）

---

## Notes

- **PR ≤ 2000 行硬约束**：所有 PR 的预算见 plan.md §6 PR 切片表；实际超出时优先把表-driven test case 抽到 `kernel/webhook/webhooktest` conformance 包平摊
- **每 PR 完成前必跑**：`go build -tags=integration ./...` + `golangci-lint run ./...`（0 issues）+ `bash hack/verify-archtest-invariants.sh`
- **AI-robust 评级**：所有新 archtest 评级 ≥ Medium，反向自检盲区清单写进各 archtest godoc
- **不留 follow-up TODO**：5 项已知 follow-up（Ed25519 / KMS / 密钥持久化 / Circuit breaker 全机 / Trace 注入）显式以 gh issue 跟踪，不在代码留 TODO
- **避免**：vague task / same file 跨 agent / cross-PR 依赖破坏独立性 / 试图把 US1+US2+US3 揉进单个 PR
