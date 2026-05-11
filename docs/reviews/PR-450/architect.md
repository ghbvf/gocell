# 架构师审查 — PR #450（S7: runtime/audit/ledger + auditcore 框架消费）

## 总体判断

**needs-changes**（架构总体方向正确，存在 1 个 P0 哈希链正确性风险 + 若干 P1 设计冗余/死代码路径）

新增的 `runtime/audit/ledger` 框架完整贯彻 typed-Go-heavy 范式（sealed marker / Option fail-fast / namespace 校验），分层依赖正确（runtime/ → kernel/+pkg/，adapters/ → 实现 runtime 接口，cells/ → 消费注入），单源 appender + Hard/Medium 双重防线设计扎实。但 PG store 的 `payload JSONB` 列与 byte-for-byte HMAC 输入存在根本性矛盾，且 `auditverify` slice 在生产路径上完全未连线（dead service），这两条必须在合入前解决或显式 backlog。

---

## Findings

### A-01 [契约完整性 / P0 / Cx3] PG store `payload JSONB` 列破坏 hash chain byte-for-byte 等价
**文件**: `adapters/postgres/migrations/020_audit_ledger.sql:39`，`adapters/postgres/audit_ledger_store.go:62-68, 487-516`

**问题**: ADR §D1 明确锁定 HMAC 输入 `msg = prevHash|eventID|eventType|actorID|UnixNano|payload`，其中 `payload = string(entry.Payload)`，要求与 `cells/auditcore/internal/domain/hashchain.go` byte-for-byte 等价。但：

1. migration 020 把 `payload` 列定义为 `JSONB`。PG JSONB 不保留原始字节——它会重新解析、规范化空白、重新排序 object 键。
2. `LedgerStore.Append` 在 line 247 用调用方原始 payload bytes 计算 Hash 并 INSERT。
3. `LedgerStore.Verify` 在 line 487-512 通过 `selectRangeSQL` 读回 `payload` 列（已经被 JSONB 规范化），再用 `protocol.ComputeHash(e.PrevHash, &e)` 重算。

对于多键、含空白、嵌套对象、数字格式存在多种合法表示的真实 outbox payload（如 `cells/auditcore/internal/dto/audit_events.go::AuditAppendedEvent` 之外的上游事件 payload），**Verify 几乎必然返回 valid=false**。这同样会让 `strictTailVerifyOnStartup`（`cells/auditcore/cell.go:291`）在启动时把任何已写入的 PG chain 判定为 broken，**整个 cell 拒绝启动**。

**测试盲点**: `runtime/audit/ledger/storetest/suite.go` 全部 payload 是 `{}` / `{"key":"value"}` / `{}`——这些 JSONB 等价于自身的退化场景，无法触发 bug。`audit_ledger_store_test.go::TestPGVerify_SubRange_*` 用 `NewEntryFixture` 同样落到 `{}`。

**建议修复**（任选其一）：
- **改 column 类型**: migration 020 把 `payload` 改为 `BYTEA`（或 `TEXT`），失去 JSONB 索引/查询能力但保住 hash 等价性；或保留 JSONB 但额外加一个 `payload_raw BYTEA NOT NULL` 列存原始字节用于 Verify。
- **改 hash 算法**: 让 ComputeHash 在写入前对 payload 先做 canonical JSON 规范化（按键排序、去空白），再 hash；Verify 也走同一 canonical 路径。但这与 ADR §D1 byte-for-byte 等价的硬约束冲突，需要新发 ADR 并明确历史 in-cell 数据不可迁移。
- **加测试**: 至少新增一个 storetest case，payload 含两键以上、key 故意倒序（`{"b":1,"a":2}`）、含中间空白，证明 JSONB 路径下 Append→Verify 仍然 valid。

**影响**: 高。这是 PG 路径核心正确性问题，且现有测试覆盖盲点严重。S8 PG 接入此 cell 后任何重启或主动 Verify 都会失败。

---

