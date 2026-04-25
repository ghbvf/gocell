# PR #269 — 架构合规评审

- **PR**: https://github.com/ghbvf/gocell/pull/269
- **角度**: Architecture（GoCell 分层 + 接口稳定性 + Cell 模型）
- **Worktree**: `/Users/shengming/Documents/code/gocell/worktrees/536-pr262-auth-typed-plan`
- **Plan**: `~/.claude/plans/l4-docs-backlog-md-pr262-auth-policy-pl-nested-salamander.md`
- **范围**: 仅架构（不审实现细节、测试覆盖、文案）
- **裁决日期**: 2026-04-25

---

## 总体裁决：**通过（CONDITIONAL APPROVE）**

### 核心结论

1. **分层依赖** — kernel/cell 严格只依赖 `crypto/sha256` `sync/atomic` `context` `time`，未 import `runtime/auth`。✅
2. **Sealed interface 模式** — 三层 marker 方法（`authPlanKind` / `listenerAuthOK` / `groupAuthOK`）全部 unexported，编译期阻止外部实现，闭枚举正确。✅
3. **JWT in chain 编译期约束 + phase0 单例校验** — 双重保护到位。✅
4. **API 稳定性** — 7 个旧 `bootstrap.PolicyXxx` 工厂全删，调用方（cmd/corebundle、3 examples）已迁移。✅
5. **Cell-pattern 合规** — `cells/` 不构造 ListenerAuth chain，全部由 composition root（cmd/、examples/）装配；archtest LAYER-09 落地。✅

但有 **1 个 P1 设计冗余** + **2 个 P2 archtest/接口对齐改进点**，建议本 PR 内修，不阻塞合并。

---

## Findings 表

| # | 标题 | 文件:行 | Cx | IN_SCOPE | 优先级 |
|---|------|---------|----|----------|--------|
| F1 | `cell.AuthProvider` 接口已声明但 bootstrap dispatch 路径仍用 `auth.IntentTokenVerifier` 私有 `authProvider`，存在两套接口冗余 | `kernel/cell/auth_types.go:111-113` + `runtime/bootstrap/auth_plan_apply.go:29-36` | C2 | YES | P1 |
| F2 | archtest LAYER-09 的 `examples/` 白名单是 dead code（只扫 `cells/`，不会命中 `examples/` 前缀） | `tools/archtest/auth_plan_test.go:351-354` | C5 | YES | P2 |
| F3 | `auth.IntentTokenVerifier` 在 runtime/auth 是 **interface 重复声明**而非 `type alias = cell.IntentTokenVerifier`，与同文件 `Claims = cell.Claims`、`TokenIntent = cell.TokenIntent` 不一致 | `runtime/auth/auth.go:46-52` | C3 | YES | P2 |
| F4 | `kernel/cell/auth_types.go:121,124` 的 `var _ = NonceStoreKind("")` / `var _ = TokenIntent("")` 注释自称"satisfies linter"但实际是 dead code，应删除 | `kernel/cell/auth_types.go:121,124` | C5 | YES | P3 |

---

## 架构维度审查

### 1. 分层架构 ✅

**kernel/cell/auth_plan.go imports**: `crypto/sha256` + `sync/atomic` （L27-30），无 `runtime/*` import。
**kernel/cell/auth_types.go imports**: `context` + `time` （L16-19），无 `runtime/*` import。

依赖方向正确：
- `runtime/auth/nonce.go:10` import `kernel/cell`（runtime → kernel ✅）
- `runtime/auth/auth.go:11` import `kernel/cell`（runtime → kernel ✅）
- `runtime/auth/authenticator.go:19` import `kernel/cell`（runtime → kernel ✅）
- `runtime/bootstrap/auth_plan_apply.go:21-26` import `kernel/cell` + `runtime/auth`（runtime/bootstrap → kernel + runtime/auth ✅）

**结论**：kernel/cell 保持纯净；runtime/auth 反向依赖 kernel/cell 是预期方向。

### 2. Cell 聚合边界 ✅

- `cells/accesscore/cell_providers.go:43-48` 暴露 `TokenVerifier() auth.IntentTokenVerifier` —— 这是服务 provider，不是构造 AuthPlan，符合 LAYER-09。
- `cells/configcore/cell.go`、`cells/auditcore/cell.go` 不暴露 TokenVerifier，不参与 auth plan 装配。
- `cmd/corebundle/bundle.go:118` `bootstrap.WithListener(cell.PrimaryListener, ..., []cell.ListenerAuth{cell.NewAuthJWTFromAssembly(asm)})` —— composition root 唯一装配点，AuthJWTFromAssembly 在 phase4 通过 `discoverAuthVerifierFromAssembly` 反向发现实现。

