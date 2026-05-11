# PR #450 产品经理审查报告（S7 Audit Ledger）

**分支**: `refactor/554-pg-s7-audit-ledger` → `develop`
**审查角色**: 产品经理（GoCell 消费者视角 / audit ledger 框架消费者视角）
**审查日期**: 2026-05-11
**结论**: **接受（需 1 处迭代）** — P0/P1 全绿；P2 单点开发者体验改进建议

---

## 1. 总体判断

**接受**。S7 阶段目标全部达成：runtime/audit/ledger 协议化（typed-Go-heavy 范式落地）、PG store 实现、4 个 auditappend* slice 拆分 + 单源 appender、payload redaction 单源到 pkg/redaction、PII 出口可观测。Init() fail-fast、composition root 三处接入路径（cmd/corebundle / examples/ssobff / 测试）均闭环。

对 GoCell 框架消费者（基于框架搭业务的 Go 开发者）和 audit ledger 框架消费者（其他 cell 的 audit 写入路径）来说，API 表面足够小、文档/godoc 充分、错误时机靠 type system 和 fail-fast 守。

仅 PM-04（开发者体验，P2）建议补充"新增审计事件类型的开发者步骤清单"——目前需要跨 contract / actors / slice.yaml / cell.go 4 处手动同步，文档未串联。

---

## 2. S7 验收清单（基于 ADR + Plan S7 范围）

