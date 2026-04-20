# PG Pilot 分层重构 + 安全修复 Plan

> 基线：PR#192 (Topology + ManagedResource + AppDeps) + PR#194 (flag persist) + PR#195 (KeyProvider + LocalAES + VaultTransit) 合入后
> 审查报告：2026-04-20 多席位审查（27 条 finding，2 个 P0 安全、12 个 P1 架构/测试/产品）
> 目标：**一次性建立 "加密 / 生命周期 / Cell 组装 / 新 adapter 接入" 四个横切能力的终局分层**，消灭 backlog S17/S29/M1/A1/A4 等 5+ 延后项
> 施工期：~5 天（单人串行）/ ~3-4 天（双人并行）
> 预计 PR 数：6（R1a ~ R1e + R2）

---

## 1. 为什么要做（Why）

PR#192+194+195 的 27 条审查 finding 不是偶发 —— 它们是**同一个分层规则漂移**在 4 个维度的回声：

| 维度 | 症状 | 根因 |
|-----|------|------|
| 加密 | VaultTransit 把明文发给 Vault；`context` 字段错用（S1 P0）；每 provider 各写 AEAD/AAD | 接口层级错：provider 被迫同时扛加密 + wrap DEK 两个职责 |
| 生命周期 | `adapters/postgres/pool_resource.go` import `runtime/bootstrap`（违反"adapter 只依赖 kernel"）| `ManagedResource` 接口宿主包选错 |
| 组装 | `AppDeps` 13→14 字段，新加 Cell 改 5 处；`flagwrite.NewService` panic | 无 CellModule 抽象，God Struct 横向膨胀 |
| 治理 | Slice 目录 kebab-case + no-dash 双轨；VaultTransit HTTP adapter 放在 `runtime/crypto/` | 无"新增 adapter checklist"，每次靠 reviewer 临场发现 |

**对照 backlog**：S17/S29/A1/A4/M1 全部早已识别但"🟡 延后"，导致下一次加 adapter 继续踩坑。**彻底修复 = 一次把四个横切能力的分层规则做干净**，不再每次 PR 打补丁。

---

## 2. 现状核查（已做 ≠ backlog 记录的）

读代码发现 backlog 部分条目实际已完成（状态过时），真实剩余工作聚焦在 3 件事：

| backlog 条目 | 实际状态 | 证据 |
|-------------|---------|------|
| S30 OUTBOX-STORE-ABSTRACTION-01 | ✅ 已完成 | `adapters/postgres/outbox_store.go` + `runtime/outbox/relay.go` |
| X7 outbox_relay → runtime | ✅ 已完成 | 同上 |
| X8 distlock 分层 | ✅ 已完成 | `runtime/distlock/locker.go` + `adapters/redis/distlock.go` |
| S28 outbox envelope kernel share | ✅ 已完成 | `runtime/outbox/envelope.go`（WireMessage 单一事实源）|
| A7 PoolStats 接口 | 🟡 postgres 侧已有 | `adapters/postgres/pool_statter.go`（redis/rabbitmq 侧待 verify）|
| S17 POOL-FRAMEWORK-LIFECYCLE-01 | 🟡 部分 | `PGResource` 已有，但 `ManagedResource` 接口还在 `runtime/bootstrap/` —— R1a 修 |
| S29 CORE-BUNDLE-APP-BUILDER-01 | ❌ 未做 | `AppDeps` 仍是 God Struct —— R1d 修 |
| A1 VaultTransit 放错包 | ❌ 未做 | `runtime/crypto/vault_transit_provider.go` —— R1c 修 |