archtest LAYER-09（`tools/archtest/auth_plan_test.go:333-397`）扫描 `cells/` 下所有 `*.go`，禁止 6 个 AuthPlan 类型的 composite literal 构造。规则正确。

### 3. 接口稳定性 ✅

**删除项核对（plan §Batch 3）**：
- `kernel/cell/policy.go` —— deleted ✅（Read 返回 file does not exist）
- `runtime/bootstrap/policy.go|policy_jwt.go|policy_jwt_from_assembly.go|policy_mtls.go|policy_servicetoken.go|policy_verbose_token.go|policy_none.go` —— 通过 archtest AUTH-PLAN-02 防回归（禁 `bootstrap.Policy*` 七大 selector）

**新增导出 API**：
- `kernel/cell.AuthPlan / ListenerAuth / GroupAuth / AuthKind` 接口 + 6 个常量
- `cell.AuthNone{} / AuthJWT / AuthJWTFromAssembly / AuthMTLS / AuthServiceToken / AuthVerboseToken` struct
- 6 个 constructor: `NewAuthJWT / NewAuthJWTFromAssembly / NewAuthServiceToken / NewAuthVerboseToken`（前 4 + AuthNone/AuthMTLS 用 zero-value）
- `cell.AssemblyRef`（最小接口避免 import cycle）
- `cell.IntentTokenVerifier / Claims / TokenIntent / NonceStore / NonceStoreKind / HMACKeyring / AuthProvider`（kernel 窄接口）
- `bootstrap.WithListener` 第三参数 → `[]cell.ListenerAuth`
- `bootstrap.WithLivezAuth / WithReadyzAuth / WithMetricsAuth(cell.GroupAuth)`

GoCell 当前阶段允许 hard break（CLAUDE.md "Review 和重构时不考虑向后兼容"），所有调用方已在同 PR 切换。✅

### 4. 一致性级别 ✅

不涉及 CUD 操作，无新 L0-L4 标注。

### 5. 性能与可扩展性 ✅

- `applyListenerAuthChain` 是装配期的一次性 type switch，无热路径开销。
- `AuthJWTFromAssembly.resolved *atomic.Pointer[IntentTokenVerifier]` 用 lock-free 读取 verifier，OK。
- `AuthVerboseToken.HashedToken [32]byte` 在构造期一次 SHA-256，constant-time compare 在请求期，OK。
- `discoverAuthVerifierFromAssembly`（auth_plan_apply.go:155-189）按 `CellIDs()` 顺序遍历，O(N)，可接受（assembly cell 数 ≤ 几十）。

### 6. 依赖方向 ✅

无逆向依赖。kernel/cell 纯净；runtime/bootstrap 单向依赖 kernel/cell + runtime/auth。

---

## Plan vs Wiring 分离审查（Watermill FacadeConfig 模式）

**期望**：`cell.AuthPlan` 是声明性数据；中间件构造发生在 bootstrap 内部。

**实际**：
- `kernel/cell/auth_plan.go` —— 仅持有结构体字段 + sealed marker + Describe()，无 HTTP middleware 构造代码。✅
- `runtime/bootstrap/auth_plan_apply.go:218-229` `mtlsMiddleware()` / `:256-273` `verboseTokenMiddleware()` 是真正的 wiring，集中在 bootstrap 一侧。✅
- `AuthJWTFromAssembly.SetResolved()`（auth_plan.go:198）由 bootstrap 在 phase4 写入，cell 代码不调用。这个**setter 暴露在 kernel/cell 公共 API**有边界泄漏嫌疑，但因为 sealed interface 已防止外部实现 `AuthPlan`，且明确注释"must not be called by cell code"，可接受。

### 边界泄漏小议（不构成 finding）

`AuthJWTFromAssembly.SetResolved` 是 bootstrap 写入路径在 kernel/ 暴露的钩子。理想是把 resolved 缓存放进 bootstrap 内部的 map（`map[ListenerRef]auth.IntentTokenVerifier`）而不是 plan struct 本身，但当前设计因 plan 在 chain 中按 value 传递、原子指针保证多副本可见性，已是 pragmatic 方案。可在 PR262 之后的清理 PR 中重构（不本 PR 阻塞）。

---

## archtest 完整性审查

四条规则：

