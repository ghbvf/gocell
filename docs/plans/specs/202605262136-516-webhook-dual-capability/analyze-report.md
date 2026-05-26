# Cross-Artifact Consistency Analysis

**Feature**: 516 Webhook 双向能力
**Date**: 2026-05-26
**Artifacts analyzed**: spec.md / plan.md / tasks.md / data-model.md / research.md / contracts/webhook-contract.md / checklists/requirements.md
**Mode**: Non-destructive（only reports；does not modify artifacts）

## Findings Summary

| Severity | Count |
|----------|-------|
| Critical | 0 |
| High | 0 |
| Medium | 2 |
| Low | 3 |
| Info | 4 |

**Overall verdict**: ✅ Artifacts are internally consistent and ready for `/speckit.implement`（或在本任务中：进入 ship 阶段 3-9 / 用户启动实施）。Medium/Low findings 是文档级精修建议，不阻塞实施启动。

---

## 1. Requirements ↔ Plan Mapping

| spec.md FR | plan.md 承载 PR | data-model 实体 | 状态 |
|-----------|---------------|---------------|------|
| FR-001 声明式 receiver 注册 | PR-2（contract schema + cellgen） | Endpoint | ✓ |
| FR-002 签名 / timestamp / 来源 / 防重 一站式校验 | PR-1（signer/verifier）+ PR-3（runtime 编排） | Source / Delivery | ✓ |
| FR-003 重复 delivery_id 幂等 | PR-3（Claimer 集成） | Delivery 状态机 | ✓ |
| FR-004 签名失败 401 不泄密 | PR-1（errcode）+ PR-3（runtime） | — | ✓ |
| FR-005 timestamp 5min 拒绝 | PR-1（verifier） | — | ✓ |
| FR-006 多密钥并存 | PR-1（Source.Secrets 多把） | Source | ✓ |
| FR-007 handler panic 5xx | PR-3（复用 Recovery middleware） | — | ✓ |
| FR-008 声明式 dispatcher 注册 | PR-2（cellgen） | DispatchTarget | ✓ |
| FR-009 SSRF CIDR | PR-4 | — | ✓ |
| FR-010 DNS rebinding 二次校验 | PR-4 | — | ✓ |
| FR-011 拒绝 redirect | PR-4 | — | ✓ |
| FR-012 即时签名 + 密钥不入 outbox | PR-5（dispatcher 现算签名） | — | ✓ |
| FR-013 5xx 重试 / 4xx 转死信 | PR-5（retry + HandleResult 映射） | RetrySchedule | ✓ |
| FR-014 超时 + body 上限 | PR-5（http.Client.Timeout + io.LimitReader） | — | ✓ |
| FR-015 健康探针 | PR-6（healthz probes） | — | ✓ |
| FR-016 metrics 多维 | PR-6（OTel metrics） | — | ✓ |
| FR-017 密钥不进任何出口 | PR-1（redaction key 扩列）+ PR-6（audit 出口二次脱敏） | — | ✓ |
| FR-018 archtest 闭环 | PR-1/4/5/6 分散 archtest 5 条 | — | ✓ |
| FR-019 webhook contract schema | PR-2 | — | ✓ |
| FR-020 v1 wire 直接演化 | 章程 + 项目宪法既定 | — | ✓（无需新代码） |

**结论**: 20/20 FR 全部有 PR 承载，无悬空需求。

## 2. User Story ↔ Tasks Coverage

| User Story | Acceptance Scenarios | Tasks 覆盖 | 状态 |
|-----------|-------------------|-----------|------|
| US1 5 scenarios | T026 + T027 + T028（unit + integration httptest 覆盖 5 路径） | T030/T031/T032 实现支持 | ✓ |
| US2 5 scenarios | T034-T039（SSRF + dispatcher table-driven + httptest fake target） | T040-T046 实现支持 | ✓ |
| US3 5 scenarios | T049-T050（metrics + archtest）+ 部分由 PR-1/4/5 内 archtest 覆盖 | T051-T057 实现支持 | ✓ |

**结论**: 15/15 Acceptance Scenarios 有对应 task 覆盖。

## 3. Success Criteria ↔ Verification Hooks

| SC | 验证手段 | Task | 风险 |
|----|---------|------|------|
| SC-001 receiver 配置 ≤ 10 行 + 1 handler | cellgen fixture（T023） | T023 | 低 |
| SC-002 dispatcher 配置 ≤ 10 行 + 1 selector | cellgen fixture（T024） | T024 | 低 |
| SC-003 篡改 / replay 100% 拒绝 | integration test（T028） | T028 | 低 |
| SC-004 SSRF 100% 拒绝 | table-driven（T034）+ integration（T039） | T034/T039 | 低 |
| SC-005 receiver p99 < 5ms 额外开销 | **Medium-1**: 当前 tasks 无独立 benchmark 任务 | — | **Medium** |
| SC-006 重试最大间隔 ≤ 10h | retry_test（T038） | T038 | 低 |
| SC-007 全失败路径密钥 0 泄漏 | redaction test（T004）+ integration（T028/T039） | 已覆盖 | 低 |
| SC-008 archtest 全绿 / 反向自检触发红 | 各 archtest（T014/T041/T047/T050） | 已覆盖 | 低 |
| SC-009 backlog ✅ + 无 TODO | T056-T058 | 已覆盖 | 低 |

