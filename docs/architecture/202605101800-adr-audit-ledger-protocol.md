# ADR: Audit Ledger Protocol（typed Protocol primitive 决议）

**Date**: 2026-05-10
**Status**: Accepted
**Accepted by**: PR #450 / 2026-05-10
**Related plan**: `docs/plans/202605082145-034-pg-corecell-b-route-plan.md` S7
**Related ADRs**:

- `docs/architecture/202605101200-adr-typed-go-heavy-protocol-primitives.md`（typed-Go-heavy 范式锚点）
- `docs/architecture/202605101400-adr-credential-session-protocol.md`（session 协议；同范式首个落地）

---

## 1. Context

### 1.1 触发因素

`cells/auditcore/internal/domain/hashchain.go` 是一个 in-memory hashchain 实现，在 PG 接入阶段（B 路线 S7）需要：

1. **B2-C-01 (P0)**：重启后无法恢复 hash chain 状态 — 重启即断链，无法验证历史条目与新条目的连续性。
2. **B2-C-10**：并发 Append 在 in-cell mutex 下串行，但 PG 接入需要显式 `pg_advisory_xact_lock` 语义。
3. **PR266**：payload strict validation（`DisallowUnknownFields`）缺失 — 非法 payload 静默接受。

按 typed-Go-heavy 范式（ADR-Typed `202605101200-...`），解法不是修补 in-cell hashchain，而是将协议决策上提到 `runtime/audit/ledger/`，让 cell 消费注入的 `*ledger.Protocol` + `ledger.Store` 接口，与 session 协议落地方式保持一致。

### 1.2 在 typed-Go-heavy 范式中的位置

ADR-Typed 锁定了 GoCell 协议决策范式：sealed interface + Option + composition-root 显式构造。本 ADR 是该范式在 audit ledger 协议上的落地：

- 协议决策落 typed Go 词汇表（`runtime/audit/ledger/protocol.go`，S7 W1 PR 实施）
- MemStore + storetest conformance suite（S7 W1 PR 实施）
- PG-backed Store（S8 PR 实施）
- auditcore cell 接入（S9 PR 实施）

S7 W1 PR 落 ADR + 完整 Protocol / MemStore / storetest 实施；不留 panic stub。

---

## 2. Decision

### D1 Hash Chain 算法

HMAC-SHA256，key ≥ 32 字节（RFC 2104 §3，NIST SP 800-107 / FIPS 198-1）。

HMAC 输入字段顺序与 `cells/auditcore/internal/domain/hashchain.go` 保持 byte-for-byte 等价：

```
msg = prevHash|eventID|eventType|actorID|UnixNano|payload
hash = hex.EncodeToString(HMAC-SHA256(key, msg))
```

分隔符为 `|`（ASCII 0x7C）。`UnixNano` 是 `time.Time.UnixNano()` 的十进制字符串表示。`payload` 是 `string(entry.Payload)` 直接转换（bytes → string，无 base64）。

**为什么 byte-for-byte 等价**：PG store（S8）接入时需要将已有 in-cell entries 迁移到新 ledger store。如果 hash 算法不一致，历史链断链，无法 verify。

**HMAC key 安全要求**：
- key 必须 ≥ 32 bytes（hash output 大小；短 key 降低 HMAC 安全强度）
- key 不得出现在错误消息或 slog 字段中
- key 通过 `WithChainHMAC` 注入；`Protocol.HMACKey()` 返回 defensive copy

ref: google/trillian log/sequencer.go@master

### D2 Restart Recovery = Strict Tail Verify

`RestartRecoveryStrictTailVerify` 是唯一支持的重启恢复模式（sealed interface 关闭枚举）。语义：

- **MemStore**：无持久化，重启即空 store；Strict Tail Verify 是 no-op（chain 从空开始）。
- **PG store（S8）**：`NewPGStore` 在接受第一个 `Append` 之前，通过 `SELECT ... FOR UPDATE` 读取最后 N 条 entry 并重新计算 HMAC，确认尾部完整性。如果验证失败，`NewPGStore` 返回 error，进程拒绝启动。

**B2-C-01 (P0) 解题**：PG store 构造期 tail verify 保证重启后 chain 不断链。

ref: google/trillian log/sequencer.go — `IntegrateBatch` 在接受新叶前验证 tree head 完整性。

### D3 Idempotency = Content Fingerprint（EventID-only，F-CR-2）

`IdempotencyContentFingerprint` 是唯一支持的幂等模式（sealed interface 关闭枚举）。

**指纹 = SHA-256(EventID)**（仅 EventID，UUID 全局唯一稳定身份）。

