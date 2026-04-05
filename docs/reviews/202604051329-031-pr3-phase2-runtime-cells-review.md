# PR #3 Review: Phase 2 — Runtime + Built-in Cells（模块级深度审查）

> **PR**: feat/001-phase2-runtime-cells → main
> **Scope**: 295 files, +34575/-2654 | 182 Go, 75 MD, 3 YAML
> **审查日期**: 2026-04-05
> **审查方式**: 8 并行 agent 模块级审查（governance / metadata / lifecycle / access-core / audit+config / http / services / cmd+pkg）

---

## 总览

| 类别 | 数量 | 阻塞? |
|------|------|-------|
| 安全问题 | 9 | Yes |
| 架构/规范违规 | 5 | Yes |
| Concerns | 42 | No, should fix |
| 亮点 | 30+ | — |

---

## 一、BLOCKING — 安全问题（9 个）

### S1 [Critical] session logout 未持久化 revoke 状态 ❌ **已更正：实际已持久化**

> **审查结论矛盾**：第一轮审查(3-agent)报告 logout 未调 `sessionRepo.Update()`。但第二轮 access-core 深度审查确认 `sessionlogout/service.go:57-61` **确实调了 `session.Revoke()` + `s.sessionRepo.Update(ctx, session)`**，revocation 已持久化。
>
> **降级为非阻塞**。但存在相关测试 bug（见 C-AC3）。

### S2 [Critical] sessionrefresh JWT 解析缺少签名方法校验 — algorithm confusion

**文件**: `cells/access-core/slices/sessionrefresh/service.go:62`

`sessionvalidate` 正确检查 `t.Method.(*jwt.SigningMethodHMAC)` 防止 `alg: none` 攻击，但 `sessionrefresh` 的 `jwt.Parse` **未做此检查**。攻击者可构造 `alg: none` 的 refresh token 绕过签名验证。

**修复**: 添加与 sessionvalidate 相同的 signing method 校验。

### S3 [High] User Lock 不撤销已有 session

**文件**: `cells/access-core/slices/identitymanage/service.go`

`Lock()` 仅阻止新登录，不撤销已有 session。`sessionvalidate.Verify()` 是纯 JWT 无状态验证，不检查 user lock 状态。被锁用户的 access token 在 15 分钟内仍可用，refresh token 7 天内仍可用。

**修复**: Lock 时调 `sessionRepo.RevokeByUserID()` 或 Verify 时检查 user 状态。

### S4 [High] RealIP 无条件信任 X-Forwarded-For — IP 伪造

**文件**: `runtime/http/middleware/real_ip.go:25-30`

**修复**: 添加 `TrustedProxies` 参数。

### S5 [High] ServiceToken HMAC 只签 method+path — 重放 + 参数篡改

**文件**: `runtime/auth/servicetoken.go:30-31`

不含 query string、timestamp/nonce。Token 可无限重放，且可附加任意 query 参数。

**修复**: 包含 query string + timestamp header + 短窗口校验。

### S6 [Medium] ServiceToken 不拒绝空 secret

**文件**: `runtime/auth/servicetoken.go:14`

> **审查结论矛盾**：review-services 确认 line 15-22 已有 `len(secret) == 0` 校验返回 500。但缺少测试覆盖。
>
> **降级为 Concern**：补充测试即可。

### S7 [Medium] RequestID 接受任意客户端输入 — log injection

**文件**: `runtime/http/middleware/request_id.go:22-24`

**修复**: 限制 128 字符 + 拒绝控制字符。

### S8 [Medium] ID 生成用 time.Now().UnixNano() — 可碰撞 + 可预测

**文件**: 7 处（identitymanage, sessionlogin, auditappend, configwrite, configpublish, eventbus）

可预测的 session ID 使攻击者能猜测并撤销他人 session。

**修复**: 使用 `crypto/rand` UUID。

### S9 [Medium] access-core 端点无 auth/authz 保护

**文件**: `cells/access-core/cell.go` RegisterRoutes

DELETE /sessions/{id}、POST /{id}/lock 等端点无认证保护。知道 session ID 的任何人可撤销他人 session。

