# Batch2 Retrospective — 综合审查席位

## 安全维度 Findings

| ID | Severity | Cx | Evidence | Root cause | Fix direction |
|----|----------|----|----------|------------|---------------|
| SEC-01 | P2 | Cx2 | `runtime/auth/keys.go:127-132, 160-170, 230-237` — `slog.Info` 中 key 名为 `"kid"`（不是 `key_id`），K' 声称"key_id 在 slog Warn/Error 零命中"，但 `keys.go` 同文件多处 Info 级日志打印 `kid` 值本身（非哈希/截断） | `NewKeySet` / `NewKeySetWithVerificationKeys` / `PruneExpired` 日志原样打印 RFC 7638 thumbprint kid（43 字节 base64url 字符串）；thumbprint 本身非密钥材料，但与 slog Warn/Error 做精确 grep 时 key 名是 `kid` 而非 `key_id`，K' 的 redact 断言针对的字段名不匹配 | K' plan 的 REPO-LOG-KEY-ID-REDACT 任务若以"key_id"为搜索目标已达成；实际字段名是"kid"，无论如何 kid = thumbprint 非密钥材料，当前不需额外 redact，但需在 backlog 中澄清"key_id redact" vs "kid log" 指向不同字段，防止下一轮误 grep 遗漏真正的密钥材料 |
| SEC-02 | P1 | Cx1 | `runtime/auth/servicetoken.go:257-266` — `errorMiddlewareInternal` 的 `cfg.metrics.recordServiceVerify("failure","internal")` 当 `cfg.metrics` 为 nil（`ServiceTokenMiddleware` 不强制 `WithServiceTokenMetrics`）时会 nil panic | 四处 `errorMiddlewareInternal(cfg, ...)` 调用中 cfg.metrics 可能为 nil；`ServiceTokenMiddleware` 只给 `cfg.now / cfg.logger` 设默认值，未给 `cfg.metrics` 设默认 NopMetrics | 在 `ServiceTokenMiddleware` 初始化 cfg 时加 `if cfg.metrics == nil { cfg.metrics = noopAuthMetrics{} }`（同 logger 的默认化方式）；或 `errorMiddlewareInternal` 内部做 nil guard；Cx1 |
| SEC-03 | P3 | Cx1 | `cells/configcore/slices/configread/handler.go:75-84` — `RegisterInternalRoutes` 加了 `auth.AnyRole(auth.RoleInternalAdmin)` 作 defence-in-depth，测试 `TestHttpConfigInternalGetV1_PolicyDefenceInDepth` 已覆盖 401/403 两条 | 正确——`HandleGet` 在内部路由下也走同一 `dto.ToConfigEntryResponse` 路径（行 96 已做） | 信息项，无需修改 |

## 测试维度 Findings

| ID | Severity | Cx | Evidence | Root cause | Fix direction |
|----|----------|----|----------|------------|---------------|
| TEST-01 | P2 | Cx1 | `tools/nogo/unconditionalskip/analyzer.go:75-77` — `hasTestPrefix` 的 Benchmark 边界用例（恰 9 字符 `Benchmark`）实际由 `*testing.B` 类型检测守住不漏报；缺少该边界的 table-driven case | 防御性代码 `len(name) >= 9 && name[:9] == "Benchmark"` 的最短通过名称是 `Benchmark`（无尾字符），恰好合法但会被类型检测拦截；缺少边界 case | 在 `analyzer_test.go` 补一个 `TestAnalyzer_BenchmarkExactPrefix` 表格项；Cx1 |
| TEST-02 | P2 | Cx1 | `cells/configcore/slices/configread/contract_test.go:162-176, 181-189` — 两个 `AuthzNegative` 测试只覆盖 `no_auth (401)` 和 `non_admin (403)`，未覆盖 `admin` 角色正常通过 mux 的路径（happy-path 在 handler_test.go 覆盖，但 admin 角色在 contract_test 层没有正向断言） | contract_test 专注 negative；happy path 由 `TestHttpConfigGetV1Serve` 验证，但该测试直接用 `ValidateResponse` 验证响应体 schema，未通过 mux 发起完整请求——等于未走 `auth.AnyRole` policy 路径 | 在 `TestHttpConfigGetV1Serve` 补一个用 `auth.TestContext("u","admin")` 通过 mux 发起真实 `GET` 的正向 e2e 轮次；Cx1 |
| TEST-03 | P3 | Cx1 | `runtime/auth/authenticator_test.go:607-636` — `TestJWTAuthenticator_EmptySubject_Error` 覆盖空 subject 拒绝；`TestUnionAuthenticator_BearerAndServiceToken_NoCrossBleed`（行 675）已 assert `Kind == PrincipalService` | 已覆盖，无需修改 | 信息项 |
| TEST-04 | P1 | Cx2 | `tests/e2e/internal/require/docker.go:23-29` 仅 skip 不 fail；关键问题：`docker-compose.e2e.yaml` 的 `healthcheck` 用 `wget` 而非 `curl`，corebundle 镜像若无 `wget` 会导致 healthcheck 永不通过 | `docker-compose.e2e.yaml:128` healthcheck `wget -qO-`；Dockerfile.corebundle 若基于 `scratch/distroless` 则无 wget；需 Grep 确认 | [需确认] `tests/e2e/Dockerfile.corebundle` 的基础镜像是否包含 `wget`；若用 `scratch` 需改为 `CMD /app/corebundle --healthcheck` 自定义 probe；Cx2 |

