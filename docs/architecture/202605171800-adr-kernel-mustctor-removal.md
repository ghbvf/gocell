# ADR: Kernel Must*/error-first 混用清理（B2-K-02）

> Status: Accepted
> Date: 2026-05-17
> Backlog: docs/backlog.md B2-K-02
> Implementation: worktree 223-kernel-mustctor-removal

## Context

backlog `docs/backlog.md` B2-K-02：

> Kernel Must*/error-first 混用 — 现状: `MustNewAuthJWT` 等 Must 系列与 error-first
> 构造器混用，composition root 残留 panic；修复: 生产路径改 error-first，Must 仅
> test-only/cmd 顶层

### 根因分析

**问题 1：production 真构造器与 error-first API 混用**

`kernel/cell`、`runtime/auth`、`runtime/audit`、`runtime/state`、`runtime/http` 等层同时暴露
`MustNewXxx`（panic-on-error）和 `NewXxx`（error-first）两套接口。composition root
（`cmd/corebundle`、`examples/*/main.go`）使用 `Must*` 形态，配置错误以 panic 逸出而不是
沿错误链上浮，影响 startup 可观测性与错误处理一致性。

**问题 2：test-only fn 被 production 代码误用**

`runtime/auth` 包同时暴露 production API 与测试专用 fn：

- `auth.MustGenerateTestKeyPair`
- `auth.MustNewTestKeySet`
- `auth.MustNewTestKeyProvider`

这三个 fn 语义上属于测试夹具，但因物理上与 production code 在同一 package namespace，
production 代码（`cmd/corebundle/secrets.go:30`）可以直接调用，且编译器不报错。

**问题 3：无静态防线**

没有任何机制阻止未来 AI 在 production 路径新增 `Must*` 真构造器或误用 test-only fn。

---

## Decision

### D1. production `Must*` 三类合法形态

重构后仅以下三类 `Must*` 被允许出现在 production 路径：

**(a) Assertion guard（运行时不变量违反，无构造义务）**

这类 fn 验证**已存在对象的不变量**，不承担构造对象的责任。违反意味着程序员错误（逻辑 bug），
不是配置错误，panic 语义正确。

| 包 | 函数 | 用途 |
|---|---|---|
| `kernel/cell` | `MustHaveNonEmptyHealthName` | registry health 名不为空 |
| `kernel/cell` | `MustHaveLifecycleHookName` | lifecycle hook 名不为空 |
| `kernel/cell` | `MustHaveNonEmptyConfigPrefixes` | config prefix 不为空 |
| `kernel/cell` | `MustHaveNonNilConfigReloadFn` | config reload fn 非 nil |
| `kernel/cell` | `MustNotBeRegistryFinalized` | registry 未被终结 |
| `kernel/clock` | `MustHaveClock` | ctx 中必须有 clock |
| `kernel/clock` | `MustHavePositiveInterval` | interval 必须为正 |
| `kernel/observability/metrics` | `MustValidateLabels` | metrics label 合法性 |
| `pkg/errcode` | `MustValidateDetailsKinds` | details kind 合法性 |

**(b) Codegen funnel（编译期 ADR 违规语义，唯一 caller 是 codegen）**

这类 fn 由 cellgen/代码生成工具调用，panic 语义等价于"codegen metadata 与 schema
不匹配是 bug"，在 CI 期立即暴露，不会进入 runtime。

| 包 | 函数 | 理由 |
|---|---|---|
| `kernel/cell` | `MustNewBaseCell` | `BASESLICE-CTOR-FUNNEL-01` 已锁定，唯一 caller 是 cellgen |
| `kernel/cell` | `MustNewBaseSliceFromMeta` | 同上，cellgen 产物，每个 slice 一次 |
| `kernel/metadata` | `MustNewGoIdentifier` | 编译期 Go identifier 合法性校验 |
| `kernel/outbox` | `MustNewEntryID` | UUID 生成，runtime 不可失败 |
| `cells/auditcore/internal/appender` | `MustNewSpec` | sealed 白名单 funnel，pkg-level var init |

**(c) Test fixture（物理隔离到测试子包）**

测试专用 fn 必须物理隔离在以下包路径，production 代码 import 即暴露调用意图，archtest 可
以低成本锁定：