**修复**: 在路由注册时挂载 auth middleware。

---

## 二、BLOCKING — 架构/规范违规（5 个）

### V1 slice/verify.go 使用 fmt.Errorf 导出错误

**文件**: `kernel/slice/verify.go` — 7 处

**修复**: 替换为 `errcode.New` / `errcode.Wrap`。

### V2 VERIFY-01 只检查 provider 角色，V3 spec 要求所有角色

**文件**: `kernel/governance/rules_verify.go:40`

V3 spec: "每个 contractUsages 条目必须有 verify.contract 或 waiver"。实现跳过了 consumer 角色（call/subscribe/invoke/read）。

**修复**: 检查所有角色，或更新 spec 明确 consumer 豁免。

### V3 Projection `replayable` 必填字段未校验

**文件**: `kernel/governance/rules_fmt.go:107-145`

V3 spec: "Projection 额外必填：replayable"。FMT-04 只检查 event 类型。

**修复**: 扩展 FMT-04 覆盖 projection。

### V4 httputil/response.go 零测试

**文件**: `pkg/httputil/response.go`

HTTP 状态码映射是所有 API 的出口，当前 0 测试。CLAUDE.md 要求 >= 80% 覆盖率。

**修复**: 添加 mapCodeToStatus、WriteDomainError、WriteError 的完整测试。

### V5 access-core 4 个 slice 覆盖率低于 80%

identitymanage 42.3%、rbaccheck 41.2%、sessionlogin 64.7%、sessionlogout 60.7%、sessionrefresh 66.0%。Handler 层零测试。

**修复**: 补充 handler HTTP 测试。

---

## 三、CONCERNS（42 个，按模块分组）

### kernel/governance（7 个）

| # | 问题 | 文件 |
|---|------|------|
| C-G1 | DFS 只找第一个环，多环需反复修 | depcheck.go:140 |
| C-G2 | depcheck.go 重复定义 isProviderRole | depcheck.go:214 |
| C-G3 | Map 遍历非确定性，多错误时输出顺序不稳定 | 多个 rules 文件 |
| C-G4 | TOPO-04 检查 ownerCell 而非 provider actor 的一致性级别 | rules_topo.go:98 |
| C-G5 | 缺少 cell.verify.smoke / slice.verify.unit 非空校验 | 未实现 |
| C-G6 | 缺少禁用字段名 (cellId/sliceId 等) 检测 | 未实现 |
| C-G7 | FMT-09/FMT-08 调用顺序导致 invalid-kind 同时报两个错误 | rules_fmt.go |

### kernel/metadata+registry（6 个）

| # | 问题 | 文件 |
|---|------|------|
| C-M1 | Registry 线程安全未文档化（build-once-read-many 模式） | registry/cell.go |
| C-M2 | Parser 接受 `id: ""` 不报错 | metadata/parser.go |
| C-M3 | SchemaRefsMeta.Payload 缺少测试 | metadata/types_test.go |
| C-M4 | Catalog CellJourneys/ContractJourneys O(n*m) 无索引 | journey/catalog.go |
| C-M5 | ContractRegistry.Consumers() 方法名与禁用 YAML 字段名碰撞 | registry/contract.go:79 |
| C-M6 | StatusBoardEntry YAML tag `journeyId` 需确认命名约定 | metadata/types.go:138 |

### kernel/lifecycle（7 个）

| # | 问题 | 文件 |
|---|------|------|
| C-L1 | Start 和 StartWithConfig ~95% 重复代码 | assembly/assembly.go |
| C-L2 | Stop 允许从 stateStopped 重复调用 | assembly/assembly.go:138 |
| C-L3 | BaseCell 无线程安全（Health/Ready 可从不同 goroutine 调用） | cell/base.go |
| C-L4 | outbox.Entry.Metadata 未测试 | outbox/outbox_test.go |
| C-L5 | idempotency.DefaultTTL 未测试 | idempotency_test.go |
| C-L6 | contract ID 格式不一致：scaffold 用点分 vs generator 用斜杠 | scaffold vs generator |
| C-L7 | scaffold cell.yaml.tpl 的 verify.smoke 格式约定未文档化 | scaffold/templates |

