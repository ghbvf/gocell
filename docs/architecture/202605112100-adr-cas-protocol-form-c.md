# ADR: CAS Protocol Form C（不暴露 Store / mem_store / storetest）

**Date**: 2026-05-11
**Status**: Accepted
**Accepted by**: PR #464 / 2026-05-11
**Related plan**: `docs/plans/202605082145-034-pg-corecell-b-route-plan.md` §4 S6
**Related ADRs**:

- `docs/architecture/202605101200-adr-typed-go-heavy-protocol-primitives.md`（typed-Go-heavy 范式锚点）
- `docs/architecture/202605101400-adr-credential-session-protocol.md`（session 协议；Form A，暴露 Store + mem_store + storetest）
- `docs/architecture/202605101800-adr-audit-ledger-protocol.md`（audit ledger 协议；Form B，暴露 Store + storetest）

---

## 1. Context

### 1.1 触发因素

S6（PR #464）引入 `runtime/state/cas` Protocol，为 accesscore ChangePassword、
configcore configwrite / flagwrite 的乐观并发控制（Optimistic Concurrency Control）
提供统一的冲突检测词汇表：

- `cas.Protocol`：封装版本字段名、冲突判定逻辑。
- `cas.ErrVersionConflict`：CAS 冲突标准错误码，映射 HTTP 409。
- `cas.CheckVersionMatch(stored, kind, key)`：helper，从 stored==0（no-rows-affected）
  推断冲突并返回 `ErrVersionConflict`。
- `cas.WithVersionField(field string)` sealed Option。
- `cas.MustNewProtocol(opts...)` 组合根构造器（`CAS-PROTOCOL-COMPOSITION-ROOT-01` archtest 守卫）。

### 1.2 原计划 Form A / Form B 分析

plan v3 §S6 原稿隐含了与 S2 session / S7 audit ledger 对称的结构：

| 组件 | S2 session（Form A） | S7 audit ledger（Form B） | S6 CAS（原稿倾向） |
|------|------|------|------|
| Protocol | `runtime/auth/session.Protocol` | `runtime/audit/ledger.Protocol` | `runtime/state/cas.Protocol` |
| Store | `runtime/auth/session.Store` interface | `runtime/audit/ledger.Store` interface | `runtime/state/cas.Store` interface（未实现）|
| mem_store | `runtime/auth/session/memstore` | — | `runtime/state/cas/memstore`（未实现）|
| storetest | `runtime/auth/session/storetest` | `runtime/audit/ledger/storetest` | `runtime/state/cas/storetest`（未实现）|

按此对称模式，S6 应提供 ~150 行的 `Store` interface + mem_store + storetest。

### 1.3 概念异质性

CAS 与 session / audit ledger 存在根本概念差异，决定了 Form A/B 的对称结构不适用：

| 维度 | S2 session / S7 audit ledger | S6 CAS |
|------|------|------|
| 对象生命周期 | Store 管理 session/entry 的完整生命周期（Create/Get/Revoke/Append） | CAS 只是一个**写约束 policy**，不拥有被守护对象的生命周期 |
| entity 类型 | `session.Session`、`audit.Entry` 是协议专属 entity | `User`、`ConfigEntry`、`FeatureFlag` 是各 cell 私有 entity，无共用基类 |
| 共享合约 | session Store / ledger Store 定义了完整 CRUD 合约 | CAS Store 只能定义"有版本字段的行"——需要发明 `CASRow` 或 opaque blob 伪 entity |
| 测试覆盖路径 | storetest 测试 Store 实现行为（如 pgstore） | CAS 一致性由各 cell repo 的 race-condition integration test 直接覆盖 |

---

## 2. Decision

**选择 Form C**：CAS Protocol 仅暴露：

1. `runtime/state/cas.Protocol`（sealed，含 `VersionField()` 方法）
2. `cas.WithVersionField(field string)` sealed Option
3. `cas.MustNewProtocol(opts...)` 组合根构造器
4. `cas.ErrVersionConflict` 标准错误码
5. `cas.CheckVersionMatch(stored int, kind, key string) error` helper

**不提供**：

- `cas.Store` interface（不定义通用 CAS 存储合约）
- `runtime/state/cas/memstore`（无需泛型 in-memory CAS 实现）
- `runtime/state/cas/storetest`（conformance 套件）

---

## 3. Rationale