### A-02 [Cell 聚合边界 / P1 / Cx2] `auditverify` slice 在生产路径完全未连线
**文件**: `cells/auditcore/cell.go:178, 385-396`；`cells/auditcore/slices/auditverify/service.go:77`

**问题**: `auditverify.Service.VerifyChain` 只被自身 *_test.go 调用（`Grep VerifyChain` 全文 8 处全在测试），cell 既未给它注册 HTTP route（无 `+slice:route` 注解、cell_gen.go 无对应 RouteGroup register），也未订阅任何触发事件，也未在启动后通过 cron / job emitter 拉起。换句话说：

1. cell 构造时 `c.verifySvc` 字段被填上（line 394），并 `AddSlice` 记入 cell 元数据；
2. 生产运行时 `verifySvc` 永远收不到任何 invocation；
3. `slice.yaml::contractUsages` 声明的 `event.audit.integrity-verified.v1` publisher 角色因此 **never fires**。

但 contract.yaml `event.audit.integrity-verified.v1` 是 `lifecycle: active`，且 ADV-05 规则要求 active event 有 subscriber——subscriber 是 `external-audit-sink` actor，不在 cell 内可控。生产环境永远不会有这个事件。

**设计后果**:
- contract 声明了一个永远不发的事件，造成「声明 vs 实际」漂移
- L2 OutboxFact 包装的事务只 Emit 不写 store（`service.go:101-103`）——这是 dummy transaction，无 atomicity 价值
- `txRunner` 强制注入但 transaction body 内只跑 outbox.Emit，与 OUTBOX-SERVICE-01 的设计意图（store.write + outbox.Emit 原子性）不符

**建议修复**（任选其一）：
- **裁掉 slice**: 既然没有触发器，删 `auditverify` slice + contract，等真正需要时再恢复（符合 CLAUDE.md「不考虑向后兼容、不造平行结构」）
- **加触发器**: 给 cell 加一个 `/internal/v1/audit/verify` POST 路由（让 ops 主动触发），或让 BaseCell 启动 ticker 周期 verify。slice.yaml 补 `contract.http.audit.verify.v1.serve`
- **改 lifecycle**: contract.yaml `event.audit.integrity-verified.v1` 改为 `lifecycle: draft`，挂 backlog 显式记录「等触发器落地再转 active」

**影响**: 中。当前不影响 build/run，但属于「声明 ≠ 实现」的架构债务，违反 cell-patterns.md ADV-05 精神。

---

### A-03 [一致性级别 / P1 / Cx2] auditverify L2 标记与实际行为不符
**文件**: `cells/auditcore/cell.go:396`；`cells/auditcore/slices/auditverify/service.go:100-105`

**问题**: `cell.AddSlice(cell.NewBaseSlice("auditverify", "auditcore", cell.L2))` 把 slice 标为 L2 OutboxFact，但 `VerifyChain` 的 transaction 内**不写 store**（line 101-103 只 Emit），没有 store.write + outbox.Emit 原子性可言。L2 的语义是「本地事务 + outbox 发布」(`.claude/rules/gocell/go-standards.md`)；这里只有 outbox，没有事务对象写入。

更接近 L0/L1 + outbox：纯查询 + 事件投递。如果保留这个 slice（参见 A-02），应该：
- 若有计算结果落库 → 保 L2（当前 VerifyResult 不落库）
- 若只发事件 → 用 DirectEmitter 路径，slice 标 L1（emit 是本地 broker 投递，无 store 写）；或干脆 L0 + 调用方负责重试

**建议**: 配合 A-02 整改时确定 slice 真实级别；如保留则去掉 RunInTx 包装（dummy transaction 是噪音），明确 L1 + outbox emit。

**影响**: 中。L 级别是 GoCell 一致性契约的核心元数据，错标会误导后续 Cell consumer 的重试/幂等策略。

---

### A-04 [分层架构 / P1 / Cx2] `LedgerStore.Probes()` duck-typing 把 cell.HealthProber 协议外溢到 adapters/
**文件**: `adapters/postgres/audit_ledger_store.go:336-348`