### cells/access-core（7 个）

| # | 问题 | 文件 |
|---|------|------|
| C-AC1 | issueToken + TokenPair + TTL 常量在 login/refresh 两处重复 | sessionlogin + sessionrefresh |
| C-AC2 | Session refresh 存在 TOCTOU 竞态（并发 refresh 覆盖） | sessionrefresh/service.go |
| C-AC3 | "already revoked is idempotent" 测试实际未测试幂等性 | sessionlogout/service_test.go:53 |
| C-AC4 | Service 层 Create 返回含 PasswordHash 的 domain.User | identitymanage/service.go |
| C-AC5 | Session.ExpiresAt 追踪 access token 过期而非 session 过期 | sessionlogin/service.go:119 |
| C-AC6 | UserRepository.Update byName 索引改名时残留 | mem/user_repo.go:73 |
| C-AC7 | 无 JWT `jti` claim，token 不可单独撤销 | sessionlogin/sessionrefresh |

### cells/audit+config（9 个）

| # | 问题 | 文件 |
|---|------|------|
| C-DC1 | **Hash chain 状态纯内存，重启后断链** | auditappend/service.go:49 |
| C-DC2 | **订阅 goroutine 用 context.Background()，Stop 时泄漏** | audit-core/cell.go:150, config-core/cell.go:159 |
| C-DC3 | TopicConfigChanged 常量 3 处重复 | configwrite/configpublish/configsubscribe |
| C-DC4 | audit query 时间参数解析失败静默忽略 | auditquery/handler.go:29 |
| C-DC5 | configsubscribe unmarshal 失败 ACK 而非 dead letter | configsubscribe/service.go:73 |
| C-DC6 | auditappend publish 失败仅 log 不重试（L3 cell 缺 outbox 保证） | auditappend/service.go:92 |
| C-DC7 | configpublish.Rollback 不校验 version > 0 | configpublish/service.go:84 |
| C-DC8 | config-core handler 直接依赖 chi.URLParam — router 耦合 | 多个 handler.go |
| C-DC9 | auditarchive 是纯 stub，ArchiveStore 已定义但未接线（dead code） | auditarchive + cell.go |

### runtime/http（5 个）

| # | 问题 | 文件 |
|---|------|------|
| C-H1 | statusRecorder 在 3 个包重复，不支持 Flusher/Hijacker | access_log/tracing/metrics |
| C-H2 | 默认 middleware chain 缺 RateLimit | router/router.go:72 |
| C-H3 | Rate limiter Retry-After 硬编码 "1" | rate_limit.go:27 |
| C-H4 | HSTS 缺 includeSubDomains | security_headers.go:13 |
| C-H5 | access_log_test.go slog.SetDefault 测试隔离 bug | access_log_test.go:18-20 |

### runtime/services（7 个）

| # | 问题 | 文件 |
|---|------|------|
| C-S1 | auth middleware 用 slog.Warn 而非 slog.WarnContext — 无 request_id | auth/middleware.go:26 |
| C-S2 | shutdown.Manager 第一个 hook 失败中断剩余 hook | shutdown/shutdown.go:78 |
| C-S3 | shutdown.Manager 是 FIFO 顺序而非 LIFO（bootstrap 补偿但 API 误导） | shutdown/shutdown.go:78 |
| C-S4 | config watcher 无 debounce | config/watcher.go:58 |
| C-S5 | EventBus "bus is closed" 用 fmt.Errorf 而非 errcode | eventbus/eventbus.go:83 |
| C-S6 | Worker.Stop 注释说 reverse order 但实际并发执行 | worker/worker.go:75 |
| C-S7 | PeriodicWorker 正常关闭时返回 context.Canceled 被 log 为 Error | worker/periodic.go:33 |

### cmd+pkg（4 个）

| # | 问题 | 文件 |
|---|------|------|
| C-P1 | mapCodeToStatus 用 strings.Contains 匹配 — 顺序敏感、歧义 | httputil/response.go:68 |
| C-P2 | WriteJSON 忽略 json.Encode 错误 | httputil/response.go:19 |
| C-P3 | CLI exit code 不区分（usage error / validation error 都是 1） | cmd/gocell/main.go |
| C-P4 | core-bundle hardcoded dev secrets 无生产环境保护 | cmd/core-bundle/main.go:33,38 |

