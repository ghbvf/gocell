# PR #258 PR-A14b 六席位 Review 汇总

Branch: `526-pr-a14b-route-group` | Base: `origin/develop` | PR: https://github.com/ghbvf/gocell/pull/258
每席位独立 review 原文见：`202604251700-pr258-reviewer-{correctness,security,tests,ops,dx,architecture}.md`（architecture 已单独落文件）。

---

## IN_SCOPE 必修（Cx1/Cx2）

### 正确性 (CORR)

| ID | Cx | File:Line | 问题 | 修复 |
|----|----|-----------|------|------|
| CORR-01 | Cx2 | `runtime/bootstrap/bootstrap_phases.go:620-634` | `FinalizeAuth` 只在 PrimaryListener router 调用；Internal/Health router 的 `verifyDelegatedConsistency` + `verifyPolicyCoverage` 被静默跳过 | 重命名为 `phase5FinalizeAllRouters`，遍历所有 routers；primary 保留 auth verifier 检查，其它只调 `FinalizeAuth` |
| CORR-02 | Cx1 | `runtime/bootstrap/listener.go:58-81` | doc 说"重复 ref 是 phase0 错误"，代码 `b.listenerConfigs[ref] = cfg` 静默覆盖 | `WithListener` 追加到 `duplicateListenerRefs []cell.ListenerRef` 切片；`validateHTTPListenerConfigs` 检测并拒绝 |
| CORR-03 | Cx1 | `runtime/bootstrap/listener.go:41-55` + `bootstrap_phases.go:1082-1120` | `WithListenerTLS` / `WithListenerShutdownGrace` 是死字段；配置永远不生效 | 实现（`tls.Listen` + per-listener shutdown ctx）或删除 option；不留死接口 |
| CORR-04 | Cx1 | `runtime/bootstrap/bootstrap_phases.go:1055-1077` | `httpErrCh` 多错误只返回第一个，其余被 GC 丢弃 | 收到第一个错误后 drain 剩余 + `errors.Join` |
| CORR-05 | Cx1 | `runtime/bootstrap/policy.go:62-70` | `PolicyStack` 静默跳过非 `mountablePolicy` 条目 | `slog.Warn("bootstrap: PolicyStack: dropping non-mountable policy", "describe", p.Describe())` 或 panic fail-fast |
| CORR-06 | Cx1 | `runtime/bootstrap/bootstrap_phases.go:1020-1044` | `for ... range map` 非确定性 bind 顺序 + 日志顺序 | 按 `ref.String()` 排序后迭代 |

### 安全 (SEC)

| ID | Cx | File:Line | 问题 | 修复 |
|----|----|-----------|------|------|
| SEC-02 | Cx1 | `runtime/bootstrap/policy_verbose_token.go:59` | `got != token` 非 constant-time，存在 timing oracle | `subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1` |
| SEC-04 | Cx1 | `runtime/bootstrap/health.go:31-73` | Health fallback 到 primary 时，`/metrics` `Public:true` 无保护 | health-metrics endpoint 添加 policy 字段；或在 SharedDeps.Validate 里 real 模式强制要求 HealthListener 独立 |
| SEC-06 | Cx1 | `runtime/bootstrap/policy_verbose_token.go:53-54` vs `runtime/http/health/health.go:424` | `?verbose=false` 触发 policy 401 但 handler 不输出 verbose，操作探针误卡 | 两边对齐：都用 `readyzVerbose` helper 或都用 `.Has("verbose")` |
| SEC-11 | Cx1 | `runtime/bootstrap/bootstrap_phases.go:1036-1043` | `http.Server` 只有 `ReadHeaderTimeout`，缺 `ReadTimeout`/`WriteTimeout`/`IdleTimeout` → Slowloris | 对所有 listener 注入 `ReadTimeout: 30s`、`WriteTimeout: 30s`、`IdleTimeout: 60s`（可 option 覆盖） |
| SEC-14 | Cx1 | `runtime/bootstrap/policy.go:62-70` | 与 CORR-05 相同（PolicyStack 静默跳过） | 同 CORR-05 |
| SEC-01 | Cx2 | `runtime/bootstrap/policy_mtls.go:64-72` | `WithMTLSClientAuth` 配置无实际效果（未写入 `tls.Config`） | 实现：在 listener 构建时把 `clientAuth` 注入 `tls.Config`；或删除未实现的 option |