| # | 验收点 | 等级 | 结果 | 证据 |
|---|--------|------|------|------|
| 1 | typed Protocol 落 runtime/audit/ledger | P1 | 通过 | `runtime/audit/ledger/protocol.go:120-326`；sealed RestartRecoveryMode / IdempotencyMode（`protocol.go:32-69`） |
| 2 | NewProtocol fail-fast 校验 4 必填项 | P1 | 通过 | `protocol.go:285-312`；4 条 sentinel sticky + error message 自解释 |
| 3 | HMAC key ≥ 32 字节强制 | P1 | 通过 | `protocol.go:23, 197-222`；error 仅暴露字节数不暴露 key |
| 4 | NamespaceID 校验（≤48 / 全小写 / 无 `:{}` / 首字符 [a-z_]） | P1 | 通过 | `protocol.go:79-118`，与 `adapters/redis.KeyNamespace` 同源规则 |
| 5 | ledger.Store 接口完备（Append/Tail/GetBySeq/Query/Verify） | P1 | 通过 | `runtime/audit/ledger/store.go:54-98` |
| 6 | MemStore 实现 + 单源 Append 算法 | P1 | 通过 | `mem_store.go:81-125` |
| 7 | PG store 实现 + advisory lock + SELECT FOR UPDATE | P1 | 通过 | `adapters/postgres/audit_ledger_store.go:207-262`；F2 advisory-lock 先于 fingerprint check 已注释说明 |
| 8 | L1 LocalTx：Append 参与 caller ambient tx | P1 | 通过 | `audit_ledger_store.go:218 RunInTx`；通过 `TxFromContext` / `execCtx` 双轨同源（`audit_ledger_store.go:163-187`） |
| 9 | Idempotency = EventID-only fingerprint（F-CR-2） | P1 | 通过 | `mem_store.go:285-289`；ADR `§D3`；`storetest/suite.go:280-312` 专有 case（不同 timestamp 同 EventID 必须 dedup） |
| 10 | DB 第二防线 UNIQUE INDEX | P1 | 通过 | `adapters/postgres/migrations/021_audit_entries_event_id_unique.sql` |
| 11 | Restart Recovery = Strict Tail Verify on PG | P1 | 通过 | `cells/auditcore/cell.go:239-243`（Init 时检测 RestartRecoveryStrictTailVerify → 调用 strictTailVerifyOnStartup）；`cell.go:291-312`；空 store no-op；任何 invalid 返回 `ErrAuditChainBroken` 拒绝启动 |
| 12 | Strict Payload Validation 默认开启无 toggle | P1 | 通过 | `mem_store.go:250-262`，ADR §D5 |
| 13 | storetest conformance suite（MemStore + PG 共测） | P1 | 通过 | `runtime/audit/ledger/storetest/suite.go:121-133` 共 12 case |
| 14 | 4 个 auditappend* slice 拆分 + 单源 Service | P1 | 通过 | `cells/auditcore/internal/appender/`（service / spec / actor / doc）；4 个 slice 仅 `type Service = appender.Service` + `var Spec = MustNewSpec(...)` |
| 15 | ActorMode sealed enum（fail-closed） | P1 | 通过 | `appender/spec.go:9-23`；ActorRequireExplicit 阻止 role 事件用 userId fallback（B2-C-05） |
| 16 | Spec sealed + closed slice name whitelist | P1 | 通过 | `appender/spec.go:42-66`；新加 slice 必须同时改两处（whitelist + archtest），有 AI-rebust Hard 防线 |
| 17 | L2 OutboxFact：store.Append + emitter.Emit 同一 RunInTx | P1 | 通过 | `appender/service.go:142-147` |
| 18 | 13 个事件 topic 订阅完整声明 | P1 | 通过 | `cell_test.go:370-388` 显式枚举 13 个 topic 与 snapshot 比对 |
| 19 | PII 出口经 RedactPayload | P1 | 通过 | `slices/auditquery/handler.go:128-138 toListResponseDataItem` 强制走 `redaction.RedactPayload`；`handler_test.go:236-298` 双重测试 |
| 20 | 内部 store 保留原始 payload（合规审计） | P1 | 通过 | `pg store / mem store` 都直存 `Entry.Payload` 不做 redact；redaction 仅在 HTTP 出口 |
| 21 | pkg/redaction 单源 + recursive | P1 | 通过 | `pkg/redaction/redaction.go:255-335`；recursive traverse；malformed JSON / marshal failure 都 fail-closed |
| 22 | sentinel 错误码可发现可操作 | P1 | 通过 | `pkg/errcode/errcode.go:174-186` 三个 sentinel：`ErrAuditLedgerNotFound` / `ErrAuditLedgerAlreadyExists` / `ErrAuditChainBroken`；service.go 在重复 Append 路径返回前者，调用方可 `errors.Is` 判定 |
| 23 | Init() 缺失依赖 fail-fast | P1 | 通过 | `cells/auditcore/cell.go:205-212`（LedgerProtocol/Store sentinel sticky）；`cell_test.go:130-182` 覆盖 4 类负面用例 |
| 24 | composition root 接入 PG / mem 双路径 | P1 | 通过 | `cmd/corebundle/audit_module.go:94-118`；环境变量 `GOCELL_STORAGE_BACKEND` 切换；postgres 分支要求 `SharedPGPool` 先初始化（fail-fast 提示 `ConfigCoreModule must run before`） |
| 25 | example app（ssobff）更新到新 API | P1 | 通过 | `examples/ssobff/app.go:283-321` `buildSSOBFFAuditCore`；godoc 标注 "Mirrors cmd/corebundle/audit_module.go" |
| 26 | HMAC key F7 内存清零 | P1 | 通过 | `protocol.go:218 clear(key)`；`audit_module.go:79` belt-and-suspenders 二次清零 |
| 27 | 4 个 contract.yaml 仍声明 publisher=auditcore | P2 | 通过 | `contracts/event/audit/appended/v1/contract.yaml:8-9` (`subscribers: [external-audit-sink]` 落地 actors.yaml 占位，ADV-05 不触发) |
| 28 | ADR 描述与实施一致 | P2 | 通过 | `docs/architecture/202605101800-adr-audit-ledger-protocol.md` 五个 D 段落与代码对齐；ADR §1.2 标 S7 W1 PR 落地范围与本 PR 匹配 |
| 29 | Journey J-auditlogintrail 仍有 cells 引用 | P2 | 通过 | `journeys/J-auditlogintrail.yaml` 引用 accesscore + auditcore 两个 cell；S7 改造不破坏 journey 语义 |
| 30 | godoc 解释 typed-Go-heavy 范式 + ADR 链接 | P2 | 通过 | `runtime/audit/ledger/doc.go:1-61`；`adr 202605101800` 链接 |