**修订说明（F-CR-2）**：原设计指纹为 SHA-256(eventID + `\x00` + eventType + `\x00` + actorID + `\x00` + UnixNano + `\x00` + payload)，包含 Timestamp（`UnixNano`）。at-least-once outbox redelivery 时 `clk.Now()` 不同 → 每次重投产生不同指纹 → 同事件被多次写入 hash chain，破坏"一事件一记录"审计语义。

EventID 对应 outbox Entry UUID，在所有重投中保持不变，是唯一稳定的事件身份标识：

- EventType / ActorID：稳定但与 EventID 冗余；不影响碰撞抵抗性。
- Timestamp：每次重投不同 → 不得纳入指纹。
- Payload：可能因 schema 演化略有变化 → 不得纳入指纹。

**DB 第二防线**：`adapters/postgres/migrations/018_audit_entries_event_id_unique.sql` 在
`audit_entries(namespace, event_id)` 加 UNIQUE INDEX，防止并发 Append 绕过应用层检查。

重复 EventID 的 `Append` 返回 `ErrAuditLedgerAlreadyExists`（idempotent — 调用方将第二次视为成功，不重试）。

**PG store 实现**：`selectFingerprintSQL` 仅对 `(namespace, event_id)` 做 SELECT 1 检查；UNIQUE INDEX 是 DB 层 second-line guard。

ref: Watermill router.go — message.UUID 作为 consumer group 去重键。
ref: NServiceBus MessageDeduplicationBehavior — message ID 幂等键（与 Timestamp/Payload 无关）。
ref: google/trillian types/logroot.go — `LeafIdentityHash` 内容寻址去重。

### D4 并发控制

| 后端 | 机制 |
|------|------|
| MemStore | `sync.Mutex` 串行化 Append |
| PG store（S8） | `pg_advisory_xact_lock(ledger_namespace_id)` + `SELECT MAX(seq_no) ... FOR UPDATE` |

PG advisory lock 保证单 ledger namespace 下 Append 严格串行，防止 seq_no gap 和 PrevHash 竞争写。

### D5 Strict Payload（默认开启，无 toggle）

所有 `Append` 调用强制验证 payload 是有效 JSON（或 nil）。这是**静态决策**，没有 toggle Option：

- 理由：audit 条目必须是可解析的 JSON —— 非法 payload 进入链后无法被 query 层处理（PR266 根因）。
- 实现：`bytes.NewReader(payload)` + `json.Decoder.Decode(&interface{})` 失败 → `ErrValidationFailed`。
- nil payload = JSON null = 合法（省略 payload 场景）。

**为什么无 toggle**：Soft 选项（`WithStrictPayload(false)`）对 AI 可绕过性高，且没有任何已知合法用例需要 non-JSON payload。

### D6 NamespaceID

`NamespaceID` 是 typed string，mirror `adapters/redis.KeyNamespace` 校验规则：

| 约束 | 规则 |
|------|------|
| 空 | 拒绝 |
| 长度 | ≤ 48 bytes |
| 首字符 | `[a-z_]` |
| 全小写 | 无大写字母 |
| 禁止字符 | `:`、`{`、`}` |

典型值：cell ID（如 `auditcore`）。

---

## 3. Consequences

### 3.1 正面影响

- **B2-C-01 (P0) 关闭**：PG store 构造期 tail verify 保证重启不断链。
- **B2-C-10 关闭**：`sync.Mutex`（MemStore）/ advisory lock（PG）显式并发控制，hash chain 验证全通过。
- **PR266 关闭**：strict payload validation 默认开启，无法绕过。
- **cell 解耦**：auditcore 消费 `ledger.Store` 接口，不再持有 in-cell hashchain 实例，PG 切换零接触 cell 代码。
- **storetest conformance**：MemStore 和 PG store 共享 `storetest.Run`，协议决策对两个后端一致证明。

### 3.2 负面影响

- MemStore 没有持久化：每次测试运行从空 store 开始，restart recovery case 通过 GetBySeq/Append replay 模拟（不是真实 restart）。
- PG store（S8）需要额外迁移：将 in-cell hashchain 历史数据迁移到新 ledger table，hash 连续性依赖 D1 byte-for-byte 等价。

---

## 4. 参考

- ref: google/trillian storage/log_storage.go@master — LogStorage 接口形态
- ref: google/trillian log/sequencer.go@master — IntegrateBatch tail verify 模式
- ref: google/trillian types/logroot.go@master — LeafIdentityHash 内容指纹
- ref: sigstore/rekor — append-only transparency log with hash chain
- cells/auditcore/internal/domain/hashchain.go — 旧 in-cell hashchain（byte-for-byte 等价来源）
- runtime/auth/session/protocol.go — typed-Go-heavy 范式 mirror 模板