| Rule | 范围 | 白名单 | 评估 |
|------|------|--------|------|
| AUTH-PLAN-01 | 字面量 `"jwt"` `"mtls"` `"service-token"` `"stack["` | `kernel/cell/auth_plan.go`、`runtime/bootstrap/auth_plan_apply.go`、`runtime/bootstrap/auth_plan_describe.go`、`runtime/auth/authenticator.go`、`adapters/vault/auth.go` | ✅ 白名单合理；`runtime/auth/authenticator.go` 用 `"jwt"` 作 AuthMethod 标签是观测字段非 dispatch；`adapters/vault/auth.go` 是 Vault policy 名 |
| AUTH-PLAN-02 | `bootstrap.PolicyJWT / PolicyJWTFromAssembly / PolicyMTLS / PolicyServiceToken / PolicyVerboseToken / PolicyNone / PolicyStack` selector | 无 | ✅ 7 个名字全覆盖 |
| AUTH-PLAN-03 | `cell.Policy{}` composite literal + type reference | 无 | ✅ |
| AUTH-PLAN-04 (LAYER-09) | `cells/` 下不构造 6 个 AuthPlan 类型 | "examples/" 前缀 | ⚠️ examples/ 白名单是 dead code（findProductionGoFilesInDir 只走 `cells/`，不会出现 examples/ 前缀），见 F2 |

三条 fixture self-test（TestAuthPlan_Fixtures_Rule01/02/03）证明扫描器有效性。建议补一条 fixture for Rule04。

---

## 发现详情

### F1 [接口冗余 / P1] `cell.AuthProvider` vs bootstrap-private `authProvider`

- `kernel/cell/auth_types.go:111-113` 声明 `cell.AuthProvider interface { TokenVerifier() IntentTokenVerifier }`
- `runtime/bootstrap/auth_plan_apply.go:34-36` 声明 `authProvider interface { TokenVerifier() auth.IntentTokenVerifier }`（私有）
- `runtime/bootstrap/auth_plan_apply.go:201-208` `asmCellLookup` 用的是私有 `authProvider`
- `cells/accesscore/cell_providers.go:43` 实现的也是结构性满足两者

**问题**：`cell.AuthProvider` 在 kernel 声明后**实际未被任何代码引用**（dispatch 走私有 `authProvider`）。两者结构相同但分别用 `cell.IntentTokenVerifier` / `auth.IntentTokenVerifier` 返回类型，造成意图与实现脱节。

**建议**（择一）：
- (a) 删除 `cell.AuthProvider`（kernel 不暴露 cell 应实现的可选接口），或
- (b) 把 bootstrap 私有 `authProvider` 改为引用 `cell.AuthProvider`，统一收窄到 kernel 接口。

推荐 (b)：`asmCellLookup` 返回类型改 `cell.AuthProvider`，调用 `ap.TokenVerifier()` 拿到 `cell.IntentTokenVerifier`，传给 `buildAuthRouterOptions(v auth.IntentTokenVerifier)` 时利用结构性子类型（Go 自动满足）。这样 kernel 暴露的接口都是真有调用方。

**影响**：低（仅类型清理，不改运行行为）。

### F2 [archtest dead code / P2] LAYER-09 examples/ 白名单失效

- `tools/archtest/auth_plan_test.go:336-337` `cellsDir := filepath.Join(root, "cells")` + `findProductionGoFilesInDir(cellsDir)` 只在 cells/ 子树扫描
- L351-354 注释写"examples/ 是白名单"，但 `rel` 永远以 `cells/` 开头，永远走不到 `strings.HasPrefix(rel, "examples/")` 为 true 的分支

**建议**：
- 要么扩大扫描范围（用 `findAllProductionGoFiles` + 显式跳过 `examples/`、`cmd/`、`kernel/cell/auth_plan.go`、`runtime/bootstrap/auth_plan_apply.go`），保证未来 `pkg/` 也覆盖；
- 要么删除 examples/ 跳过分支 + 注释更新为"扫描仅限 cells/"。

**影响**：低（当前 cells/ 内构造在所有 commits 都是 0，规则有效；只是注释误导）。

### F3 [接口对齐 / P2] `auth.IntentTokenVerifier` 应改为 type alias

- `runtime/auth/auth.go:32` `type Claims = cell.Claims`（alias）
- `runtime/auth/auth.go:21` `type TokenIntent = cell.TokenIntent`（alias）
- `runtime/auth/nonce.go:36` `type NonceStoreKind = cell.NonceStoreKind`（alias）
- 但 `runtime/auth/auth.go:46-52` `IntentTokenVerifier` 是**重复 interface 声明**（结构性满足 cell 接口，但是不同的命名类型）