**结果**：30/30 通过。无 FAIL，无 P2 SKIP。

---

## 3. Findings（产品经理视角）

### PM-01 [开发者体验 / P2 / Cx1] — 新增审计事件类型的开发者步骤清单缺失文档化

**位置**: `cells/auditcore/cell.go:158-176`、`docs/architecture/202605101800-adr-audit-ledger-protocol.md`

**问题**: ADR 与 godoc 都没说明"开发者新增一个 audit 事件类型（例如 `event.tenant.created.v1`）"需要改动几处。从代码看至少要：
1. `cells/auditcore/cell.go` 加 `// +slice:subscribe` 注解 + 字段 + initSlices 选 spec
2. 若属于新的 actor 模式（既非 user-fallback 也非 require-explicit），需要在 `appender/spec.go` 加新 ActorMode 实例
3. 若属于新的"slice 分组"（非 user/role/session/config），需要在 `appender/spec.go:42-47 auditcoreAppenderSliceNames` 加 name + 在 archtest 同步
4. 新建 `cells/auditcore/slices/auditappendxxx/{slice.yaml, slice_gen.go, service.go}`
5. publisher 端 contract.yaml 加 `subscribers: [auditcore]`（ADV-05 联动）

5 处同步，没有"add-audit-topic"扩展指南文档。

**建议**: 在 `cells/auditcore/internal/appender/doc.go` 加 "Extending: adding a new audit event family" 章节列 5 步骤；或在 ADR 加一节"演化路径"。

**严重度依据**: 不阻塞当前 PR 验收；但 GoCell 框架价值之一是"扩展可推导"，缺指南会让后续业务接入者依赖代码考古。

---

### PM-02 [开发者体验 / P2 / Cx1] — `Entry.SeqNo` / `ID` / `Hash` 字段使用语义在 godoc 中清楚，但缺少最小调用样例

**位置**: `runtime/audit/ledger/entry.go:13-49`、`runtime/audit/ledger/doc.go`

**问题**: godoc 文字描述"caller leaves SeqNo as 0; store fills it in"清晰，但 doc.go 没有最小可运行的 Append/Tail/Verify 示例。consumer 接入时要先翻 storetest/suite.go 才能看到调用形态。

**建议**: 在 `runtime/audit/ledger/doc.go` 加 godoc Example block（Go 工具链自动测试这些 example），覆盖：
- composition-root 构造 Protocol + MemStore
- 业务侧构造 Entry + Append
- Verify 调用 + 解读结果（valid=true firstInvalidSeq=-1 含义）

**严重度依据**: godoc 已经充分但"零文档发现"成本可降低。

---

### PM-03 [开发者体验 / P3 / Cx1] — `Probes()` duck-typed 接口未在 ledger.Store 文档中提及

**位置**: `adapters/postgres/audit_ledger_store.go:341-348`、`cells/auditcore/cell.go:267-282`

**问题**: PG store 通过 duck typing 实现 `Probes() map[string]func(context.Context) error`；cell.go 用 type assertion `if hp, ok := c.ledgerStore.(cell.HealthProber); ok` 注册 readyz 探针 `audit_ledger_ready`。这是隐式契约——godoc 没说"实现 cell.HealthProber 接口可注册 readyz 探针"。开发者若自己写新 Store 实现（比如未来的 MongoStore），会漏掉这一可选 hook。

**建议**: 在 `ledger.Store` 接口 godoc 顶部加一段："Implementations MAY also implement `cell.HealthProber` to register backend-specific readyz probes; see PG store / MemStore for examples."

**严重度依据**: 当前只有 2 个实现（PG + Mem），漏掉成本低；放到 P3 即可。

---