- `runtime/auth/keystest/`（新建，承接迁移来的三个 RSA key Must*）
- `runtime/auth/authtest/`（既有 policy fixture 包，不变）
- `pkg/contracttest/`
- `pkg/testutil/fileutil/`
- `kernel/cell/celltest/`
- `runtime/audit/ledger/storetest/`
- `cells/internal/testoutbox/`

**D1 总结：除上述三类外，production 路径（`kernel/` `runtime/` `adapters/` `cells/`
`cmd/` `examples/` 非 `_test.go` 非 test 子包）禁止声明或调用 `Must*` 真构造器。**

另有一处内部 validator 豁免，详见 §carve-out registry。

### D2. 物理删除 + 物理迁包（Hard 主防线）

production 真构造器 Must*（共 15 个）全部直接删除，无 deprecation alias、无 build tag
shim、无 namespace 共存：

**删除清单（按包分组）：**

`kernel/`（2 个）：

| 位置 | 函数 | 处置 |
|---|---|---|
| `kernel/wrapper/handler.go` | `MustHTTPHandler` | 零 production caller，死代码删除 |
| `kernel/wrapper/consumer.go` | `MustWrapConsumer` | 零 production caller，死代码删除 |

`kernel/cell`（3 个）：

| 位置 | 函数 | caller 改造 |
|---|---|---|
| `kernel/cell/auth_plan.go` | `MustNewAuthJWT` | `examples/iotdevice/main.go`、`examples/todoorder/main.go` 改 `NewAuthJWT` + `log.Fatal` |
| `kernel/cell/auth_plan.go` | `MustNewAuthJWTFromAssembly` | `cmd/corebundle/bundle_options.go`、`examples/ssobff/app.go` 改 error-first |
| `kernel/cell/auth_plan.go` | `MustNewAuthServiceToken` | `cmd/corebundle/bundle_options.go` 改 error-first |

`runtime/`（9 个）：

| 位置 | 函数 | caller 改造 |
|---|---|---|
| `runtime/auth/session/protocol.go` | `MustNewProtocol` | `cmd/corebundle/access_module.go`、`examples/ssobff/app.go` 改 error-first |
| `runtime/audit/ledger/protocol.go` | `MustNewProtocol` | `cmd/corebundle/audit_module.go` 改 error-first |
| `runtime/state/cas/protocol.go` | `MustNewProtocol` | `cmd/corebundle/{access,config}_module.go`、`examples/ssobff/app.go` 改 error-first |
| `runtime/distlock/locker.go` | `MustNew` | 零 production caller，死代码删除 |
| `runtime/http/router/router.go` | `MustNew` | 零 production caller，死代码删除 |
| `runtime/http/middleware/circuit_breaker.go` | `MustCircuitBreaker` | 零 production caller，死代码删除 |
| `runtime/http/middleware/cookie_session.go` | `MustCookieSession` | 零 production caller，死代码删除 |
| `runtime/auth/principal.go` | `MustFromContext` | 零 production caller，死代码删除 |
| `runtime/auth/route.go` | `MustMount` | `runtime/bootstrap/health.go:119,130,144` 改 `route.Mount` + error 上浮 |

`adapters/`（1 个）：

| 位置 | 函数 | 处置 |
|---|---|---|
| `adapters/websocket/handler.go` | `MustUpgradeHandler` | 零 production caller，死代码删除 |

**test-only fn 物理迁包（3 个）：**

| 现位置 | 新位置 | 理由 |
|---|---|---|
| `runtime/auth/keys.go:MustGenerateTestKeyPair` | `runtime/auth/keystest/keys.go` | K8s `httptest` 范本：物理迁出 production namespace |
| `runtime/auth/keys.go:MustNewTestKeySet` | `runtime/auth/keystest/keys.go` | 同上 |
| `runtime/auth/provider.go:MustNewTestKeyProvider` | `runtime/auth/keystest/provider.go` | 同上 |

迁包同时在 `runtime/auth/keystest/keys.go` 新增 error-first `GenerateRSAKeyPair`，
修复 `cmd/corebundle/secrets.go:30` 处 production 代码误调 test-only fn。