**问题**: 注释明确解释 `Probes` 方法是为了 satisfy `cell.HealthProber` 而不直接 import `kernel/cell`，靠 duck-typing 实现。这虽然规避了 adapters→kernel/cell 的硬依赖，但留下两个架构隐患：

1. **协议外溢**: `kernel/cell.HealthProber` 是 cell-layer 接口，把它的方法签名（`Probes() map[string]func(context.Context) error`）让 adapters 实现，等于把 cell 概念偷渡进 adapters。如果未来 HealthProber 改名或加方法，没有编译期保护（duck-typing 默默断开）。
2. **probe 名硬编**: `audit_ledger_ready` 字符串在 adapters/ 写死，未通过 kernel/cell 的命名注册路径校验（observability.md「Readyz Probe 命名」约束，要求 snake_case + `_ready` 后缀）。当前格式对，但没有编译期/archtest 守卫。

**建议**:
- 在 `kernel/cell` 定义 `type HealthProbeMap = map[string]func(context.Context) error` 类型 alias 并导出 `HealthProber` 接口的类型断言 helper；adapters 通过 import kernel/cell（这条边在分层规则里允许：`adapters/` 实现 `kernel/` 接口）正式 implement 而非 duck-type
- 或者把 probe 注册责任完全留在 cell 一侧：cell 不做 `if hp, ok := c.ledgerStore.(cell.HealthProber)` 探测，而是 cell 内手写 `reg.Health("audit_ledger_ready", func(ctx) { return ledgerStore.Tail(ctx) })` — 没有 duck-typing 协议外溢

**影响**: 中。当前能跑，但抹掉了类型系统的安全网，违反「分层依赖应在编译期表达，不靠协议字符串约定」（AI-collab.md Hard vs Soft 评级）。

---

### A-05 [接口稳定性 / P1 / Cx2] `MemStore.MustTamper*` 测试-only API 出现在生产 export 表面
**文件**: `runtime/audit/ledger/mem_store.go:223-245`

**问题**: `MustTamperEntryHash` / `MustTamperEntryPrevHash` 是 storetest negative case 的测试夹具，但作为 `*MemStore` 的 public method 暴露到 `runtime/audit/ledger` 包的导出 API。任何 cell 拿到 `ledger.MemStore` 都可以调它篡改链；注释只说「intended for storetest」是 godoc 约定（Soft），没有任何编译/类型护栏。

ADR §D5 强调 strict mode「无 toggle」，但 MustTamper 直接绕过整条 hash 验证，等于私下提供 backdoor。

**建议**:
- 把 MemStore 的 tamper 入口放到 `runtime/audit/ledger/internal_test_export.go`（`// +build testexport` 之类的 build tag），或者
- 移到 `runtime/audit/ledger/storetest` 包内通过 unexported field 反射 / 通过新建一个 `MemStoreTestAccess` sealed marker 提供（参考 PR450 自己的 sealed Spec 模式）
- 至少加 archtest `AUDIT-LEDGER-TAMPER-TEST-ONLY-01`：禁止 `MustTamperEntry*` 在非 `_test.go` 文件被调用（Medium）

**影响**: 中。当前没有滥用，但属于「生产 API 表面渗漏测试杂质」，与本 PR 强调的 sealed Spec / sealed ActorMode Hard 防线一致性不匹配——单一标准。

---

### A-06 [接口稳定性 / P1 / Cx1] `ledger.Protocol` 4 个 sentinel bool 字段过度防御
**文件**: `runtime/audit/ledger/protocol.go:125-134`

**问题**: 4 个 `xxxNil bool` 标志位（hmacKeyNil / namespaceNil / restartRecoveryNil / idempotencyNil）+ `NewProtocol` 末尾 4 段 `if p.xxxNil || ...{ return error }` 是 over-engineering：