### 3.1 CAS 是约束，不是对象

session.Store 管理 session 的完整生命周期；ledger.Store 管理 audit entry 的追加链。
CAS Protocol 不管理任何对象——它只向 cell repo 提供"当版本不匹配时应该返回什么错误"的策略。
强加一个 Store interface 需要在 CAS 层重新定义 entity（`CASRow`、`VersionedRecord` 等），
这是对实际业务概念的误建模。

### 3.2 Entity 归属不可共享

User、ConfigEntry、FeatureFlag 是各自 cell 的私有聚合根，每个都有与 CAS 无关的
字段（`PasswordHash`、`Sensitive`、`RolloutPercentage` 等）。定义 `cas.Store[T]`
泛型接口需要所有 cell repo 实现 CAS 专用 CRUD，而这些 CRUD 与 cell 已有的 repo
接口高度重叠，形成重复抽象层。

### 3.3 Conformance 路径直接且充分

CAS 一致性（"并发写只有一个成功"）由 cell 内部的并发 integration test 直接证明：

- `cells/accesscore/slices/identitymanage/service_test.go`: `TestChangePassword_ConcurrentRequests_ExactlyOneSucceeds`（mem path）
- `cells/accesscore/slices/identitymanage/service_pg_integration_test.go`: `TestChangePassword_ConcurrentRequests_ExactlyOneSucceeds_PG`（PG path，真实 SQL CAS 守卫）
- `cells/configcore/slices/configwrite/service_test.go`: `TestConcurrentUpdate_ExactlyOneSucceeds`、`TestConcurrentDelete_ExactlyOneSucceeds`
- `cells/configcore/slices/flagwrite/service_test.go`: `TestConcurrentToggle_ExactlyOneSucceeds`、`TestConcurrentUpdate_ExactlyOneSucceeds`、`TestConcurrentDelete_ExactlyOneSucceeds`

无需 `storetest` 套件即可在真实 cell 上下文中验证 CAS 语义。

### 3.4 代码量节省

Form C 相比 Form A/B 节省约 150 行无功能损失代码（Store interface + mem_store + storetest）。
这部分代码在当前 3-consumer（accesscore/configcore shared config）场景下产生的益处为零，
但引入了需要长期维护的抽象层。

---

## 4. Consequences

### 4.1 表面非对称

CAS Protocol 与 S2 session / S7 audit ledger 的 Protocol 在外形上不对称：
session 和 ledger 各有 Store + storetest，CAS 没有。这是**有意的**概念正确性选择，
不是遗漏或技术债。未来如果出现需要跨 cell 共享通用 CAS store 的场景（例如超过 5 个 cell
都有版本化 entity 且 repo 接口完全同构），可以按 Form A 升级——当前证据不支持该路线。

### 4.2 新 consumer 接入指南

新 cell 需要 CAS 保护时：

1. Cell repo 接口加 `UpdateXxx(ctx, ..., expectedVersion int) (newVersion int, err error)` 方法。
2. SQL（或 mem repo）实现 `WHERE version = $expected RETURNING version`，
   `0 rows affected` 时调用 `cas.CheckVersionMatch(0, "kind", key)` 返回 `ErrVersionConflict`。
3. Composition root：`cas.MustNewProtocol(cas.WithVersionField(MyCell.VersionField))`。
4. Cell 消费 `*cas.Protocol` 通过 `WithCASProtocol(proto)` option 注入。
5. 补并发测试（mem path + PG integration test，参考 `identitymanage` 模式）。

### 4.3 archtest 守卫

`CAS-PROTOCOL-COMPOSITION-ROOT-01` archtest 拦截 cell 内部直接调用 `cas.MustNewProtocol`，
确保 Protocol 始终由 composition root 构造后注入，不在 cell 内硬编码字段名。

---

## 5. References

- plan: `docs/plans/202605082145-034-pg-corecell-b-route-plan.md` §4 S6
- 范式锚点: `docs/architecture/202605101200-adr-typed-go-heavy-protocol-primitives.md`
- session（Form A 对比）: `docs/architecture/202605101400-adr-credential-session-protocol.md`
- audit ledger（Form B 对比）: `docs/architecture/202605101800-adr-audit-ledger-protocol.md`
- CAS Protocol 实现: `runtime/state/cas/protocol.go`
- CAS archtest: `tools/archtest/cas_protocol_invariants_test.go`