注：`runtime/auth/authtest/` 是既有 policy fixture 包（`authtest/policy.go`），与本次
RSA key 迁包无关，两个子包分工不同：`keystest/` 承接 key 生成测试工具，`authtest/` 承接
policy fixture 工具。

### D3. archtest 声明侧守卫（Medium 次防线）

新增 `tools/archtest/kernel_mustctor_production_decl_test.go`：

- 规则 ID：`KERNEL-MUSTCTOR-PRODUCTION-DECL-01`
- **声明侧**扫描：production 路径中非 `_test.go`、非 test 子包的文件，禁止声明 `func Must*`
- 工具链：`scanner.EachInSubtree[*ast.FuncDecl]`（声明侧 AST 扫描）
- carve-out 机制：`(pkgPath, funcName)` 双键 allowlist，明列 D1(a)(b) 保留项
- 反向自检 fixture：内联于 `TestKernelMustCtorReverseFixtureScan` 函数体（`fixtureSrc`
  字符串），不使用物理文件；`parser.ParseFile` 从字符串解析含故意违规 `func MustViolation()`
  的合成 Go 文件，子测试断言命中 fixture 且未命中 production carve-out

---

## 对标范本

| 框架 | Must* 政策 | GoCell 对应决策 |
|---|---|---|
| **Kubernetes** `apimachinery/pkg/api/resource` | `MustParse` godoc: *"for tests or other cases where you know the string is valid"*；production init path 全部 error-first | 直接对标：test fixture 物理隔离 + production 删 Must* |
| **Uber fx** | 零 `Must*` 暴露；构造期全 error-first；`app.Err()` 链路聚合 compose error | `cmd/corebundle` composition root 改 error-first chain，`defaultRuntimeOptions` 签名改 `(_, error)` |
| **Kratos** | 零 `Must*` 暴露；middleware 全 error-first option；`server.New` 返回 error | runtime/ 层删 `MustCircuitBreaker`、`MustCookieSession`；改 error-first |
| **go-zero** | `MustNewServer` 仅 cmd 层 `os.Exit` 入口，不是 library 函数 | GoCell `examples/*/main.go` 用 `log.Fatal` 而非 panic；library 层无 Must* |

参见 `docs/references/framework-comparison.md`。

---

## AI-rebust 评级

按 `.claude/rules/gocell/ai-collab.md` 三档定义严格分类。

### 主防线 = Hard（compile-error）

**机制**：symbol 物理删除 + 物理迁包

**Hard 性质来源**：违反不可表达。

- AI 在 production 路径写 `kernel.MustHTTPHandler(...)` → symbol 不存在 → **编译失败**
- AI 在 production 路径写 `auth.MustGenerateTestKeyPair()` → symbol 物理迁到 `keystest/`
  → import 自动暴露调用者语义（"我在 import test-only 包"），archtest 可以低成本锁定
  production 包不准 import `keystest/`

无 deprecation alias / 无 build tag 影分身 / 无 namespace 共存。

**对照 ai-collab.md §"Hard 范本"**（typed function call as Hard funnel）：

> Hard property comes from "form uniqueness": picking any other shape fails archtest
> immediately.

本方案的 form uniqueness 来自 **symbol 不存在性**，比 archtest-bound Hard 更彻底——
绕过需要重新 export `Must*`，是改 production 包 API 的 diff，PR review 必然捕获。
**这比 ai-collab.md 任何范本都更强（编译期 vs CI 期）。**

### 次防线 = Medium（archtest 声明侧）

**机制**：`KERNEL-MUSTCTOR-PRODUCTION-DECL-01`，声明侧 `func Must*` 扫描 + carve-out
allowlist

**Medium 性质来源**：carve-out 是 `(pkgPath, funcName)` 双键字符串约定；AI 可以新增
未在 carve-out 中的 `Must*` 声明，archtest fail；理论上 AI 也可以"自助修改 carve-out
一行字符串"绕过——但 carve-out 改动是 review 一等公民 diff，正常 review 流程会捕获。

**为何声明侧优于调用侧**：调用侧规则（"production caller 禁调 Must*"）的 callee 集合
是字符串约定，同样是 Medium；声明侧更直接——未来扩散点是**声明**，不是调用，一个规则
封锁全部扩散路径。