1. `WithChainHMAC([]byte{})` / `WithNamespace("")` / `WithRestartRecovery(nil)` 等都直接走 fail-fast 即可（option body 内 `return error`），不需要 sticky sentinel 来检测「nil 后又被合法值覆盖」的 fictional 场景。
2. ADR §D5 + cell-patterns.md「构造函数 fail-fast」明确「依赖缺失在 Init() 报错，不降级」——sentinel sticky 解决的是「累加式 builder option」的 nil 哲学（runtime-api.md 表格），但这 4 个 option 都是**强依赖 wiring option** 类，按 runtime-api.md 应直接 phase0 fail-fast，不应模拟 builder noop。

实际效果：sticky sentinel 让一段「正常路径 fail-fast + 容错」的简单代码变成 8 个字段 + 80 行注释解释「为什么 nil 不被后续清掉」。

**建议**:
- 删 4 个 sentinel；Option body 改 `return errcode.New(...)` 直接拒；NewProtocol 末尾改为 `if len(p.hmacKey)==0 { return nil, err }` 4 段即可
- 或者保留 sticky 但归入 runtime-api.md「累加式 builder option」类（accumulator 语义），并把 godoc 强调点从「sticky 检测」改为「累加配置」——但前提是确实有累加用例（目前没有）

**影响**: 低。功能正确，只是不必要复杂度，违反 CLAUDE.md「优雅简洁」+ Go 编码规范「认知复杂度 ≤ 15」精神（NewProtocol 当前 16）。

---

### A-07 [扩展性 / P1 / Cx2] auditquery 的 `auditQueryFetchCap = 5000` 是已知 OOM 兜底但未真正解决
**文件**: `cells/auditcore/slices/auditquery/service.go:29, 83-93`

**问题**: 当前 query 路径：
1. `Service.Query` 用 `ledger.Store.Query(ctx, filters, {Limit: 5000})` 一次性拉 5000 行
2. 然后在内存里做 `query.Sort` + `query.ApplyCursor` 做 keyset 模拟
3. 命中 5000 上限只发 Warn 日志，「results may be incomplete」

这违反 go-standards.md「列表接口强制分页，pageSize 上限 500」（auditQueryFetchCap=5000 是该上限 10×），且 N+1 之外另开了 N+5000 的内存膨胀路径。已有 backlog 项 `S8-AUDIT-QUERY-KEYSET-PUSH-DOWN-01` 跟踪。

**问题升级到架构层面**:
- `ledger.Store.Query` 接口签名（`store.go:88-91`）`QueryListParams{Limit, Offset}` 是**简单 LIMIT/OFFSET**——没有 cursor。MemStore 这样做合理，PG store 也只能这样。即使后续把 keyset 推到 SQL 层，需要修改 `ledger.Store.Query` 签名（Breaking change），影响 MemStore + PG 两个实现。
- 现在签名定型再 backlog 改，是一次 API 演化负债。

**建议**:
- 在本 PR 把 `QueryListParams` 加上 `Cursor *KeysetCursor` optional 字段（MemStore 可忽略，PG 后续实现）— 预留接口扩展空间，符合 api-versioning.md「新增字段 only-additive」规则
- 或者把 `auditQueryFetchCap` 改为 500 跟齐 go-standards.md，避免在生产先暴露 OOM/不完整结果

**影响**: 中。当前 audit_entries 量级未到风险，但 keyset push-down 是 contract API 形态改动，越晚做越贵。

---

### A-08 [分层架构 / P2 / Cx1] `ledger.Protocol.HMACKey()` 导出 defensive copy 鼓励错误使用
**文件**: `runtime/audit/ledger/protocol.go:139-143`

**问题**: `Protocol.HMACKey() []byte` 返回 defensive copy。注释要求「must not be used for logging or error messages」（godoc Soft 约定）。但导出这个 getter 没有任何**正面**用例——`ComputeHash` 已经封装在 Protocol 内部，外部不需要拿到 raw key。