**问题**：当前 `cells/accesscore/cell_providers.go:43` 返回 `auth.IntentTokenVerifier`，但通过 type assertion 与 `bootstrap` 私有 `authProvider`（也是 auth.IntentTokenVerifier）匹配。如果将来 `cell.IntentTokenVerifier` 加方法但 `auth.IntentTokenVerifier` 没加，两者会无声漂移。

**建议**：
```go
// runtime/auth/auth.go
type IntentTokenVerifier = cell.IntentTokenVerifier
```
配合统一 `Claims` / `TokenIntent` / `NonceStoreKind` 的 alias 风格。

**影响**：低（语义零差异，只是消除"两套接口"的认知开销）。

### F4 [dead code / P3] kernel/cell/auth_types.go 末尾的 self-reference vars

- L121 `var _ = NonceStoreKind("") // zero-value self-reference, satisfies linter`
- L124 `var _ = TokenIntent("") // zero-value self-reference, satisfies linter`

注释声称"satisfies linter"，但 unused-import / unused-type lint 检查不会因为这两个 `var _ =` 改变结果（这些类型已被同文件其他地方使用）。这是 dead code。

**建议**：删除两行 + 上方 13 行注释。

**影响**：极低（清洁度）。

---

## 是否需要 backlog 后续条目

- **F1**: 建议本 PR 修；如不修，记入 backlog `cleanup-plan` 已有的"AuthProvider 接口收窄"条目。
- **F2/F4**: 建议本 PR 修（10 行内改动）。
- **F3**: 建议本 PR 修（1 行 alias 替换）。
- **AuthJWTFromAssembly.SetResolved kernel 边界泄漏**：不本 PR 修，记入 `docs/plans/202604252100-026-post-v1.0-cleanup-plan.md` 评估"plan resolved 缓存搬到 bootstrap 内部 map"。

---

## 关键文件 archive 链

- `/Users/shengming/Documents/code/gocell/worktrees/536-pr262-auth-typed-plan/kernel/cell/auth_plan.go`
- `/Users/shengming/Documents/code/gocell/worktrees/536-pr262-auth-typed-plan/kernel/cell/auth_types.go`
- `/Users/shengming/Documents/code/gocell/worktrees/536-pr262-auth-typed-plan/kernel/cell/routegroup.go`
- `/Users/shengming/Documents/code/gocell/worktrees/536-pr262-auth-typed-plan/runtime/bootstrap/auth_plan_apply.go`
- `/Users/shengming/Documents/code/gocell/worktrees/536-pr262-auth-typed-plan/runtime/bootstrap/auth_plan_validate.go`
- `/Users/shengming/Documents/code/gocell/worktrees/536-pr262-auth-typed-plan/runtime/bootstrap/auth_plan_describe.go`
- `/Users/shengming/Documents/code/gocell/worktrees/536-pr262-auth-typed-plan/runtime/bootstrap/listener.go`
- `/Users/shengming/Documents/code/gocell/worktrees/536-pr262-auth-typed-plan/runtime/bootstrap/health.go`
- `/Users/shengming/Documents/code/gocell/worktrees/536-pr262-auth-typed-plan/runtime/auth/auth.go`
- `/Users/shengming/Documents/code/gocell/worktrees/536-pr262-auth-typed-plan/runtime/auth/authenticator.go`
- `/Users/shengming/Documents/code/gocell/worktrees/536-pr262-auth-typed-plan/runtime/auth/nonce.go`
- `/Users/shengming/Documents/code/gocell/worktrees/536-pr262-auth-typed-plan/runtime/auth/servicetoken.go`
- `/Users/shengming/Documents/code/gocell/worktrees/536-pr262-auth-typed-plan/cells/accesscore/cell_providers.go`
- `/Users/shengming/Documents/code/gocell/worktrees/536-pr262-auth-typed-plan/cmd/corebundle/bundle.go`
- `/Users/shengming/Documents/code/gocell/worktrees/536-pr262-auth-typed-plan/examples/iotdevice/main.go`
- `/Users/shengming/Documents/code/gocell/worktrees/536-pr262-auth-typed-plan/tools/archtest/auth_plan_test.go`

---

## 元数据合规

未涉及 cell.yaml / slice.yaml / contract.yaml / assembly.yaml 字段变更。`gocell validate --strict` 现有规则不受影响。

## 总裁决

**CONDITIONAL APPROVE** — 架构方向正确（sealed plan + segregated interfaces + composition-root wiring + AST 兜底），是 PR262 plan §"关键决策" 的精确实现。建议本 PR 内修 F1/F2/F3/F4 四项小尾巴；不修也不阻塞合并，但应记入 cleanup backlog。