**核心剩余工作**：
1. **ManagedResource/KeyProvider/ValueTransformer 接口从 runtime/ 迁 kernel/**（R1a + R1b）
2. **VaultTransit 迁 adapters/vault/ 并重写为 envelope encryption 模式**（R1c，核心）
3. **AppDeps 解体为 CellModule**（R1d）
4. **安全修复 + 治理扫尾**（R2 + R1e）

---

## 3. 分层终局架构

```
kernel/                                  ← 领域接口与模型，零外部系统依赖
├── lifecycle/                          ← 新建
│   ├── managed_resource.go             ← 从 runtime/bootstrap/ 迁入
│   └── context_closer.go               ← 已存在，迁入
├── crypto/                             ← 新建
│   ├── key_provider.go                 ← 从 runtime/crypto/ 迁入
│   ├── value_transformer.go            ← 从 runtime/crypto/ 迁入
│   └── errors.go                       ← 哨兵错误
└── outbox/                             ← 已存在（Entry）

runtime/                                 ← 通用运行时实现（零外部系统协议）
├── crypto/                             ← 仅保留 LocalAES 与 envelope 辅助
│   ├── local_aes.go                    ← 实现 kernel/crypto.KeyProvider
│   └── key_provider_transformer.go     ← ValueTransformer 共享实现
├── bootstrap/                          ← 改为 kernel/lifecycle 的消费者
│   ├── app.go                          ← 新 BuildApp(modules ...CellModule)
│   ├── cell_module.go                  ← 新 CellModule interface
│   └── lifecycle.go                    ← 引用 kernel/lifecycle.ManagedResource
├── outbox/  distlock/  worker/         ← 已就位

adapters/                                ← 外部系统协议实现，只依赖 kernel/ + runtime/(接口)
├── vault/                              ← 新建
│   ├── transit_provider.go             ← 从 runtime/crypto/vault_transit_provider.go 迁入 + envelope 重写
│   ├── client_adapter.go               ← 从 runtime/crypto/vault_client_adapter.go 迁入
│   └── integration_test.go             ← 新建，testcontainers Vault dev
├── postgres/                           ← 改依赖 kernel/lifecycle
│   └── pool_resource.go                ← import kernel/lifecycle 替代 runtime/bootstrap
├── awskms/  gcpkms/                    ← S14a 未来路径（不在本 plan 内创建）

cmd/core-bundle/
├── main.go                             ← 5 行，拼 module 列表
├── shared_deps.go                      ← SharedDeps（原 AppDeps 的横切字段）
├── access_module.go                    ← access-core 自管 wiring
├── audit_module.go
└── config_module.go
```

### 依赖方向规则（强化）

| 层 | 允许依赖 | 禁止依赖 | 变化 |
|----|---------|---------|------|
| kernel/ | 标准库 + pkg/ + gopkg.in/yaml.v3 | runtime/ adapters/ cells/ | 不变 |
| runtime/ | kernel/ + pkg/ | cells/ adapters/ | 不变 |
| **adapters/** | **kernel/** + pkg/（不再依赖 runtime/ 的具体实现）| runtime/ 的实现细节；cells/ | **收紧** |
| cells/ | kernel/ + runtime/ | adapters/（通过接口）| 不变 |

---

## 4. PR 簇 + 依赖图

| PR | 范围 | 代码量 | 工时 | Review 难度 |
|----|------|-------|------|-----------|
| **R1a** | kernel/lifecycle 建立 + ManagedResource/ContextCloser 迁入 + adapters/postgres 改 import | +200/-150 | 0.5-1 天 | 低（纯搬家）|
| **R1b** | kernel/crypto 建立 + KeyProvider/ValueTransformer 接口迁入 + LocalAES 改 import（不动 Vault） | +250/-150 | 0.5 天 | 低 |
| **R1c** | VaultTransit 迁 adapters/vault/ + envelope 模式重写 + Vault testcontainer 集成测试 | +600/-400 | **1.5-2 天** | **高**（核心）|
| **R1d** | CellModule interface + BuildApp + AppDeps 解体 + 3 个 module 文件 + flagwrite.NewService 改 error | +600/-400 | 1.5 天 | 中 |
| **R1e** | PoolStats 接口统一（redis/rmq 侧）+ kebab-case 目录清理 + gocell validate --strict + adapter-checklist.md | +200/-150 | 0.5-1 天 | 低 |
| **R2** | Validate fail-closed（KeyProvider 存在性）+ TOCTOU 守卫 + rejectDemoKey(MASTER_KEY) + migration 010 Down RAISE EXCEPTION + VAULT_TOKEN static guard | +300/-50 | 0.5-1 天 | 中 |

### 依赖图

```
R1a (kernel/lifecycle)
  │
  ▼
R1b (kernel/crypto) ─── 可与 R1d 并行
  │
  ▼
R1c (Vault envelope + testcontainer) ─── 核心
  │
  ▼
R2 (安全修复 + fail-closed + 集成测试补丁)

R1d (CellModule + AppDeps 解体) ─── 独立，R1a 后即可开
R1e (治理扫尾) ─── 独立，任何时候
```

**串行工期**：~5 天（R1a → R1b → R1c → R1d → R1e → R2）
**并行工期**：~3 天（R1a 先；R1b+R1d+R1e 并行；R1c 接 R1b；R2 接 R1c）

---

## 5. R1a 详细：kernel/lifecycle 建立

### 文件清单

**新建**：
- `kernel/lifecycle/doc.go`
- `kernel/lifecycle/managed_resource.go` — 从 `runtime/bootstrap/managed_resource.go:21-41` 搬入 `ManagedResource` interface
- `kernel/lifecycle/context_closer.go` — 已存在则搬入（`kernel/lifecycle/context_closer.go` 已存在，确认）

**修改**：
- `runtime/bootstrap/managed_resource.go` — 保留 `WithManagedResource` option + `expandManagedResources`；`type ManagedResource = lifecycle.ManagedResource` 类型别名过渡 **但一次切完不留 alias**（用户偏好 no-lazy-deferral）→ 全部 import 改 `kernel/lifecycle`
- `adapters/postgres/pool_resource.go:8` — `import "runtime/bootstrap"` → `import "kernel/lifecycle"`
- `adapters/postgres/pool_resource.go:106` — `var _ bootstrap.ManagedResource = (*PGResource)(nil)` → `var _ lifecycle.ManagedResource = (*PGResource)(nil)`
- `runtime/bootstrap/*.go` 所有 `ManagedResource` 引用改 package 前缀

### 接口签名（不变）

```go
// kernel/lifecycle/managed_resource.go
package lifecycle

type ManagedResource interface {
    Checkers() map[string]func() error
    Worker() worker.Worker
    Close(ctx context.Context) error
}
```

**注意**：`worker.Worker` 在 `runtime/worker` —— kernel 不能依赖 runtime。

**解决方案**：`Worker` 接口同步迁入 `kernel/worker` 或改为 `interface{ Start/Stop }` 最小化抽象。**建议迁入 `kernel/worker`** —— worker 契约是领域接口，不是 runtime 特有。

### 验收

- `go build ./kernel/... ./runtime/... ./adapters/...` 通过
- `adapters/postgres/pool_resource.go` 不再 import `runtime/bootstrap`
- `PGResource` 测试不变（`pool_resource_test.go` 只替换 import）

### Commit 模板

```
refactor(lifecycle): ManagedResource → kernel/lifecycle

adapters/postgres/pool_resource.go 此前为使用 bootstrap.ManagedResource
接口而 import runtime/bootstrap，违反"adapter 只依赖 kernel"分层规则。

将 ManagedResource 与 ContextCloser 迁入 kernel/lifecycle，worker.Worker
同步迁入 kernel/worker（作为领域契约），adapters/postgres 改依赖 kernel。
runtime/bootstrap.WithManagedResource option 保留，内部消费 kernel 接口。

backlog: S17 POOL-FRAMEWORK-LIFECYCLE-01

ref: uber-go/fx lifecycle.go@master:L124-L310 — Lifecycle 接口在 fx 顶层公开包
ref: kubernetes/apiserver pkg/server/healthz — health probe 契约在公开接口包

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
```

---

## 6. R1b 详细：kernel/crypto 建立

### 文件清单

**新建**：
- `kernel/crypto/doc.go`
- `kernel/crypto/key_provider.go` — 搬 `runtime/crypto/key_provider.go:21-67`（KeyProvider + KeyHandle interface）
- `kernel/crypto/value_transformer.go` — 搬 `runtime/crypto/value_transformer.go:22-43`（ValueTransformer + CurrentKeyIDProvider + AADForConfig）
- `kernel/crypto/errors.go` — 引用 `pkg/errcode` 的哨兵错误（ErrKeyProvider*）

**修改**：
- `runtime/crypto/local_aes_provider.go` — 保留实现，import 改为 `kernel/crypto`；`var _ kcrypto.KeyProvider = (*LocalAESKeyProvider)(nil)` 编译期断言
- `runtime/crypto/value_transformer.go` — 仅保留 `keyProviderTransformer` struct 实现，interface 移走
- `runtime/crypto/vault_transit_provider.go` — import 改 `kernel/crypto`（文件位置不动，R1c 再迁）
- 所有 import `runtime/crypto` 引用 KeyProvider/ValueTransformer interface 的文件 → 改 `kernel/crypto`

### 接口签名（不变）

```go
// kernel/crypto/key_provider.go
package crypto

type KeyProvider interface {
    Current(ctx context.Context) (KeyHandle, error)
    ByID(ctx context.Context, keyID string) (KeyHandle, error)
    Rotate(ctx context.Context) (newKeyID string, err error)
}

type KeyHandle interface {
    ID() string
    Encrypt(ctx context.Context, plaintext, aad []byte) (ciphertext, nonce, edk []byte, err error)
    Decrypt(ctx context.Context, ciphertext, nonce, edk, aad []byte) (plaintext []byte, err error)
}
```

### 验收

- kernel/ 下 `go list -deps ./kernel/crypto/...` 零 runtime/ 或 adapters/ 依赖
- `go test ./runtime/crypto/...` 全部通过（只改了 import）

---

## 7. R1c 详细：Vault envelope 重写（核心）

### 问题根治

当前 `runtime/crypto/vault_transit_provider.go:67-91` 的 `Encrypt`：

```go
// ❌ 当前：直接发明文给 Vault，AAD 传 "context" 字段（对 non-derived key 无效）
payload := map[string]any{
    "plaintext": base64(plaintext),
    "context":   base64(aad),  // bug: 应为 "associated_data"
}
result := vault.encrypt(payload)
return []byte(result["ciphertext"]), nil, nil, nil
// ↑ nonce/edk 都是 nil，不遵循 envelope 模式
```

**两个问题合并**：
1. **S1 P0 安全**：`context` vs `associated_data` 字段错（对 aes256-gcm96 + non-derived key，`context` 被 Vault 忽略 → AAD 无密码学效力）
2. **A1 架构**：明文穿越信任边界发给 Vault；不遵循 LocalAES 的 envelope 模式导致接口"两套语义"（`edk=nil` vs `edk=wrapped_dek`）

### 修复方案（对标 k8s KMS v2）

```go
// ✅ envelope 模式：本地 AEAD + AAD，Vault 只 wrap DEK
func (h *vaultTransitHandle) Encrypt(ctx, plaintext, aad []byte) (ct, nonce, edk []byte, err error) {
    // 1. 本地生成 32-byte DEK
    dek := make([]byte, 32)
    io.ReadFull(rand.Reader, dek)

    // 2. DEK 本地 AES-GCM 加密 plaintext，AAD 在本层生效
    ct, nonce, err = aesGCMEncryptSplit(dek, plaintext, aad)
    if err != nil { return nil, nil, nil, err }

    // 3. Vault wrap DEK（明文不过 Vault）
    //    不传 context/associated_data — DEK 是随机 32 字节，无 AAD 需求
    result, err := h.client.Write(ctx, encryptPath, map[string]any{
        "plaintext": base64.StdEncoding.EncodeToString(dek),
    })
    if err != nil { return nil, nil, nil, errcode.Wrap(...) }
    wrappedDEK := []byte(result["ciphertext"].(string))

    return ct, nonce, wrappedDEK, nil  // edk = wrappedDEK（Vault ciphertext string）
}

func (h *vaultTransitHandle) Decrypt(ctx, ciphertext, nonce, edk, aad []byte) (plaintext []byte, err error) {
    // 1. Vault unwrap DEK
    result, err := h.client.Write(ctx, decryptPath, map[string]any{
        "ciphertext": string(edk),  // Vault ciphertext string
    })
    if err != nil { return nil, errcode.Wrap(...) }
    dek, _ := base64.StdEncoding.DecodeString(result["plaintext"].(string))

    // 2. DEK 本地 AES-GCM 解密 + AAD 校验（跨行复制攻击在此被挡）
    return aesGCMDecrypt(dek, ciphertext, nonce, aad)
}
```

**收益**：
- ✅ 明文不过外部边界（Vault 只见 32 字节 DEK）
- ✅ AAD 在本层 `cipher.AEAD.Seal/Open` 生效，与 LocalAES 完全对齐
- ✅ `context` vs `associated_data` 字段 bug 消失（不再依赖 Vault 的 AAD 处理）
- ✅ keyID 从 Vault ciphertext 前缀 `vault:vN:...` 提取（保持现有语义）
- ✅ 接口统一：所有 provider 都返回 `(ct, nonce, edk)` 三元组

### 文件清单

**新建**：
- `adapters/vault/doc.go`
- `adapters/vault/transit_provider.go` — 从 `runtime/crypto/vault_transit_provider.go` 搬入 + envelope 重写
- `adapters/vault/client_adapter.go` — 从 `runtime/crypto/vault_client_adapter.go` 搬入
- `adapters/vault/integration_test.go` — testcontainers Vault dev，验证：
  - AAD 正确 → 解密成功
  - AAD 错误 → `ErrKeyProviderDecryptFailed`（**跨行复制攻击被挡**）
  - Rotate 后旧 ciphertext 仍可解密
  - Vault 服务端日志确认 **wrap/unwrap 的 plaintext 是 32 字节随机值**（不是业务 plaintext）

**删除**：
- `runtime/crypto/vault_transit_provider.go`
- `runtime/crypto/vault_client_adapter.go`
- `runtime/crypto/vault_transit_provider_test.go`
- `runtime/crypto/vault_transit_unit_test.go`

**修改**：
- `cmd/core-bundle/bundle.go:509` — `crypto.NewVaultTransitKeyProviderFromEnv()` → `vault.NewTransitKeyProviderFromEnv()`

### 集成测试骨架

```go
// adapters/vault/integration_test.go
//go:build integration

func TestTransitEnvelope_AADBindingAgainstRealVault(t *testing.T) {
    ctx := context.Background()
    container, client := startVaultContainer(t, ctx) // testcontainers
    defer container.Terminate(ctx)

    provider := NewTransitKeyProvider(client, "transit", "gocell-test-key")
    handle, _ := provider.Current(ctx)

    aad1 := []byte("cell:config-core/key:db_password")
    aad2 := []byte("cell:config-core/key:api_secret")

    ct, nonce, edk, err := handle.Encrypt(ctx, []byte("secret-value"), aad1)
    require.NoError(t, err)

    // 正确 AAD → 成功
    pt, err := handle.Decrypt(ctx, ct, nonce, edk, aad1)
    require.NoError(t, err)
    require.Equal(t, "secret-value", string(pt))

    // 错误 AAD（跨行复制模拟）→ 失败
    _, err = handle.Decrypt(ctx, ct, nonce, edk, aad2)
    require.ErrorIs(t, err, errcode.ErrKeyProviderDecryptFailed)

    // 验证 Vault 审计日志：encrypt 调用的 plaintext 是 base64(32 字节)，
    // 不含 "secret-value" 字符串（明文不过边界）
    // （通过 Vault audit 文件或 request recording 验证）
}
```

### Commit 模板

```
feat(vault): envelope encryption + adapters/vault relocation

修复两个关联问题：

1. 安全 (S1 P0): VaultTransit 将 AAD 传给 Vault API 的 "context" 字段，
   但对 aes256-gcm96 + non-derived key，"context" 被 Vault 忽略 →
   跨行密文复制攻击无法被拦截。

2. 架构 (A1): VaultTransit 是 Vault HTTP adapter 而非 runtime 通用实现，
   之前错置于 runtime/crypto/ 违反 "adapters 实现 runtime/kernel 接口" 规则。

解决方案（对标 kubernetes KMS v2）：
- envelope encryption: 本地生成 DEK, DEK 本地 AES-GCM 加密 + AAD 在
  cipher.AEAD.Seal/Open 生效，Vault 只负责 wrap DEK (32 字节随机)。
- 文件从 runtime/crypto/ 迁至 adapters/vault/。
- 新增 testcontainers 集成测试：AAD 错误必须解密失败；Vault audit 日志
  验证明文不过外部边界。

backlog: S1 (P0) + A1 + S14a adapters/vault 路径就位

ref: kubernetes/apiserver pkg/storage/value/envelope/kmsv2 — DEK 本层 AEAD
ref: hashicorp/vault api-docs/transit — associated_data vs context 区别

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
```

---

## 8. R1d 详细：CellModule + AppDeps 解体

### CellModule 接口

```go
// runtime/bootstrap/cell_module.go

// CellModule 是 Cell 向 BuildApp 自报家门的契约。每个 Cell 对应一个
// *_module.go 文件，声明自己需要哪些 SharedDeps，构造 cell.Cell 与
// assembly option。
//
// ref: uber-go/fx fx.Module(name, opts...) — 每个 Module 封装一个领域的
//      Provide/Invoke；主入口只拼 Module 列表，不感知内部依赖。
// ref: google/guice AbstractModule.configure() — 同模式
type CellModule interface {
    // ID 返回 cell 标识（供日志/错误标注）。
    ID() string

    // Provide 构造 cell.Cell 与 bootstrap options。shared 只暴露横切依赖
    // （Topology/JWT/Prom/EventBus/HMACKey），cell-specific 配置（如
    // KeyProvider）由 module 自行从 env 读取。
    //
    // 返回：(cell 实例, 本 cell 专属的 bootstrap options, error)
    Provide(ctx context.Context, shared *SharedDeps) (cell.Cell, []Option, error)
}

// BuildApp 是 main + integration test 的共享装配入口。
//
// ref: uber-go/fx fx.New(options...) — 单一入口构造，支持测试与生产共享。
// ref: backlog S29 CORE-BUNDLE-APP-BUILDER-01
func BuildApp(ctx context.Context, shared *SharedDeps, modules ...CellModule) (*Bootstrap, error) {
    if err := shared.Validate(); err != nil {
        return nil, err
    }

    var cells []cell.Cell
    var opts []Option
    for _, m := range modules {
        c, cellOpts, err := m.Provide(ctx, shared)
        if err != nil {
            return nil, fmt.Errorf("module %s: %w", m.ID(), err)
        }
        cells = append(cells, c)
        opts = append(opts, cellOpts...)
    }

    asm, err := assembly.NewCoreAssembly(shared.PromStack.registry, cells...)
    if err != nil {
        return nil, err
    }
    opts = append(opts, WithAssembly(asm), /* 通用 options */)
    return New(opts...), nil
}
```

### SharedDeps（原 AppDeps 横切部分）

```go
// cmd/core-bundle/shared_deps.go
// SharedDeps 是所有 Cell 共享的横切依赖。每个 Cell 的 module 通过
// Provide(ctx, shared) 读取需要的字段，不再有 God Struct。
type SharedDeps struct {
    Topology       bootstrap.Topology
    JWTDeps        jwtDeps
    PromStack      promStack
    CursorCodecs   cursorCodecs
    HMACKey        []byte
    EventBus       *eventbus.InMemoryEventBus
    InternalGuard  func(http.Handler) http.Handler
    MetricsToken   string
    VerboseToken   string
}