**结论**: 8/9 SC 有任务覆盖。

## 4. Findings

### Medium-1: SC-005 性能预算缺独立 benchmark 任务

**Location**: tasks.md Phase 3 / Phase 6
**Issue**: SC-005 要求 receiver 端到端额外开销 p99 < 5ms，但 tasks.md 未列出独立 benchmark 任务。当前 T028 是 functional integration test，不验证 p99 延迟分布。
**Recommendation**: 在 Phase 6 加 T063：在 `runtime/webhook/receiver_bench_test.go` 写 `BenchmarkReceiverHotPath`（含 HMAC verify + Claimer InMem hit + handler noop），CI 不 gate（避免抖动），但 PR-6 验收时人工读取 b.N / ns/op 与 SC-005 对照记录于 closeout 文档。

### Medium-2: spec.md US3 scenario 3 与 ADR 关系不显式

**Location**: spec.md US3 acceptance scenario 3（"看不到任何密钥/签名值"）
**Issue**: 该 scenario 的"看不到"范围（log / span / trace event / audit / metric）在 plan.md 中分散在多处复用既有 ADR（observability §Audit Payload Redaction / Span Error Redaction / Span Attribute Redaction），但 spec.md 自身未引用具体 ADR；新人读 spec 时需要交叉对照多份 ADR 才能确认范围。
**Recommendation**: 在 spec.md US3 scenario 3 加一行 "（覆盖 log / span attribute / trace event / audit 出口；范围沿用现有四通道 ADR）"，避免业务读者误解为仅覆盖 log。**注**：因 spec.md 应 technology-agnostic，"ADR" 字样可改为 "现有可观测性脱敏边界"。

### Low-1: data-model.md "Open Items" 与 research.md "Open Items" 重复但未交叉引用

**Issue**: data-model.md 未列 Open Items，research.md §"Open Items" 列了 5 项 follow-up。tasks.md T057 引用 5 项。三处一致，但 spec.md 与 plan.md 都说 "5 项 follow-up"，无单一权威源。
**Recommendation**: 把 5 项 follow-up 列表迁到 spec.md Assumptions 节下方新增一节 "Out-of-scope follow-ups"，其他文档引用 spec.md。

### Low-2: PR-1 行数预算边界（~2130）超 2000 仅给出"备选抽出策略"未给量化阈值

**Location**: plan.md / docs/plans/047 PR-1 行数估算
**Issue**: 预算 2130 接近 2000 上限，备选方案是"把部分 verifier table case 抽到 webhooktest"，但未规定何时触发抽出（PR push 前实测 diff 行数）。
**Recommendation**: 在 tasks.md Phase 6 / PR-1 完工前 check 加一行 "Push 前必跑 `git diff --numstat origin/develop | awk '{i+=$1; d+=$2} END{print i+d}'`，>2000 触发抽出"。

### Low-3: contracts/webhook-contract.md `signedStringForm` 字面量与 archtest SIGNED-STRING-FORM-01 之间无显式锚点

**Issue**: contract.yaml 声明 `signedStringForm: "{deliveryID}.{timestamp}.{body}"`，但 archtest `WEBHOOK-SIGNED-STRING-FORM-01` 锁定的是代码侧 `fmt.Sprintf` 模板字面量。两边漂移可能被忽略（contract 改了 schema 没改代码 / 反之）。
**Recommendation**: 把 contract.yaml 该字段加入 archtest 守护（archtest 读取 contract.yaml 与 signer.go 中 `fmt.Sprintf` 模板，二者必须 byte-equal），或在 contract README 标记该字段为 "informational-only"（实际 source of truth 在 code）。

### Info-1: spec.md `Algorithm` 字段在 spec 中未提，data-model.md 列为 Source 字段

**Issue**: spec.md 故意 technology-agnostic 不提 HMAC-SHA256；data-model.md 把 Algorithm 列为 typed const。属预期边界（spec/plan 关注点分离），但读者切换文档时需理解此约定。
**Action**: 不修。

### Info-2: 9 个 speckit skills 中 `speckit-checklist` 未被使用

**Issue**: 本次产出未生成自定义 domain checklist（如 security-checklist / api-design-checklist）。已生成的 `requirements.md` 是 spec quality checklist（speckit-specify 自带产物），不等同于 speckit-checklist 的输出。
**Action**: 不必额外生成；安全约束已由 archtest invariants 与 ADR 覆盖（强于人工 checklist）。