---

## 四、Spec vs 实现覆盖率

### V3 校验规则覆盖

| V3 规则 | 实现 | 状态 |
|---------|------|------|
| slice.belongsToCell → existing Cell | REF-01 | ✅ |
| contractUsages[].contract → existing contract | REF-02 | ✅ |
| contract.ownerCell must be Cell | REF-03 | ✅ |
| schemaRefs files exist | REF-12 | ✅ |
| cell.id / slice.id == directory name | REF-04, REF-05 | ✅ |
| contractUsages.role matches kind | TOPO-01 | ✅ |
| Provider: belongsToCell == contract provider | TOPO-02 | ✅ |
| Consumer: belongsToCell in consumers | TOPO-03 | ✅ |
| contract.consistencyLevel ≤ provider level | TOPO-04 | ⚠️ 检查 ownerCell 而非 provider |
| L0 Cell 不在契约端点 | TOPO-05 | ✅ |
| Cell 最多属于一个 assembly | TOPO-06 | ✅ |
| **contractUsage 需 verify 或 waiver** | VERIFY-01 | ❌ **只查 provider** |
| verify 标识符前缀格式 | — | ❌ 未实现 |
| L0 deps 声明 + 校验 | VERIFY-03, REF-09 | ✅ |
| lifecycle ∈ {draft, active, deprecated} | FMT-01 | ✅ |
| cell.type ∈ {core, edge, support} | FMT-02 | ✅ |
| 动态字段不在非 status-board 文件 | — | ❌ 未实现 |
| deprecated 契约不被新引用 | ADV-02 | ✅ (warning) |
| **Projection replayable 必填** | — | ❌ **未实现** |

**覆盖率**: 12/15 (80%)。3 条 gap 中 2 条为 BLOCKING（V2, V3）。

### V3 字段覆盖（Go types vs spec）

**100% 覆盖**。所有 V3 模型字段在 Go 类型中正确表示，YAML tag 匹配，无禁用字段名。

---

## 五、亮点

1. **0 层依赖违规** — kernel/runtime/cells 三层依赖方向完全正确
2. **kernel 覆盖率 93-100%**，table-driven 测试质量高
3. **governance 37 条校验规则**，超出 spec 的 22 条额外规则均合理
4. **bcrypt 密码哈希正确**，UserResponse DTO 正确排除 PasswordHash
5. **in-memory repo 全部 clone + RWMutex**，无竞态
6. **Recovery 不泄露内部错误**，AccessLog 不 dump body
7. **Bootstrap LIFO 回滚**（ref: uber-go/fx）正确实现
8. **HMAC-SHA256 hash chain** 密码学实现正确
9. **Feature flag** 确定性百分比评估（SHA256 bucketing）正确
10. **governance validate** 注入 `now`/`fileExists`，测试可确定性

---

## 六、修复优先级

### P0 — 合入前必修

| # | 工作量 | 说明 |
|---|--------|------|
| S2 sessionrefresh signing method 校验 | 3 行 | copy sessionvalidate 的检查 |
| V2 VERIFY-01 consumer 角色校验 | 10 行 | 删除 `continue` 或更新 spec |
| V3 Projection replayable 校验 | 5 行 | 扩展 FMT-04 |
| S5 ServiceToken HMAC 范围 | ~20 行 | 加 query + timestamp |
| S4 RealIP trusted proxies | ~30 行 | 添加配置参数 |
| V1 errcode 替换 | 7 处 | fmt.Errorf → errcode |

### P1 — 合入后一周内

| # | 工作量 | 说明 |
|---|--------|------|
| S3 Lock 不撤销 session | ~15 行 | 添加 RevokeByUserID |
| S8 UUID 替换 UnixNano | 7 处 | 引入 google/uuid |
| S9 access-core 端点挂 auth | ~10 行 | 路由级 middleware |
| V4 httputil 测试 | ~100 行 | 全量 status mapping 测试 |
| V5 access-core 覆盖率 | ~200 行 | handler HTTP 测试 |
| C-DC1 hash chain 重启恢复 | ~15 行 | Init 时 seed prevHash |
| C-DC2 订阅 goroutine 取消 | ~15 行/cell | context.WithCancel |
| C-S2 shutdown hook 不中断 | 5 行 | 改 return 为 continue |