cmd/corebundle 也不调用 `HMACKey()`。grep 全仓只有 protocol_test.go 在用，验证 defensive copy 行为本身。

**建议**: 删 `Protocol.HMACKey()`。Hash 计算完全走 `ComputeHash` 方法，raw key 永远只在 Protocol 内部存在。这是「不可表达」的 Hard 防线——把字段从 export 表面移除等于压根不可能误用。

**影响**: 低。当前没人调，但留 export 表面就是潜在的 PII 出口（即使加注释）。

---

### A-09 [契约完整性 / P2 / Cx1] `event.audit.appended.v1` subscriber 是 external actor 但无 PII 风险声明
**文件**: `contracts/event/audit/appended/v1/contract.yaml:9`；`pkg/redaction/redaction.go:292`（RedactPayload 只在 HTTP 出口生效）

**问题**: `event.audit.appended.v1` 的 subscribers 是 `[external-audit-sink]`（外部 SIEM）。outbox 发出去的事件 payload 包含原始未脱敏的事件 payload（在 `cells/auditcore/internal/dto/audit_events.go::AuditAppendedEvent` 中只有 `auditEntryId + eventType`，**不包含 payload**——这是好设计）。

但 `event.audit.integrity-verified.v1` 同样发到 external-audit-sink，DTO `AuditIntegrityVerifiedEvent` 含 `Valid / FirstInvalidIndex / EntriesChecked`，不含 payload，无 PII 风险。

实际审计 payload 持久化在 `audit_entries.payload` JSONB，**仅在 HTTP `auditquery` 出口**走 `redaction.RedactPayload`（handler.go:137）。事件出口未走 RedactPayload，因为事件不携带 payload，OK。

**建议**: contract.yaml 加注释 / metadata 明确「此事件 payload 不含原始 event payload，无 PII 风险」，或者 codegen 派生检查。当前隐含约定，下个修改 DTO 的人可能加上 payload 字段。

**影响**: 低。当前无问题，是预防性结构化注释。

---

### A-10 [一致性级别 / P2 / Cx1] AUDIT-LEDGER-PROTOCOL-COMPOSITION-ROOT-01 archtest 自评 Medium，但实现含字符串识别 pkg 名
**文件**: `tools/archtest/audit_ledger_composition_root_test.go:75-78`

**问题**: archtest 注释自评 Medium（AST 包路径识别），但实际实现用 `pkg.Name != "ledger"`（line 76）——即如果调用方写 `import auditledger "github.com/ghbvf/gocell/runtime/audit/ledger"; auditledger.NewProtocol(...)`，identifier name 就是 `auditledger`，不会被检测到。这是一个 by-name identifier 检查，AI-rebust 三档应当是 **Soft**（按 ai-collab.md 表，「按方法/包名识别」是 Soft）。

**建议**:
- 升级为 type-aware：用 `go/types` 检查 selector 的解析包是否是 `github.com/ghbvf/gocell/runtime/audit/ledger`（Medium 真正落地）
- 或者在 godoc 自评里诚实降为 Soft + backlog 升级条目（按 ai-collab.md「既有 Soft 按实际事故密度排队升级」）

**影响**: 低。当前编码风格未触发 alias，AI 协作章程要求自评准确。

---

### A-11 [删除合理性 / P2 / Cx1] `adapters/s3/s3.go:125` 仍引用已删除的 `cells/auditcore/s3archive`
**文件**: `adapters/s3/s3.go:124-126`

**问题**: PR 删除了 `cells/auditcore/internal/adapters/s3archive`，但 `adapters/s3/s3.go::Upload` 的 godoc 注释仍然写 "Implements the ObjectUploader interface used by cells/auditcore/s3archive"。dangling doc reference。

**建议**: 更新注释为「used by example consumers」或删掉具体引用；如果 S3 adapter 不再有任何消费者，整体考虑是否把 `adapters/s3/s3.go` 也删（CLAUDE.md「不向后兼容，不留死代码」）。