func (s *SharedDeps) Validate() error { /* 聚合校验，已实现 */ }
```

### 三个 Cell Module 文件

```go
// cmd/core-bundle/config_module.go
type ConfigCoreModule struct{}

func (ConfigCoreModule) ID() string { return "config-core" }

func (ConfigCoreModule) Provide(ctx context.Context, s *SharedDeps) (cell.Cell, []bootstrap.Option, error) {
    // Cell-specific wiring：KeyProvider/PGResource 由 module 自管
    kp, err := buildKeyProvider(s.Topology.StorageBackend)
    if err != nil { return nil, nil, err }
    vt := keyProviderToTransformer(kp)

    pgRes, cellOpts, err := buildPostgresBacking(ctx, s.Topology, s.EventBus, s.PromStack.metricProvider, vt)
    if err != nil { return nil, nil, err }

    c := configcore.NewConfigCore(append([]configcore.Option{
        configcore.WithPublisher(s.EventBus),
        configcore.WithCursorCodec(s.CursorCodecs.config),
        configcore.WithValueTransformer(vt),
    }, cellOpts...)...)

    var opts []bootstrap.Option
    if pgRes != nil {
        opts = append(opts, bootstrap.WithManagedResource(pgRes))
    }
    return c, opts, nil
}

// 同样 access_module.go / audit_module.go
```

### main.go

```go
// cmd/core-bundle/main.go (精简后 ~30 行)
func main() {
    ctx := context.Background()
    shared, err := LoadSharedDeps(ctx)
    if err != nil { log.Fatal(err) }

    app, err := bootstrap.BuildApp(ctx, shared,
        ConfigCoreModule{},
        AccessCoreModule{},
        AuditCoreModule{},
    )
    if err != nil { log.Fatal(err) }

    if err := app.Run(ctx); err != nil { log.Fatal(err) }
}
```

### flagwrite.NewService 签名修正

```go
// cells/config-core/slices/flagwrite/service.go
// 当前: func NewService(repo, logger, opts...) *Service { panic(...) }
// 改为:
func NewService(repo ports.FlagRepository, logger *slog.Logger, opts ...Option) (*Service, error) {
    s := &Service{repo: repo, logger: logger}
    for _, opt := range opts { opt(s) }

    // XOR 校验
    if (s.outboxWriter == nil) != (s.txRunner == nil) {
        return nil, errcode.New(errcode.ErrCellMissingOutbox,
            "outboxWriter and txRunner must both be set or both be nil (L2 coupling)")
    }
    return s, nil
}
```

### 文件清单

**新建**：
- `runtime/bootstrap/cell_module.go`
- `runtime/bootstrap/app.go`
- `cmd/core-bundle/shared_deps.go`
- `cmd/core-bundle/config_module.go`
- `cmd/core-bundle/access_module.go`
- `cmd/core-bundle/audit_module.go`

**修改**：
- `cmd/core-bundle/main.go` — 精简至 module 拼接
- `cmd/core-bundle/bundle.go` — 拆出 SharedDeps 后减半
- `cells/config-core/slices/flagwrite/service.go:67` — panic → return error
- `cells/config-core/config_core.go` — 调用 `NewService` 处处理 error

**删除**：
- `cmd/core-bundle/bundle.go` 的 `AppDeps.Validate()` / `AppDepsFromEnv()` / `BuildBootstrap()` 合并到 SharedDeps 与 BuildApp

### 验收

- `go build ./cmd/core-bundle/...` 通过
- `go test -tags=integration ./cmd/core-bundle/...` 所有测试通过（测试调用 `BuildApp(shared, modules...)` 而非 `BuildBootstrap(deps)`）
- `AppDeps` 类型不再存在
- 新增 Cell 的步骤：`touch new_cell_module.go` + `main.go` 加一行 `NewCellModule{}`

---

## 9. R1e 详细：治理扫尾

### 任务清单

1. **PoolStats 接口统一**（A7）
   - 检查 `adapters/redis/` 与 `adapters/rabbitmq/` 是否有 PoolStats 实现
   - 若有，抽共同接口 `kernel/observability/pool_stats.go: type PoolStats interface { ActiveConns() int; IdleConns() int; ... }`
   - 若无则标记为 "pg-only" 保留 backlog，不强制统一

2. **Slice 目录 kebab-case 幻影清理**（A2）
   - 扫描 `cells/*/slices/` 下所有 kebab-case 目录（如 `config-read/` 同时存在 `configread/`）
   - 确认 `slice.yaml.allowedFiles` 实际路径后删除旧版
   - 更新 `gocell scaffold slice` 模板统一 no-dash 命名

3. **`gocell validate --strict` CI**
   - 新增 CI job，独立于现有 build/test
   - 失败阻断 merge

4. **`docs/contributing/adapter-checklist.md`**
   - 新增 adapter 时强制 checklist：
     - [ ] 接口定义在 `kernel/` 或 `runtime/`（不在 adapter 包内）
     - [ ] 实现 `kernel/lifecycle.ManagedResource`（自管 lifecycle）
     - [ ] 真实容器集成测试（`//go:build integration`）
     - [ ] 对外协议字段 cross-check upstream API 文档（引用 URL 写注释）
     - [ ] Rotation/token 续期路径在 backlog 登记（即使延后实现）
     - [ ] fail-fast smoke test（构造期验证连通性 + 认证）

### 验收

- `gocell validate --strict` CI 0 warning
- `grep -r "kebab-case 目录" cells/*/slices/` 无结果
- `docs/contributing/adapter-checklist.md` 存在

---

## 10. R2 详细：安全修复

### 任务清单（P0 + P1 安全）

| # | Finding | 修复点 |
|---|---------|-------|
| S1 fallback | VaultTransit AAD（已在 R1c envelope 重写中根治）| 确认 R1c 集成测试覆盖跨行复制攻击 |
| S2 | `GOCELL_MASTER_KEY` 未纳入 `rejectDemoKey` | `cmd/core-bundle/bundle.go::buildKeyProvider` local-aes 分支追加 `rejectDemoKey(adapterMode, "GOCELL_MASTER_KEY", masterKeyBytes)` |
| S3 | `plaintextMigrator` TOCTOU | UPDATE 语句追加 `AND value_cipher IS NULL` 守卫；并发 writer 不会覆盖已加密行 |
| S4 | VAULT_TOKEN 静态模式 real-mode guard | `vault.NewTransitKeyProviderFromEnv` 开头 `if adapterMode == "real" && authMode == "token" → error` |
| A3 | `AppDeps.Validate()` 对 KeyProvider 无存在性强制 | R1d 已把 Validate 移到 SharedDeps；新增 `if Topology.StorageBackend == "postgres" && KeyProvider == nil → error`（但 R1d 中 buildKeyProvider 已 fail-fast，这里是 struct-literal test 路径的防线）|
| O3 | migration 010 Down 无守卫 | 010 Down 追加 `RAISE EXCEPTION 'migration 010 rollback drops encrypted data; manual DBA action required'` |
| P1 | Toggle API 语义冲突 | 暂缓（产品决策，R2 范围外，转 backlog P1 项）|
| M3 | Stale 可观测 | `config_repo.go` Decrypt 后检测 `storedKeyID != currentKeyID` → `slog.Warn("config value encrypted with stale key", cellID, key, stored_key_id, current_key_id)` + `metric_config_stale_cipher_total.Inc()` |

### 文件清单

**修改**：
- `cmd/core-bundle/bundle.go` (R1d 后变为 `shared_deps.go` + `config_module.go`) — 添加 S2/S4 guard
- `cells/config-core/internal/adapters/postgres/plaintext_migration.go` — S3 TOCTOU 守卫
- `adapters/postgres/migrations/010_add_config_value_cipher.sql` — S3 Down RAISE EXCEPTION
- `cells/config-core/internal/adapters/postgres/config_repo.go` — M3 stale slog.Warn + 指标

**新建**：
- `cmd/core-bundle/security_integration_test.go`（可选）— table-driven 覆盖 S2/S4 拒绝路径

### 验收

- `adapterMode=real + GOCELL_MASTER_KEY=010203...（已知测试密钥）→ startup error`
- `adapterMode=real + VAULT_TOKEN=hvs.xxx → startup error（要求 AppRole/K8s auth）`
- `goose down 010 → ERROR`（手动执行需 DBA 改 migration）
- 并发 plaintext migration + config write → 已加密行不被覆盖（testcontainer 验证）

---

## 11. 验收标准（整体）

### 分层规则 CI（持续防退化）

```yaml
# .github/workflows/layering.yml
- name: adapters 不依赖 runtime/bootstrap
  run: |
    if go list -deps ./adapters/... | grep -q 'runtime/bootstrap$'; then
      echo "❌ adapter 直接依赖 runtime/bootstrap，违反分层规则"
      exit 1
    fi