### 为何 Medium 是终态（不登记 backlog 升 Hard 条目）

升 Hard 的唯一可行路径是 sealed marker receiver：

```
mustpolicy.AssertionGuardMarker  // 私有字段，包外不可构造 fake 实现
mustpolicy.CodegenFunnelMarker
mustpolicy.TestFixtureMarker

// 合规 Must* 改 receiver method
func (AssertionGuardMarker) MustHaveClock(ctx context.Context, key any) {}
```

验证 caller 规模（547 数字来源）：

```bash
grep -rEcn '\bMust(Have|Not|Validate|NewBase|NewGo|NewEntry|NewSpec)' \
  --include='*.go' \
  kernel/ runtime/ adapters/ cells/ cmd/ examples/ tools/ \
  | awk -F: '{s+=$2} END{print s}'
# 2026-05-17 快照：~547（production ~236 + test ~311）
```

代价：

- 新建 `kernel/mustpolicy/` 抽象包
- 改 D1(a)(b) 9 个保留 fn 签名加 receiver
- 改 ~547 处 caller（production ~236 处 + test ~311 处）加 `mustpolicy.AssertionGuard.` 前缀
- cellgen 模板同步改（`MustNewBaseSliceFromMeta` 在 cellgen 产物中每 slice 一次，110+ 处）
- 预估 diff +500-800 行 + 547 处单点修改

这违反优雅简洁原则：caller 从 `clock.MustHaveClock(c, "ctx")` 变成
`mustpolicy.AssertionGuard.MustHaveClock(c, "ctx")`，啰嗦且无信息增量。

且升 Hard 也并非"纯 Hard"：sealed marker 防"合规 Must* 被绕过调用"，但 AI 仍可新增
`func MustFoo` 不接 receiver——**新增声明**仍需 archtest 声明侧介入，无法纯靠 type
system 表达"function name 必须有 receiver"。

真实图景：

| 防线层 | 机制 | 评级 |
|---|---|---|
| 物理删除 production 真构造器 | symbol 不存在 = compile error | **Hard** |
| 物理迁包 test-only fn | import 改变语义 + 低成本 archtest lock | **Hard** |
| archtest 声明侧守卫 | carve-out allowlist = CI 期 archtest fail | **Medium（终态）** |

**明确决策：不登记 backlog 升 Hard 条目。** 这不是 silent carryover，是主动论证后的终态
决策，由本 ADR §"为何 Medium 是终态"存档。

---

## Carve-out Registry

`KERNEL-MUSTCTOR-PRODUCTION-DECL-01` archtest 的 `allowedMustDecls` allowlist，权威源为
`tools/archtest/kernel_mustctor_production_decl_test.go`：

| 包路径常量 | 函数名 | 类别 | 理由 |
|---|---|---|---|
| `pkgKernelCell` | `MustHaveNonEmptyHealthName` | assertion guard | registry health 名不为空，运行时不变量 |
| `pkgKernelCell` | `MustHaveLifecycleHookName` | assertion guard | lifecycle hook 名不为空，运行时不变量 |
| `pkgKernelCell` | `MustHaveNonEmptyConfigPrefixes` | assertion guard | config prefix 不为空，运行时不变量 |
| `pkgKernelCell` | `MustHaveNonNilConfigReloadFn` | assertion guard | config reload fn 非 nil，运行时不变量 |
| `pkgKernelCell` | `MustNotBeRegistryFinalized` | assertion guard | registry 未被终结，状态机不变量 |
| `pkgKernelCell` | `MustNewBaseCell` | codegen funnel | `BASESLICE-CTOR-FUNNEL-01` 守卫，唯一 caller = cellgen |
| `pkgKernelCell` | `MustNewBaseSliceFromMeta` | codegen funnel | 同上，cellgen 每 slice 一次，110+ call site |
| `pkgKernelClock` | `MustHaveClock` | assertion guard | ctx 中必须有 clock，programmer-error |
| `pkgKernelClock` | `MustHavePositiveInterval` | assertion guard | interval 必须为正，programmer-error |
| `pkgKernelMetrics` | `MustValidateLabels` | assertion guard | metrics label 合法性，pkg-level var init |
| `pkgKernelMeta` | `MustNewGoIdentifier` | codegen funnel | 编译期 Go identifier 合法性 |
| `pkgKernelOutbox` | `MustNewEntryID` | codegen funnel | UUID 生成，runtime 不可失败 |
| `pkgErrcode` | `MustValidateDetailsKinds` | assertion guard | details kind 合法性，pkg-level var init |
| `pkgAppender` | `MustNewSpec` | codegen funnel | sealed 白名单 funnel，pkg-level var init |
| `pkgWebsocketHub` | `MustValidateHubConfig` | 内部 validator | `NewHub` 内部调用，不暴露为跨包 callee |
| `runtime/audit/ledger` | `MustTamperEntryHash` | (c) test fixture method on MemStore | test-only method 暴露在 production package 供 storetest 合规测试（负向 Verify 用例）注入篡改；不能移到 `_test.go`，因为 `storetest` 子包在 suite runtime 消费该方法 |
| `runtime/audit/ledger` | `MustTamperEntryPrevHash` | (c) test fixture method on MemStore | 同上，`MustTamperEntryHash` 的配套方法，用于篡改 prev_hash 构造 chain-break 测试向量 |