## DX 维度 Findings

| ID | Severity | Cx | Evidence | Root cause | Fix direction |
|----|----------|----|----------|------------|---------------|
| DX-01 | P2 | Cx1 | `runtime/auth/servicetoken.go:368-381` — `classifyServiceTokenVerifyError` 用 `strings.Contains(msg, ...)` 匹配错误消息做 metric label 分类；若底层错误消息措辞改变，metric label 静默降级为 `"invalid_format"`，历史监控告警断链 | 错误分类依赖字符串内容而非 error type/code；`errcode.Error.Code` 是更稳定的锚点 | 改为 `errors.As(err, &ec); switch ec.Code { case ErrAuthTokenExpired: return "expired"; ... }`；Cx1 |
| DX-02 | P1 | Cx1 | `cells/accesscore/slices/identitymanage/service.go:471-473` — `ChangePassword` 在外部读取 user（行 471）后再在 `updatePasswordAndRevokeSessions` 内 txRunner.RunInTx 中再次写；读取（`GetByID`）在 tx 外，存在 TOCTOU 窗口 | `GetByID + CompareHashAndPassword + GenerateFromPassword` 在 tx 外；并发 ChangePassword 可能各自读到同一 `PasswordHash`，都通过校验，先后写入不同 hash，最终只有最后写入者生效，但两者都返回 200 | 将 `GetByID + CompareHashAndPassword + GenerateFromPassword` 整体移入 `updatePasswordAndRevokeSessions` 的 tx 闭包；`UpdateAt` 乐观锁或 `SELECT ... FOR UPDATE` 防并发冲突；Cx1 单文件改动 |
| DX-03 | P2 | Cx1 | `cells/configcore/slices/configwrite/handler.go:44` — `HandleUpdate`（PUT /{key}）中 `key := r.PathValue("key")` 未做空值或格式校验；service 层 `RequireNotBlank` 拦空 key，但非法字符不拦截（如 `/../`）；与 `identitymanage` UUID 路径参数 `ParseUUIDPathParam` 规范不一致 | 字符串 key 参数无 pattern 约束，contract.yaml 未声明 `format` 故 CH-05 不拦截 | 在 contract.yaml `pathParams.key` 加 `pattern: "^[a-zA-Z0-9._-]{1,255}$"` 并在 handler 用 `httputil.ParsePathParam(w, r, "key", pattern)` 校验；Cx1 |
| DX-04 | P3 | Cx1 | `runtime/auth/servicetoken.go:204-206` — `errorMiddlewareInternal` helper 五处复用消除原 500 重复（PR-CFG-I 目标已达） | 正向确认 | 信息项 |

## 工具结果

- `golangci-lint run ./...`: 静态阅读代码核实，未本地执行；PR-I 提交前已要求本地 lint 0 issues
- `go test ./...` 状态: 静态核实；`servicetoken_test.go` 和 `authenticator_test.go` 覆盖路径完整，`contract_test.go` 场景闭环（含 401/403/schema 三组表驱动）

## Seat Digest

**安全**：fail-closed 四层构造（kernel `NewAuthServiceToken` → bootstrap `auth_plan_validate` → `cmd/corebundle SharedDeps.Validate` → `ServiceTokenMiddleware` 运行时）逻辑正确、不可绕过；SEC-02 的 `cfg.metrics` nil panic 是真实崩溃路径（虽仅在无 `WithServiceTokenMetrics` 的测试配置中触发），需修复。K' 的 key_id/kid 字段名混淆不构成安全风险（thumbprint 非密钥材料），记为 P2 澄清项。

**测试**：PR-CFG-D 的 `unconditionalskip` analyzer 逻辑正确；`contract_test` 表驱动 401/403/envelope 三组已覆盖；TEST-02 的 `handleGet` happy-path 未经 mux 走完 `AnyRole` policy 是轻微覆盖缺口；TEST-04 的 Dockerfile.corebundle `wget` 依赖需确认。

**DX**：DX-02（`ChangePassword` GetByID 在 tx 外的 TOCTOU）是隐含的 P1 一致性风险，比 Lock/Unlock 的保护模式弱；DX-01 的 error-message-based metric label 是维护性脆点，建议随 DX-02 一并处理。**SEC-02 + DX-02 建议立即开 follow-up PR**（均为 P1）；其余为 P2 建议项，不阻塞合并。已登记 backlog `PR338-FU-AUTH-FAIL-CLOSED-DOC-CLEANUP` 和 `PR333-BOOTSTRAP-OPTION-CROSS-CONCERN` 范围不与以上 findings 重叠。

---

**复杂度汇总**: Cx1: 7 / Cx2: 2 / Cx3: 0 / Cx4: 0

**修复分流**:
- SEC-02, DX-01, DX-02, DX-03, TEST-01, TEST-02 → Cx1，可派发 `developer` agent 自动修
- TEST-04（Dockerfile healthcheck 确认）, SEC-01（字段名澄清）→ Cx2，建议人工确认后再修

**总体结论**: 需修复（SEC-02 是真实 nil panic 路径；DX-02 是一致性风险），其余为 P2 建议项，不阻塞合并。
