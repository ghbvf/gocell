# ADR: typed-Go-heavy 协议 primitive 范式

**Date**: 2026-05-10
**Status**: Proposed
**Related plans**: `docs/plans/202605082145-034-pg-corecell-b-route-plan.md`
**Supersedes (architectural intent)**: 034 §4 ADR-B 6 条边界规则（被 typed Go 类型系统天然画出）

---

## 1. Context

### 1.1 PG 接入循环

`accesscore` / `auditcore` / `configcore` 三个 corecell 接入 PG 的过程出现重复模式：

- PR#417 在 accesscore PG 接入中暴露 5 个 P0/P1 协议缺口（token 重放 / role revoke 排序 / admin 不变量 / credential 失效 / CAS）
- archive `202604201800-pg-pilot-layering-refactor-plan.md` 中 PG pilot 接入暴露过 4 个分层维度漂移（加密 / 生命周期 / Cell 组装 / 治理）
- 两次循环的共同形态：**接 PG 时一次性翻出 mem 模式下未被强制决策的协议**

### 1.2 第一性原理分析

接 PG 不是"换存储介质"，是"强制暴露 mem 模式下保持隐式的协议决策":

| 属性 | mem 默认 | PG 强制 |
|---|---|---|
| Durability | volatile | 哪些写持久 / 何时持久 |
| Consistency | 单进程原子 | tx 边界 / isolation level |
| Concurrency | mutex 平凡 | row lock / CAS / advisory |
| Recovery | 重启清空 | 重启从哪恢复 |
| Encryption | 无 | 哪些字段 / key rotation |
| Indexing | hash map | 查哪些列 / partial / 复合 |

**根因**：协议决策被写在了实现层（PG repo / cell 内部），而不是声明层。mem 不强制决策 → 决策保持隐式 → PG 强制时一次性爆发。

### 1.3 GoCell 已有的演化轨迹（未显式 ADR 化）

| PR / 范式 | 改动 |
|---|---|
| PR262 typed AuthPlan | string config → sealed `cell.ListenerAuth` interface (`AuthJWT` / `AuthServiceToken` / `AuthMTLS` / `AuthNone`) |
| PR-MODE-1 typed-nil reject | `pkg/validation.IsNilInterface` + bootstrap phase0 sentinel；`authChain` nil 显式 fail-fast |
| PR-MODE-6 error-first constructor | `cell.NewAuthJWT(v) (AuthJWT, error)` / `MustNewAuthJWT` 双轨；`OUTBOX-SERVICE-01` archtest 守 fail-fast on nil TxRunner |
| Option 范式分层 | 强依赖 wiring（nil → flag → phase0 fail-fast）vs 累加式 builder（nil → noop → factory fail-fast）|
| typed `wrapper.ContractSpec` | 取代 yaml 字符串引用，运行时组装 typed |
| `cell.RouteGroup` / `cell.ListenerRef` | 路由组装 typed Go，非 yaml |

这是同一方向的连续演化，但**未被显式 ADR 化为"GoCell 协议决策的官方范式"**，导致每次新加协议（session / cas / audit ledger）时仍按 library-style runtime 抽取思路走。

### 1.4 决策载体的 AI-friendly 评估

GoCell 是 AI 主导开发的项目。AI 出错形态分类：

| 形态 | 触发场景 |
|---|---|
| F1 忘记某约束 | 约束散在多文件 |
| F2 复制错误模式 | 历史代码不规范当 reference |
| F3 绕过 type check | 用 `interface{}` / `any` |
| F4 多源不一致 | yaml 改了 Go 没改 |
| F5 静默错 | 默认值掩盖 |
| F6 局部 plausible / 全局错 | 看不全 codebase |
| F7 长清单跳过 | review checklist |

减缓机制的强度排序：

```
[最强] 编译错误 (sealed interface / typed const / 强类型签名)
       phase0 启动 fail-fast
       constructor fail-fast
       runtime panic / error
       archtest 静态拦截
       integration test
       review checklist
[最弱] 文档约定
```

**typed-Go-heavy 是把约束往强端推的工程化实现**。

---

## 2. Decision

### D1 协议决策必须在最早能用类型系统检查的层级

GoCell 中这一层是 **typed Go primitive**：sealed interface + 强类型 constructor + composition-root 显式构造。不允许藏在 implementation 层（cell 内部 / SQL / mem store 字段）。

### D2 yaml 仅承载静态拓扑 metadata

| 类型 | 载体 |
|---|---|
| 静态拓扑（id / consistencyLevel / 拥有者 / verify 义务 / contract 引用） | yaml |
| schema 形态（HTTP / event payload） | yaml + json schema → 派生 typed Go |
| **协议决策 / 运行时组装 / 互斥约束 / 生命周期 wiring** | **typed Go** |