Test fixture 包路径前缀（`testFixturePkgPrefixes` slice，这些路径下不扫描）：

- `runtime/auth/keystest`（RSA key test-only fn，本次迁包目标）
- `runtime/auth/authtest`（既有 policy fixture 包，不变）
- `pkg/contracttest`
- `pkg/testutil`
- `kernel/cell/celltest`
- `runtime/audit/ledger/storetest`
- `cells/internal/testoutbox`

---

## 影响面

### 删除统计

| 包 | 删除 Must* | 备注 |
|---|---|---|
| `kernel/wrapper` | 2 | `MustHTTPHandler`、`MustWrapConsumer`（零 caller） |
| `kernel/cell` | 3 | `MustNewAuthJWT`、`MustNewAuthJWTFromAssembly`、`MustNewAuthServiceToken` |
| `runtime/auth` | 5 | `MustNewProtocol`（session）、`MustFromContext`、`MustMount`、`MustGenerateTestKeyPair`（迁 keystest）、`MustNewTestKeySet`（迁 keystest） |
| `runtime/auth` | +1（迁包） | `MustNewTestKeyProvider`（迁 keystest） |
| `runtime/audit` | 1 | `MustNewProtocol`（ledger） |
| `runtime/state` | 1 | `MustNewProtocol`（cas） |
| `runtime/distlock` | 1 | `MustNew`（零 caller） |
| `runtime/http` | 4 | `MustNew`（router）、`MustCircuitBreaker`、`MustCookieSession`（各零 caller）；`MustMount` 在 auth/route |
| `adapters/websocket` | 1 | `MustUpgradeHandler`（零 caller） |
| **合计删除** | **~15** | （含 3 迁包 = 物理迁出 production namespace） |

### Production caller 改造

| 文件 | 改造数量 | 主要变更 |
|---|---|---|
| `cmd/corebundle/bundle_options.go` | 2 | `MustNewAuthJWTFromAssembly` + `MustNewAuthServiceToken` → error-first；`defaultRuntimeOptions` 签名 + error |
| `cmd/corebundle/access_module.go` | 2 | `MustNewProtocol`（session + cas）→ error-first |
| `cmd/corebundle/audit_module.go` | 1 | `MustNewProtocol`（ledger）→ error-first |
| `cmd/corebundle/config_module.go` | 1 | `MustNewProtocol`（cas）→ error-first |
| `cmd/corebundle/secrets.go` | 1 | `auth.MustGenerateTestKeyPair` → `keystest.GenerateRSAKeyPair` + godoc dev-only |
| `runtime/bootstrap/health.go` | 3 | `route.MustMount` × 3 → `route.Mount` + error 上浮 |
| `examples/iotdevice/main.go` | 1 | `MustNewAuthJWT` → error-first + `log.Fatal` |
| `examples/todoorder/main.go` | 1 | `MustNewAuthJWT` → error-first + `log.Fatal` |
| `examples/ssobff/app.go` | 4 | `MustNewProtocol` × 2 + `MustNewAuthJWTFromAssembly` + `MustNewAuthServiceToken` → error-first |

### Test caller 改造