### P2 — Phase 3 前

| # | 说明 |
|---|------|
| C-P1 mapCodeToStatus 改显式 map | 消除 strings.Contains 歧义 |
| C-AC1 issueToken 提取共享包 | 消除 login/refresh 重复 |
| C-H1 statusRecorder 提取 + Flusher/Hijacker | SSE/WebSocket 支持 |
| C-DC3 TopicConfigChanged 抽常量 | 消除 3 处重复 |
| C-S1 auth 日志用 slog.WarnContext | 关联字段注入 |
| C-L1 Start/StartWithConfig 去重 | 40 行重复 |
| C-G5/C-G6 补充 smoke/unit 非空 + 禁用字段校验 | spec 完整性 |

---

## 七、审查方法论对比

本 PR 进行了两轮审查，第二轮显著优于第一轮。

### 方法对比

| 维度 | 第一轮（3 agent） | 第二轮（8 agent） |
|------|-------------------|-------------------|
| 拆分粒度 | 按层：kernel / cells / runtime | 按模块：governance / metadata / lifecycle / access-core / audit+config / http / services / cmd+pkg |
| 每 agent 文件数 | 60-100 | 15-25 |
| 审查深度 | 广度扫描 | 逐文件深入 + spec 对照 |
| 发现总数 | 7 blocking + 21 concerns | 14 blocking + 42 concerns |

### 第二轮独有发现（第一轮遗漏）

| 发现 | 严重度 | 遗漏原因 |
|------|--------|---------|
| sessionrefresh JWT algorithm confusion（S2） | **Critical** | 第一轮 cells agent 覆盖 75 文件，未逐行对比 login vs refresh 的 jwt.Parse |
| User Lock 不撤销已有 session（S3） | **High** | 需要跨 slice 理解 validate 是无状态的，粗粒度审查未做此关联 |
| access-core 端点无 auth 保护（S9） | **Medium** | 第一轮聚焦代码质量，未审查路由注册的安全配置 |
| VERIFY-01 只查 provider 角色（V2） | **Blocking** | 第一轮无 spec vs 实现对照分析 |
| Projection replayable 未校验（V3） | **Blocking** | 同上 |
| httputil/response.go 零测试（V4） | **Blocking** | 第一轮 runtime agent 范围太大，pkg/ 被边缘化 |
| 5 个 slice 覆盖率 < 80%（V5） | **Blocking** | 第一轮未做逐包覆盖率检查 |
| shutdown.Manager 首 hook 失败中断（C-S2） | Concern | 第一轮只看了 bootstrap 层（正确），未深入 Manager 本身 |
| contract ID 格式 scaffold vs generator 不一致（C-L6） | Concern | 需要对比两个独立模块的格式约定，粗粒度审查未覆盖 |

### 第一轮误报（第二轮纠正）

| 误报 | 原因 |
|------|------|
| "session logout 未持久化 revoke" | 第一轮 cells agent 覆盖文件过多，sessionlogout/service.go 的 `Update` 调用被遗漏。第二轮 access-core 专项 agent 逐行确认 line 57-61 确实调了 Update |

### 结论

**大型 PR（200+ 文件）必须按模块拆分审查**。关键收益：
1. 每 agent 焦点 15-25 文件，逐行深入而非广度扫描
2. 支持 spec vs 实现对照（需要 agent 同时理解 spec 文档和代码）
3. 跨模块一致性检查（如 scaffold vs generator 格式、login vs refresh 安全校验）
4. 降低误报率（第一轮 1 个误报，第二轮 0 个）

**后续 PR 审查规则**：
- < 50 文件：单 agent 即可
- 50-100 文件：按层拆 2-3 agent
- 100+ 文件：按模块拆 6-8 agent，每 agent ≤ 25 文件
- 安全敏感模块（auth、session、crypto）始终独立 agent