理由：yaml 表达不了 sealed / 互斥 / 强依赖 nil-fail-fast 这三类约束；typed Go 编译期可查，无需外部工具。

### D3 typed Protocol primitive 由 sealed Option 组合构造

每个跨 cell 协议（session / cas / audit ledger / ...）以 typed Protocol 形式存在：

- 模式属性是 sealed interface 或 typed enum
- Option 函数每个对应一个属性
- `NewProtocol(opts ...Option) (*Protocol, error)` fail-fast 校验互斥与必填
- `MustNewProtocol(...)` 是 composition-root 包装

参见 §4 session protocol 原型。

### D4 mem 与 PG 共享 storetest conformance suite，suite 接受 typed Protocol

```go
storetest.Run(t, factory, protocol *Protocol)
```

mem / PG factory 都接受同一 Protocol 实例；suite 根据 Protocol 字段派生 test cases。**mem 不再"默认能跑"**，与 PG 对称 conform 同一协议。

### D5 composition root 必须显式构造 typed Protocol

`cmd/corebundle/*_module.go` 必须显式 `MustNewProtocol(...)` 构造并注入 cell。
- typed-nil reject 让"忘了构造"在 phase0 fail-fast
- 强依赖 wiring option 让"忘了配某属性"在 phase0 fail-fast

### D6 archtest 是兜底，不是主防线

主防线在 type system + codegen。archtest 只守"无法 funnel 也无法 typed 化的残留"（如"PG 写路径不绕过 RunInTx"这种调用形态约束）。033 §6 计划的 `PG-REPO-CONSTRUCTOR-FAIL-FAST-01` 由 typed 签名 + body 顶层校验直接覆盖，降级删除。

---

## 3. Consequences

### 3.1 正面

- **F1/F3/F5 出错形态降到编译期**：sealed interface 让"忘了 case"无法编译；typed-nil reject 让"忘了赋值"phase0 fail-fast
- **F2 出错降到接口层**：新 substrate 必须 conform typed Protocol，不能"差不多就行"
- **F4 出错从源消除**：yaml 不承载协议，无 yaml↔Go 漂移
- **PR 评审复杂度降低**：协议决策在 composition root 一目了然，不需要跨 5 个文件 grep
- **新 cell 接 PG 不再触发协议爆发**：composition root 强制声明 → 启动期暴露，不到 review

### 3.2 负面

- **协议升级成本**：新加 Option / 新协议属性需要在 composition root 显式重声明（这是预期成本，等于把"暗债"显化）
- **Go 类型设计前置成本**：每个协议第一次落地需投入 Option 范式设计；后续 substrate 复用
- **不能用 yaml 工具链分析协议**：协议在 Go AST，需 Go 工具链分析（gocell 已有 archtest / `go/packages`，能力具备）

### 3.3 与现有约束的关系

- **K-04**（cells/{accesscore,auditcore,configcore} 留 framework 仓）：cell 仍是协议本体（消费 + 业务决策），typed Protocol primitive 是 runtime 提供的"协议词汇表"，无矛盾
- **funnel-first**（CLAUDE.md `## 新增 invariant 决策原则`）：typed Go 是 funnel 优先级 #2 "type system 自然拦"的具体形式；archtest 退到 #3 兜底，与原则一致
- **Option 范式分层**（runtime-api.md）：typed Protocol Option 沿用强依赖 wiring vs 累加式 builder 的现有判定规则
- **PR262 / PR-MODE-1 / PR-MODE-6**：本 ADR 把这三个 PR 的隐式范式显式化

---

## 4. session protocol 原型（验证形态可落地）

> 以下伪代码层级，供 S2 PR 落地时参照；实际签名以 PR 内为准。

### 4.1 Protocol 类型

> **注意**：此原型为初始范式说明；ADR-Session (`docs/architecture/202605101400-adr-credential-session-protocol.md`) D1/D6 决议已精简到 jti-only 单实现；以 ADR-Session §2 D1/D6 与 `runtime/auth/session/protocol.go` 为准。