- `runtime/bootstrap/*_test.go` ~50 处 `cell.MustNewAuthJWT*` → `cell.NewAuthJWT*` + `require.NoError`
- `cmd/corebundle/*_test.go` 中 `auth.Must*` → `keystest.*`
- `kernel/wrapper/handler_test.go`、`consumer_test.go` 中 panic 验证测试 → error-first 测试

### 新增文件

- `runtime/auth/keystest/keys.go`（`MustGenerateTestKeyPair`、`MustNewTestKeySet`、新增 `GenerateRSAKeyPair`）
- `runtime/auth/keystest/provider.go`（`MustNewTestKeyProvider`）
- `tools/archtest/kernel_mustctor_production_decl_test.go`（`KERNEL-MUSTCTOR-PRODUCTION-DECL-01`）
- `tools/archtest/internal/kernelmustctorfixture/redfixture/decl.go`（反向 fixture）
- `docs/architecture/202605171800-adr-kernel-mustctor-removal.md`（本文件）

---

## 验证

```bash
# 全量编译
go build ./...

# 全量测试
go test ./...

# archtest（process-isolated shards）
bash hack/verify-archtest.sh

# archtest 精确子集（本次新增规则）
go test ./tools/archtest/ -run TestKernelMustCtorProductionDecl

# Hard 主防线 sanity：production 路径不残留真构造器 Must* 声明
# 期望：空输出
grep -rn '^func Must' --include='*.go' \
  kernel/ runtime/ adapters/ cells/ cmd/ examples/ \
  | grep -v '_test.go' \
  | grep -v 'keystest/' \
  | grep -v 'authtest/' \
  | grep -v 'contracttest/' \
  | grep -v 'testutil/' \
  | grep -v 'celltest/' \
  | grep -v 'storetest/' \
  | grep -v 'testoutbox/' \
  | grep -v 'MustHave\|MustNot\|MustValidate\|MustNewBaseCell\|MustNewBaseSliceFromMeta\|MustNewGoIdentifier\|MustNewEntryID\|MustNewSpec'

# Medium 次防线 sanity：反向 fixture 被命中
go test ./tools/archtest/ -run TestKernelMustCtorProductionDecl/fixture_violation_detected

# Lint
golangci-lint run ./...

# keystest 循环 import 检查
go list -json ./runtime/auth/keystest/... | grep '"Imports"'
# 期望：不包含 "runtime/auth"（keystest 不可 import 被测包）
```

**端到端验证**：

- `examples/iotdevice/main_test.go` 跑通（验证 caller error path）
- `cmd/corebundle/run_test.go` 跑通（验证 option 链 error 上浮）
- `tools/archtest/kernel_mustctor_production_decl_test.go` 反向 fixture 子测试通过

---

## 三大原则自审

| 原则 | 落地形态 |
|---|---|
| **彻底** | 15 个删除 + 9 处 production caller 改造 + 3 个 test fn 迁包 + ~50 处 test caller 批量改写 + secrets 误用修复 + archtest + ADR + backlog 更新，同一 PR；无 TODO/FIXME；无 P2/follow-up |
| **不向后兼容** | 直接删除 symbol（compile-error）+ 物理迁子包（namespace 变更）+ 签名变更（`defaultRuntimeOptions` / `route.MustMount`）；无 deprecation alias / 无 build tag shim / 无 double-write |
| **优雅简洁** | 复用 K8s `httptest` 范本；archtest 复用现有 `EachInSubtree`，无新工具；不引入 `mustpolicy.*Marker` 抽象类型；删除 > 改造 |

ref: kubernetes/apimachinery `pkg/api/resource/quantity.go` MustParse godoc — *"for tests or
other cases where you know the string is valid"*;
https://github.com/kubernetes/apimachinery/blob/master/pkg/api/resource/quantity.go
ref: uber-go/fx `app.go` error-first constructor convention;
https://github.com/uber-go/fx/blob/master/app.go
ref: go-kratos/kratos `app.go` middleware option pattern;
https://github.com/go-kratos/kratos/blob/main/app.go
ref: zeromicro/go-zero `core/service/servicegroup.go` MustNewServer cmd-layer pattern;
https://github.com/zeromicro/go-zero/blob/master/core/service/servicegroup.go