- name: kernel 不依赖 runtime/adapters
  run: |
    if go list -deps ./kernel/... | grep -E 'runtime/|adapters/'; then
      echo "❌ kernel 依赖 runtime/ 或 adapters/"
      exit 1
    fi
```

### 功能验收

- [ ] `go build ./...` 零错误
- [ ] `go test ./...` 全部通过（含 `-tags=integration`）
- [ ] `go test -tags=integration ./adapters/vault/...` 真实 Vault 容器测试通过（AAD binding + envelope 验证）
- [ ] `gocell validate --strict` 0 warning
- [ ] 审查报告 27 条 finding 中 P0/P1 全部关闭（P0: S1/S2/S3/S4/A3；P1: A1/A2/A4/M1/M2/M3/T1/T2/T3/P1/P2 → 除 P1/P2（产品决策）外全部关闭）
- [ ] backlog 条目 S17/S29/A1/A4/M1/M2/M3/S2/S3/S4/A3/O3 标记 ✅

### 性能验收（防回归）

- [ ] envelope 模式下 VaultTransit encrypt latency ≤ 原实现 + 20%（测一次 Vault RTT，LocalAES wrap DEK 增量可忽略）
- [ ] LocalAES encrypt/decrypt latency 不变（接口迁移无语义变化）

---

## 12. 风险与回滚

| 风险 | 概率 | 影响 | 缓解 |
|-----|-----|-----|------|
| R1c envelope 重写破坏已加密数据 | 低 | 高 | 当前 sensitive 数据量 ≈ 0（dev/test，migration 010 刚落）；迁移前 LocalAES/VaultTransit 之间无兼容性（两者独立 provider），不存在 cross-provider 已加密数据 |
| Vault testcontainer CI 不稳定 | 中 | 中 | 使用 `hashicorp/vault:1.15.0` 固定 tag+digest；重试策略；失败降级为 `t.Skipf` 但 dev 环境必跑 |
| R1d CellModule 改动面大 | 中 | 中 | 保持 Provide 返回签名稳定；现有测试逐条迁移验证 BuildApp 语义等价 |
| R1a kernel/worker 迁移引发大量 import 改动 | 低 | 低 | 单次 IDE 全局替换；编译器保证零遗漏 |

### 回滚策略

- 每个 PR 独立可回滚（`git revert`）
- R1c 回滚后 envelope bug 复活 → 紧急 hot-fix PR 只改 `context` → `associated_data`（1 行）作为 band-aid
- R1d 回滚后恢复 AppDeps 结构；无数据影响

---

## 13. 与 backlog 的映射

### 本 plan 完全消化（合并后从 backlog 删除）

| backlog 条目 | PR |
|-------------|-----|
| S17 POOL-FRAMEWORK-LIFECYCLE-01 | R1a |
| S29 CORE-BUNDLE-APP-BUILDER-01 | R1d |
| S12 AUTH-GUARD-INLINE-UNIFY-01 | 已 ✅（无关本 plan）|
| A3 AppDeps KeyProvider 存在性 | R2 |
| S2 GOCELL_MASTER_KEY demo key | R2 |
| S3 plaintextMigrator TOCTOU | R2 |
| S4 VAULT_TOKEN static real-mode | R2 |
| A1 VaultTransit 放错层 | R1c |
| S1 VaultTransit `context` AAD bug | R1c（envelope 根治）|
| M1 AppDeps God Struct | R1d |
| M2 flagwrite.NewService panic | R1d |
| M3 Stale 可观测 | R2 |
| O3 migration 010 Down RAISE | R2 |
| A2 kebab-case slice 目录 | R1e |
| A4 ManagedResource 宿主包 | R1a |

### 本 plan 推进（条件触发转 backlog 可见）

| backlog 条目 | 说明 |
|-------------|------|
| S14a AWS-KMS / GCP-KMS adapter | R1c 建立 adapters/vault/ 后，adapters/awskms/ + gcpkms/ 路径确定；envelope 模式下新 KMS = 实现 KMS interface wrap/unwrap 即可（~2-3h/个，而非原估 6h）|

### 本 plan 不处理（保留 backlog）

- S14a 实际 AWS-KMS/GCP-KMS 实现（等生产云平台选定）
- P1/P2 Toggle API 语义（产品决策）
- S19-S23 JWT audience 系列（独立链条）
- S10 RunMode 读写类型分离（纯类型层，独立）
- S16 Topology 彻底单一事实源（本 plan 未触及）
- X10 AUTH-REFRESH-OPAQUE-01（依赖 X1 PG-DOMAIN-REPO）

---

## 14. 施工检查清单

施工前确认：
- [ ] 读本 plan 并在 issue/PR 描述中引用
- [ ] 确认 2026-04-20 当前 develop 分支干净（无未 merge 改动）
- [ ] 为每个 PR 创建独立 worktree（`.claude/rules/git-worktree` 规范）

每个 PR 合入前检查：
- [ ] 本地 `golangci-lint run` 0 issues
- [ ] 本地 `go build -tags=integration ./...` 0 错误
- [ ] commit message 引用 backlog 条目 + 对标框架 `ref:`
- [ ] 若改公共接口签名，push 前 `go build -tags=integration ./...`

全部合入后：
- [ ] 更新 `docs/backlog.md`（按"本 plan 消化"清单标 ✅）
- [ ] 归档 `docs/reviews/archive/`
- [ ] CHANGELOG.md 更新 v1.0-rc 条目

---

## 附录 A：对标框架引用

本 plan 的架构决策基于 3 个研究员报告（kubernetes KMS v2 / uber-go/fx / hashicorp/vault 生态）：

- [kubernetes/kubernetes pkg/storage/value/transformer.go](https://github.com/kubernetes/kubernetes/blob/master/staging/src/k8s.io/apiserver/pkg/storage/value/transformer.go) — Transformer 接口三层分离（接口/标准实现/外部 plugin）
- [kubernetes/kubernetes kmsv2/envelope.go](https://github.com/kubernetes/kubernetes/blob/master/staging/src/k8s.io/apiserver/pkg/storage/value/encrypt/envelope/kmsv2/envelope.go) — envelope encryption DEK/KEK 分离
- [uber-go/fx lifecycle.go](https://github.com/uber-go/fx/blob/v1.23.0/lifecycle.go) — Lifecycle interface 在顶层公开包
- [uber-go/fx module.go](https://github.com/uber-go/fx/blob/v1.23.0/module.go) — Module 模式避免 God Struct
- [hashicorp/vault api-docs/transit#encrypt-data](https://developer.hashicorp.com/vault/api-docs/secret/transit) — `context` vs `associated_data` 字段语义
- [external-secrets/external-secrets pkg/provider/vault](https://github.com/external-secrets/external-secrets/tree/main/pkg/provider/vault) — Kubernetes auth 替代静态 token

---

**本 plan 作者**：Claude + 用户对齐决策（2026-04-20）
**状态**：待用户最终批准后开工