**影响**: 极低，文档清洁度。

---

## 亮点（值得保留的设计决策）

1. **typed-Go-heavy 范式贯彻彻底**：sealed `RestartRecoveryMode` / `IdempotencyMode` 接口 + 单 var 实例 + 不可外部实现的 unexported marker；ADR §1.2 显式与 session 协议落地路径对齐，typed Protocol 注入 + composition root 集中构造，cells 零知识。

2. **appender 单源 + Hard/Medium 双重防线设计模板级**（`cells/auditcore/internal/appender/`）：type alias to non-local Service 在语言层禁止 method 重定义（Hard） + sealed Spec/ActorMode 不可零值构造（Hard） + AUDITCORE-APPENDER-SINGLE-SOURCE-01 archtest 兜底 abandonment case（Medium）。完全符合 ai-collab.md 立项门槛 ≥ Medium，可以作为「4 个同形 slice 收敛到单源」的范式样板。

3. **F-CR-2 EventID-only 指纹设计**：纠正了原 multi-field 指纹在 at-least-once redelivery 下退化为「每次重投产生不同指纹」的根因，加上 DB-level UNIQUE INDEX (migration 021) 作为 second-line guard。advisory lock 顺序 (Step 2 before Step 3) 显式注释 TOCTOU race。

4. **composition root HMAC key 处理**（`cmd/corebundle/audit_module.go:79`）：`clear(hmacKey)` belt-and-suspenders + `WithChainHMAC` option 内部 `clear(key)` 形成双层清零，密钥进入 Protocol 后立刻从 caller heap 抹除。`Protocol.HMACKey()` 返回 defensive copy（虽然按 A-08 应该删 getter）也保持 immutability。

5. **storetest conformance suite 设计**：`ledger.Store` 两个实现（MemStore + PG）共享 12 个 case + `protocol.ComputeHash` 用作 expected-hash reference；F-CR-2 redelivery case (`Idempotency_DifferentTimestamp_SameEventID`) 用 fakeClock advance 模拟，覆盖了关键回归路径。

6. **HMAC key minimum 32 bytes + key 永不出现在 error message**：RFC 2104 §3 + NIST SP 800-107 / FIPS 198-1 显式 cite，error message 只暴露 `minimumBytes / actualBytes`，从源头切断 key 泄漏面。

7. **migration 020/021 设计审慎**：
   - seq_no 不用 SERIAL/IDENTITY 而是显式 app-side 用 advisory_lock + SELECT FOR UPDATE 串行化分配，避开 SERIAL 自增锁与 advisory_lock 的双重 contention；
   - `pg_advisory_xact_lock(hashtextextended(namespace, 0))` 跨 namespace 不 contend，单 namespace 严格串行；
   - UNIQUE (namespace, seq_no) DB-level 兜底；
   - Down migration WARNING 显式提示数据销毁。

8. **pkg/redaction 单源治理**：HTTP audit payload 出口（`auditquery/handler.go:137`）和 slog error/panic redaction 共用 `sensitiveKeyPattern`，避免多份 regex 漂移。`RedactPayload` fail-closed 在 JSON unmarshal 失败时返回合法 JSON token `"<REDACTED>"`（不是裸字符串 `<REDACTED>` 触发 json.RawMessage 500）。

---

## 裁决备忘

- **A-01 (P0)**：必须修复或显式 backlog + 文档警示，否则 S8 PG cell 接入第一天就 broken。架构师不接受降级。
- **A-02 / A-03 (P1)**：建议二选一在本 PR 内整改（裁掉 / 加触发器 / 改 lifecycle）；如不修，必须 backlog 显式登记+设定触发条件。
- **A-04 / A-05 / A-06 / A-07 (P1)**：可在本 PR 内修复（成本低），也可拆 follow-up。建议至少 A-07 在本 PR 调整接口签名（contract 形态变化越晚越贵）。
- **A-08~A-11 (P2)**：follow-up PR 处理，不阻断合入。