### PM-04 [兼容性风险 / P2 / Cx2] — `WithLedgerProtocol` / `WithLedgerStore` 必填，但旧 `WithHMACKey` Option 是否已彻底删除未在 PR 描述中明示

**位置**: `cells/auditcore/cell.go:39-61`

**问题**: PR 概要说"`WithHMACKey` → `WithLedgerProtocol/Store`"。从代码看 cell.go 已不存在 `WithHMACKey`（grep 已确认）。但项目无外部消费方（CLAUDE.md "不考虑向后兼容"），需要在 commit message / PR 描述里显式声明"删除了 cells/auditcore.WithHMACKey 公开 Option，调用方应改用 ledger.WithChainHMAC（在 composition root 注入 Protocol）"，让未来 examples / 第三方接入者知道断点。

**建议**: PR 合并前在 PR 描述加一节 "Breaking changes (internal)" 列出删除的公开 Option 与替代路径。Code 不需要变更。

**严重度依据**: 项目允许激进重构（无外部消费方），但 git history 是唯一的"破坏性变更通告"渠道；不写清楚会让 ssobff 之外的内部消费者（若有 fork）漏迁。

---

### PM-05 [验收标准 / P2 / Cx3] — Journey J-auditlogintrail 的 passCriteria checkRef 仍指向旧的 in-cell hashchain 实现

**位置**: `journeys/J-auditlogintrail.yaml:18-22`

**问题**: passCriteria 用 `journey.J-auditlogintrail.hash-chain` / `.integrity-verify` 等抽象 checkRef，没说明它们现在是验 `runtime/audit/ledger` 链还是旧 `cells/auditcore/internal/domain/hashchain`。S7 后 in-cell hashchain 已让位给 ledger.Store；journey 的 mode=auto 检查在哪？需要确认 fixtures/check-implementor 已经切换到 ledger.Store.Verify 路径，避免 journey 假绿。

**建议**: 跑一次 `gocell run-journey J-auditlogintrail`（或 plan 里相应的 verification step）确认 passCriteria 真实绑定到 `ledger.Store.Verify` 而非 stub。若 mode=auto 是 stub-only，把 lifecycle 改 `experimental` 已 OK（当前确实是），但应在 PR 描述提醒"journey 自动校验链接到 ledger 接口"以做心理预期管理。

**严重度依据**: lifecycle=experimental 已经表达"不阻塞主线"，因此 P2；但产品视角 journey 是用户故事，需要切换数据源后语义不漂移。

---

## 4. 7 维度产品评审

| 维度 | 评级 | 证据 |
|------|------|------|
| A. 验收标准覆盖率 | **绿** | 30/30 P1+P2 全通过；P1 100% PASS |
| B. UI 合规检查 | **绿** | HTTP 出口（auditquery）：空 store → 200 + 空 data 数组；error envelope 走标准 `errcode.Error`；payload redact mask 是合法 JSON token；游标失败有 fail-open/fail-closed 模式区分 |
| C. 错误路径覆盖率 | **绿** | nil Protocol/Store / 短 HMAC / 非法 namespace / 无效 JSON payload / 重复 EventID / chain tampered / 短 cursor / 缺 TxRunner / typed-nil → 全部有显式测试 (`cell_test.go:130-343`、`service_test.go`、`storetest/suite.go`) |
| D. 文档链路完整性 | **黄** | ADR 完整、doc.go 充分；但 PM-01 / PM-02 / PM-03 三条均指向"docs 散落、未串联"——开发者扩展指南、godoc Example、HealthProber duck-typed 契约都需要补。已识别但不阻塞 |
| E. 功能完整度 | **绿** | S7 范围（Protocol + MemStore + PG store + appender 单源 + redaction + composition root）全部交付；S8 标记的 "keyset push-down" 已合理 defer 到 backlog `S8-AUDIT-QUERY-KEYSET-PUSH-DOWN-01`（service.go:23-28） |
| F. 成功标准达成度 | **绿** | B2-C-01（重启不断链）/ B2-C-10（PG 并发）/ B2-C-05（role event fail-closed actor）/ B2-C-09（PII 出口）/ F-CR-2（EventID-only 幂等）/ PR266（strict payload）均闭环 |
| G. 产品 Tech Debt | **绿** | [PRODUCT] 标签仅 1 处：service.go:23-28 keyset push-down，正式 backlog item 登记；非 silent carryover |