```go
// runtime/auth/session/protocol.go
package session

// FingerprintMode is sealed: only types in this package implement it.
type FingerprintMode interface{ fingerprintModeOK() }

type fingerprintHMACSha256 struct{ key []byte }
func (fingerprintHMACSha256) fingerprintModeOK() {}

type fingerprintNone struct{} // dev / demo only
func (fingerprintNone) fingerprintModeOK() {}

// CredentialEvent enumerates credential state changes that revoke active sessions.
type CredentialEvent int

const (
    CredentialEventPasswordReset CredentialEvent = iota + 1
    CredentialEventLock
    CredentialEventDelete
    CredentialEventRoleRevoke
)

// OrderingModel is sealed: defines login vs role-revoke ordering primitive.
type OrderingModel interface{ orderingModelOK() }

type OrderingAdvisoryLock struct{}
func (OrderingAdvisoryLock) orderingModelOK() {}

type OrderingAuthzEpoch struct{}
func (OrderingAuthzEpoch) orderingModelOK() {}

type OrderingRowVersion struct{}
func (OrderingRowVersion) orderingModelOK() {}

// Protocol bundles protocol decisions for a session subsystem.
// All fields required; nil means caller forgot to opt in.
type Protocol struct {
    fingerprint FingerprintMode
    revokeOn    []CredentialEvent
    ordering    OrderingModel
}

type Option func(*Protocol) error

func WithFingerprintHMACSha256(key []byte) Option {
    return func(p *Protocol) error {
        if len(key) < 32 {
            return errcode.New(errcode.ErrValidationFailed,
                "session protocol: HMAC key must be ≥32 bytes")
        }
        p.fingerprint = fingerprintHMACSha256{key: key}
        return nil
    }
}

func WithRevokeOn(events ...CredentialEvent) Option {
    return func(p *Protocol) error {
        if len(events) == 0 {
            return errcode.New(errcode.ErrValidationFailed,
                "session protocol: WithRevokeOn requires ≥1 event")
        }
        p.revokeOn = append(p.revokeOn, events...)
        return nil
    }
}

func WithOrdering(om OrderingModel) Option {
    return func(p *Protocol) error {
        if om == nil { // typed-nil also rejected via validation.IsNilInterface
            return errcode.New(errcode.ErrValidationFailed,
                "session protocol: ordering required")
        }
        p.ordering = om
        return nil
    }
}

// NewProtocol fail-fast on missing required attributes.
func NewProtocol(opts ...Option) (*Protocol, error) {
    p := &Protocol{}
    for _, opt := range opts {
        if err := opt(p); err != nil {
            return nil, err
        }
    }
    if p.fingerprint == nil {
        return nil, errcode.New(errcode.ErrValidationFailed,
            "session protocol: fingerprint mode required (use WithFingerprintHMACSha256)")
    }
    if p.ordering == nil {
        return nil, errcode.New(errcode.ErrValidationFailed,
            "session protocol: ordering model required (use WithOrdering)")
    }
    return p, nil
}

// MustNewProtocol is the composition-root convenience wrapper.
func MustNewProtocol(opts ...Option) *Protocol {
    p, err := NewProtocol(opts...)
    if err != nil {
        panic(err)
    }
    return p
}
```

### 4.2 Store 接口（Protocol 决定方法形态）

```go
// runtime/auth/session/store.go
package session

type Session struct {
    ID         string
    SubjectID  string
    Fingerprint []byte // shape determined by Protocol.fingerprint
    CreatedAt  time.Time
    // ...
}

type Store interface {
    Create(ctx context.Context, s *Session) error
    Get(ctx context.Context, id string) (*Session, error)
    Revoke(ctx context.Context, id string) error
    RevokeForSubject(ctx context.Context, subjectID string, event CredentialEvent) error
}
```

### 4.3 storetest conformance（Protocol-driven）

```go
// runtime/auth/session/storetest/suite.go
package storetest

func Run(t *testing.T, factory func(t *testing.T) (session.Store, func()), protocol *session.Protocol) {
    t.Helper()
    t.Run("Create_Get", func(t *testing.T) { /* always */ })
    t.Run("Revoke_Direct", func(t *testing.T) { /* always */ })

    // Protocol-driven: derive cases from declared revoke events
    for _, event := range protocol.RevokeOn() {
        event := event
        t.Run("RevokeOn_"+event.String(), func(t *testing.T) {
            store, cleanup := factory(t)
            defer cleanup()
            // ... assert RevokeForSubject(ctx, subj, event) revokes all matching
        })
    }

    // Fingerprint shape conformance
    switch protocol.Fingerprint().(type) {
    case session.FingerprintJTIRef:
        t.Run("Fingerprint_NotPlaintext", func(t *testing.T) { /* assert no plaintext token in store */ })
    }
}
```

### 4.4 composition root 显式构造

```go
// cmd/corebundle/access_module.go (postgres mode)
sessionProto := session.MustNewProtocol(
    session.WithFingerprint(session.FingerprintJTIRef{}),
    session.WithRevokeOn(
        session.CredentialEventPasswordReset,
        session.CredentialEventLock,
        session.CredentialEventDelete,
        session.CredentialEventRoleRevoke,
    ),
    session.WithOrdering(session.OrderingAuthzEpoch{}),
)

pgSessionStore, err := pgstore.NewSessionStore(shared.SharedPGPool, txMgr, sessionProto)
if err != nil {
    return nil, err
}

cell := accesscore.New(
    accesscore.WithSessionProtocol(sessionProto),
    accesscore.WithSessionStore(pgSessionStore),
    // ...
)
// AccessCore.Init phase0: typed-nil reject sessionProto / sessionStore via WithSessionProtocol/WithSessionStore (强依赖 wiring)
```