### 测试 (TEST)

| ID | Cx | 问题 | 修复 |
|----|----|------|------|
| TEST-01 | Cx1 | LAYER-07/LAYER-08 archtest 已存在但未验证 "反向 test"（插入违规文件确认报错） | 添加一个 fixture 子测试或 `t.Run("negative_probe", ...)` |
| TEST-02 | Cx1 | 无 3-server shutdown goroutine leak 测试 | 新增 `TestTripleListener_ShutdownNoLeak` |
| TEST-03 | Cx1 | 未测试未知 ListenerRef（零值/外部构造）传入 `WithListener` | 已有 phase5 panic 路径，新增显式用例 |
| TEST-04 | Cx1 | `PolicyMTLS` 缺 happy path（valid cert 200 OK）测试 | 构造 self-signed cert + pool，注入 `r.TLS.PeerCertificates` |
| TEST-05 | Cx1 | `PolicyStack()` 空切片无 no-op 测试 | 补一行 |
| TEST-13 | Cx1 | bootstrap 层 duplicate `(method,path)` 未测 | 新增 `TestBootstrap_DuplicateRouteGroup_FailsFast` |
| TEST-06 | Cx1 | `kernel/cell/routegroup_test.go` 非 table-driven | 重构为单 Test+table |
| TEST-07 | Cx1 | `dual_listener_test.go` 仍用旧 shim options | 改用 `newTestListeners(t)` 走 `WithListener` |
| TEST-08 | Cx1 | `waitForHealthy` 在 HealthListener 存在时仍 poll primary | 改 poll health listener 地址 |
| TEST-10 | Cx2 | `cmd/corebundle/auth_integration_test.go` 未声明 HealthListener | 添加 HealthListener + 断言 primary `/healthz` 返回 404 |
| TEST-11 | Cx1 | bootstrap-owned primary + internal bind fail 路径无测 | 补测 |
| TEST-14 | Cx1 | `kernel/cell/policy_test.go` 缺编译期 satisfaction 断言 | `var _ cell.Policy = testDoublePolicy{}` |

### 运维/可观测 (OPS)

| ID | Cx | File:Line | 问题 | 修复 |
|----|----|-----------|------|------|
| OPS-01 | Cx1 | `runtime/bootstrap/bootstrap_phases.go:1104-1108` | shutdown drain log 打 `server_index=0/1/2`，operator 无法定位 listener | 改签名收 `[]boundServer`，log `slog.String("listener", bs.name)` |
| OPS-02 | Cx1 | `runtime/http/router/router.go:531-543` + `bootstrap_phases.go:562-571` | `FinalizeAuth` delegated 一致性错误未带 cell ID | `phase5MountRouteGroups` 捕获 rg.Register 错误并包裹 cell id；或 `RouteGroup` 添加 `CellID` 字段 |
| OPS-03 | Cx1 | `docs/ops/listener-topology.md` | 缺 Helm/values.yaml 迁移示例 | 追加 "Helm migration" 小节 |
| OPS-04 | Cx1 | `docs/ops/listener-topology.md` | 缺 PodMonitor/ServiceMonitor 示例 | 追加 endpoints[].port: health 示例 |
| OPS-06 | Cx1 | `runtime/bootstrap/bootstrap_phases.go:1064-1066` | 启动日志缺 policy 字段 | `slog.Info("HTTP listener bound", "listener", ref.String(), "addr", ..., "policy", cfg.policy.Describe())` |
| OPS-07 | Cx1 | `runtime/bootstrap/bootstrap_phases.go:1020-1043` | PolicyNone + 非 loopback 未 Warn | bind 后检测 `ln.Addr().(*net.TCPAddr).IP` 是否 loopback；非 loopback + PolicyNone → slog.Warn |
| OPS-08 | Cx1 | `docs/ops/env-vars.md:108-122` | HTTP Listeners 节缺 "Breaking change" blockquote | 添加显眼 blockquote |
| OPS-05 | Cx4 | `runtime/observability/metrics/provider_collector.go:89` | `cell` label = assemblyID（PR-A36 pre-existing debt） | OUT_OF_SCOPE，登记 backlog |