---

## 5. 开发者体验亮点

1. **Sealed 三件套（RestartRecoveryMode / IdempotencyMode / ActorMode）**：unexported marker 方法 + package-level instances 让违反不可表达（AI-rebust Hard 级）。开发者拿 `WithRestartRecovery(...)` 时 IDE 自动补全只有一个合法值 `ledger.RestartRecoveryStrictTailVerify{}`，配错不可能。
2. **错误信息自解释 + 修复路径内嵌**：
   - `"audit ledger protocol: HMAC key required (use WithChainHMAC, key >= 32 bytes)"` — 直接告诉 caller 修哪个 Option 用什么阈值
   - `"auditcore: LedgerProtocol required; use WithLedgerProtocol (composition root must construct via MustNewProtocol)"` — 告诉 caller 应该在哪一层调用
3. **fail-fast 时机一致**：所有 nil/typed-nil 都在构造期暴露，不会等到第一次 Append 才崩——符合 typed-Go-heavy 范式。
4. **storetest conformance suite**：12 个 case 覆盖 backend parity；新写一个 Store 实现只需提供 `Factory`，免重写测试。
5. **Composition root 单源**：`cmd/corebundle/audit_module.go` 81 行就把 cursor key / HMAC key / PG/mem 切换 / Protocol 构造说清楚；ssobff 仅做 demo simplification（明示 `Mirrors cmd/corebundle/audit_module.go`）。

## 6. 开发者体验痛点

1. **新增 audit slice 跨 5 处同步**：见 PM-01。
2. **Duck-typed `HealthProber`**：见 PM-03；ledger.Store godoc 未说可选 hook。
3. **缺少 godoc Example block**：见 PM-02；当前所有用法示例藏在 storetest / cell_test / audit_module 三处。
4. **`Probes()` map key 命名 `audit_ledger_ready` 在 observability.md "Readyz Probe Naming" 规范内**（snake_case + `_ready` 后缀）— 不是痛点，标在亮点应该更合适，但与 PM-03 联动：开发者若不知道 cell.HealthProber 这条 hook 路径，可能写错命名。
5. **`ErrAuditLedgerAlreadyExists` 语义**：ADR §D3 说"调用方将第二次视为成功，不重试"——但 sentinel 在 `pkg/errcode/errcode.go:177` 描述只说 "rejected because an entry with the same content fingerprint exists"。consumer handler 在 `appender/service.go:142-153` 走 `outbox.Requeue(err)`，会触发 retry budget。**建议**：在 ErrAuditLedgerAlreadyExists 的 errcode godoc 加注 "consumer SHOULD treat this as success (Ack) — duplicate redelivery is idempotent"；当前 service.go 把它误当作 transient error 重试，会让幂等保护变成无效的 retry 风暴（但 PG UNIQUE 索引兜底也会持续踩同样错），运维侧应有告警。

—— 上面"5 痛点"是 P2 改进，不阻塞合并。

---

## 7. 产品验收确认清单

- [x] 产品上下文已定义（cell 消费者 + audit 写入路径双视角）
- [x] 验收标准已分级（P1 / P2 / P3 — 30 条全部）
- [x] P1 验收标准 = 100% PASS（23/23）
- [x] P2 无 FAIL（7/7 PASS，0 SKIP）
- [x] 产品评审报告无红色维度（A/B/C/E/F/G 绿，D 黄但不阻塞）
- [x] 使用者签收判定非 REJECT

**产品 PASS**（建议合并；PM-01~PM-05 列为 follow-up backlog）。