### 4.5 与 archtest 的关系

| 033 §6 原计划 archtest | typed Protocol 后形态 |
|---|---|
| `PG-REPO-CONSTRUCTOR-FAIL-FAST-01` | typed `func New(pool, txRunner, proto) (*T, error)` 签名 + body 顶层校验，**降级删除** |
| `PG-REPO-AMBIENT-TX-01` | 调用形态约束，**保留**（typed signature 不能拦） |
| `PG-REPO-INVARIANT-LIST` 索引 | 由 storetest 注册派生，**降级删除** |

新增 archtest（兜底）：
- `SESSION-PROTOCOL-COMPOSITION-ROOT-01`：`session.NewProtocol` / `MustNewProtocol` 仅在 `cmd/` 调用，禁止在 `cells/` / `runtime/` 内构造（防止 cell 自定义协议绕过 composition-root 决策）

---

## 5. Alternatives Considered

### 5.1 cell.yaml 加 protocol properties（撤回）

把 `protocols: [session, cas, auditledger]` 加进 cell.yaml，codegen 派生 store / migration / handler。

撤回理由：
- yaml 表达不了 sealed interface / 互斥 / typed-nil reject
- 与 PR262 演化方向相反（PR262 把 string config 升 typed AuthPlan）
- 多源治理：yaml ↔ Go 仍漂移

### 5.2 cell-as-data + 整体 codegen（推迟）

把 cell 整体 codegen（K8s controller-runtime / kubebuilder 风格），cell 作者只写 reconcile body。

推迟理由：
- 重大架构投资，2-3 个月起步
- PR#417 业务侧不能等
- typed-Go-heavy 是同方向的更小步长，留 codegen 路径开放

### 5.3 维持 library-style runtime + archtest（PR#417 验证不可行）

继续按 034 v2 走"抽 runtime/auth/session 等 Go 包 + cell import 消费"，archtest 守范式。

撤回理由：library 仍由 cell 手写消费 → 协议决策仍可以藏在 cell 实现 → PR#417 现象重演。

### 5.4 整套 PG store 单点 codegen 工具（撤回）

写 `cmd/gocell generate pg-store --schema=xxx.codegen.yaml`。

撤回理由：
- 增加额外能力层（违反 user 反馈"能不能整体思考"）
- yaml schema 是错的源头方向（同 5.1）
- typed Go schema → 派生 SQL migration 是后续可选优化，不是 Wave 0 必做

---

## 6. Migration / Rollout

| 阶段 | 内容 |
|---|---|
| Phase 1（本 ADR + 034 重写） | 锁定方向；S2/S3+S5/S4/S6/S7 PR 内落 typed Protocol primitive；mem/PG conform 同 storetest |
| Phase 2（按需） | typed Go schema → SQL migration / errcode / schema_guard 派生工具（kubebuilder annotation 风格） |
| Phase 3（按需） | cell 整体 codegen（如果 typed Protocol 演化中证明 cell 内 boilerplate 仍重） |

Phase 2/3 不在本 ADR 范围；本 ADR 仅锁 Phase 1 方向。

---

## 7. Open Questions

1. PG migration 文件本身是 SQL，与 typed Go schema 之间的桥梁形态待 Phase 2 决定（候选：kubebuilder annotation / sqlc-style / ent-style）
2. cell 内部对协议是"消费"（仅调 Store 接口）还是"实现"（自己 implement Protocol 部分），sealed interface 边界 case-by-case 判断
3. 现有 cell.yaml 字段哪些应该 typed Go 化（待 audit；不在本 ADR 范围）

---

## 8. References

- `docs/plans/202605082145-034-pg-corecell-b-route-plan.md` — B 路线计划（本 ADR 触发其重写）
- `docs/reviews/202605082044-pr417-pg-corecell-framework-analysis.md` — PR#417 分析（路线源）
- `docs/plans/archive/202604201800-pg-pilot-layering-refactor-plan.md` — 历史 PG pilot 分层重构（同形态前例）
- `.claude/rules/gocell/runtime-api.md` — Option 范式分层 / sealed AuthPlan / 强依赖 wiring
- `CLAUDE.md` `## 新增 invariant 决策原则` — funnel + codegen → type system → archtest 优先级
- PR262 / PR-MODE-1 / PR-MODE-6 — typed-Go-heavy 演化前例