### DX

| ID | Cx | File:Line | 问题 | 修复 |
|----|----|-----------|------|------|
| DX-01 | Cx1 | `.claude/rules/gocell/runtime-api.md:59-62` | doc 示例签名错 (`WithListener(ref, primaryCfg)` vs 真实 `(ref, addr, policy, opts...)`) | 改为 `WithListener(cell.PrimaryListener, ":8080", bootstrap.PolicyJWT(verifier))` 等真实调用 |
| DX-02 | Cx1 | `runtime/bootstrap/policy.go:43,62-70` | `PolicyStack` 与 CORR-05 合并：静默跳过 + `Describe()` 返回静态 "stack" | 修复 + `Describe()` 返回 `"stack[jwt, service-token]"` |
| DX-05 | Cx1 | `kernel/cell/routegroup.go` | 无 `cell.SingleGroup` 便捷构造器，每个 cell 要写 struct literal 样板 | 新增 `func SingleGroup(l ListenerRef, prefix string, fn func(RouteMux)) RouteGroup` |
| DX-07 | Cx1 | `runtime/bootstrap/policy_jwt.go:33` | `PolicyJWT(nil)` panic 消息未提示替代方案 | 追加 `"; use WithAuthDiscovery() to discover from an authProvider cell"` |
| DX-08 | Cx2 | `runtime/bootstrap/bootstrap.go:353-375` | Deprecated shims `WithPrimaryListener` 等与 CLAUDE.md "无向后兼容" 相悖 | **删除** 4 个 shim，迁移所有 call sites 到 `WithListener` |
| DX-09 | Cx1 | `cmd/corebundle/bundle.go:99-103` | Health fallback 行为仅在 bundle.go 内联注释，bootstrap 自己的 godoc 未提 | 补 `Bootstrap.Run()` 或 `WithListener` godoc |
| DX-03 | Cx1 | `runtime/bootstrap/listener.go:59-66` | nil `defaultPolicy` 语义未在 godoc 说明 | 补一句 "nil = 无 listener-level auth；靠 per-route auth.Declare" |

### 架构 (ARCH)

| ID | Cx | 问题 | 修复 |
|----|----|------|------|
| ARCH-01 | Cx1 | `cell.Policy` 只有 `Describe()`，允许外部 impl 编译过但运行 no-op（与 CORR-05/SEC-14/DX-02 同源） | 合并到 CORR-05 修复 |
| ARCH-03 | Cx2 | listener policy / auth.Declare / RouteDecl.Policy 三路径优先级无文档 | `.claude/rules/gocell/runtime-api.md` 补优先级矩阵 |
| ARCH-04 | Cx2 | `PolicyVerboseToken` 作用于整个 listener mux，但语义只给 `/readyz` | 改名 + 作用域（如 `bootstrap.HealthReadyzVerboseTokenOption`）或文档明确 |
| ARCH-05 | Cx2 | `WithAuthMiddleware`/`WithAuthDiscovery` 与 `PolicyJWT` 两路径 | phase0 冲突检测：同时声明都视为错误 |
| ARCH-12 | Cx2 | Shim 与 DX-08 重合 | 合并到 DX-08 修复 |
| ARCH-02 | Cx2 | `cell.{Primary,Internal,Health}Listener` 把部署拓扑烘焙进 cell 层（跨 assembly 复用疑虑） | OUT_OF_SCOPE 本 PR：保留 pragmatic design，backlog 登记未来抽象 |

---

## OUT_OF_SCOPE / 登记 backlog

- OPS-05 / ARCH-02 / DX-11 / SEC-07 / SEC-10 / SEC-12 / TEST-12 / CORR 已确认安全项 → backlog 或文档化
- PR-A36 R2-FOLLOW 已在 backlog，不在本 PR 范围

---

## 合并阻塞判定

**P0 blocker**（必修）：CORR-01 / CORR-03 / SEC-02 / SEC-04 / SEC-11 / DX-01 / DX-08

**P1 必修**（见上表）：~25 项 Cx1/Cx2 IN_SCOPE

**P2 / 低风险**：其余