### Info-3: speckit-clarify 流程未独立执行

**Issue**: 通常 spec-kit 流程在 specify 后 clarify。本次 spec.md 内已无 [NEEDS CLARIFICATION] 标记（由 ship AskUserQuestion 提前解决），故 clarify 步骤合理跳过。
**Action**: 不必补。

### Info-4: 与 ship 阶段产出 047 plan 的对照关系

**Issue**: spec-kit 产出（specs/516）与 ship 产出（docs/plans/047）维度不同但内容重叠：
- 047 是 PR-by-PR 文件清单 + 行数预算（工程视角）
- 516 是 user-story 优先级 + Acceptance Scenarios（产品视角 + spec-driven 视角）

两者互为对照，共同构成完整制品。
**Action**: 已在两边互相 cross-reference（516/plan.md 顶部 "Sibling: 047 plan" / 047 plan 未引用 516——可在 047 plan 补一行链接 Low-4）。

### Low-4: 047 plan 未反向引用 specs/516 spec-kit 制品

**Issue**: 047 plan 是先于 spec-kit 流程产出的（ship 阶段 2 输出），故未引用 516。两份计划文档目前是单向引用关系。
**Recommendation**: 在 047 plan §"References" 节末尾加一行 `- specs/516-webhook-dual-capability/ — spec-kit 视角的 user-story 拆分与 quality checklist`，建立双向链接。

## 5. Constitution Alignment Check

逐条对照 `.specify/memory/constitution.md`（项目宪法）与 CLAUDE.md：

| 章程条款 | 制品对齐 | 状态 |
|---------|---------|------|
| 分层结构（kernel / runtime / contracts / tools / pkg） | plan.md "Source Code" 节路径完全符合 | ✓ |
| Cell 间只通过 contract 通信 | webhook contract kind 设计正确 | ✓ |
| 一致性等级 L0-L4 | Receiver = L1 / Dispatcher = L2，与既有 outbox 一致 | ✓ |
| AI-robust 三档分级 | 5 条 archtest 全部 ≥ Medium，0 个 Soft | ✓ |
| 错误处理 / errcode | 12 sentinel 全 const literal message | ✓ |
| 可观测性 / redaction | sensitive key 扩列 + ADR 引用既有四通道 | ✓ |
| API 版本策略 | webhook contract v1 演化对齐 | ✓ |
| 契约扇出闭环 | implementation matrix 在 PR body 强制 | ✓ |
| 文档命名 | ADR 用 `yyyyMMddHHmm-编号-` 格式 | ✓ |
| 参考框架 commit ref | 8 项 ref 候选 | ✓ |
| Sandbox 提权 | 不触及 | N/A |
| Skill / Agent PR 纪律 | 不修改 .claude/skills 或 .claude/agents | N/A |

**结论**: 章程对齐 12/12，无违规。

## 6. Recommended Next Actions

| 优先 | Action |
|------|--------|
| 立即 | Medium-1: 加 benchmark 任务 T063 到 tasks.md |
| 立即 | Medium-2: 调整 spec.md US3 scenario 3 范围注释 |
| 实施前 | Low-1/Low-2/Low-3/Low-4: 文档微调（10 分钟内可完成）|
| 实施时 | 按 plan.md Phase 顺序启动 PR-1（可直接进入 ship 阶段 3 worktree → 阶段 4 TDD）|
| 关闭 | 全部 PR 合入后，按 tasks.md T056-T062 完成 closeout |

## 7. Coverage Matrix（spec FR / SC ↔ tasks ↔ PR）

完整矩阵在 tasks.md 各 Phase 标题已隐含表达；本节只示意 Critical FR 的双向追溯：

```
FR-002 (签名/时间/防重一站式校验)
  ├─ Tests:   T013 (verifier_test) + T026 (conformance) + T028 (integration)
  └─ Impl:    T008 (verifier.go) + T030 (receiver.go)
       └─ Acceptance: US1 scenarios 1,2,3,4,5
            └─ SC: SC-003 (100% reject 篡改/replay)

FR-009 (SSRF CIDR)
  ├─ Tests:   T034 (table-driven) + T036 (fixture cross-check) + T039 (integration)
  └─ Impl:    T040 (ssrf.go) + T041 (archtest)
       └─ Acceptance: US2 scenario 2
            └─ SC: SC-004 (100% reject 内网)

FR-018 (archtest 闭环 — 防绕过)
  ├─ Impl:    T014 (HMAC funnel) + T041 (SSRF guard) + T047 (Signer funnel + Signed-string form) + T050 (Idempotency Claimer)
       └─ Acceptance: US3 scenario 4
            └─ SC: SC-008 (archtest 全绿 + 反向自检触发红)
```
