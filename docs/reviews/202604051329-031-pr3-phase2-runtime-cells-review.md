# PR #3 Review: Phase 2 — Runtime + Built-in Cells

> **PR**: feat/001-phase2-runtime-cells → main
> **Scope**: 295 files, +34575/-2654 | 182 Go, 75 MD, 3 YAML
> **审查日期**: 2026-04-05
> **审查方式**: 3 并行 agent 分层审查（kernel / cells / runtime）

---

## 总览

| 类别 | 数量 | 阻塞? |
|------|------|-------|
| 安全问题 | 6 | Yes |
| 架构违规 | 1 | Yes |
| Concerns | 21 | No, but should fix |
| 亮点 | 7 | — |

---

## BLOCKING — 安全问题（6 个）

### S-2 [Critical] session logout 没有持久化 revoke 状态

**文件**: `src/cells/access-core/slices/sessionlogout/service.go:48-70`

`Logout` 方法获取 session（in-memory repo 返回 clone），对 clone 调用 `session.Revoke()`，发布事件、写日志——但 **从未调用 `s.sessionRepo.Update(ctx, session)`** 持久化撤销状态。撤销仅存在于局部变量，函数返回即丢失。

后果：
- "已退出"的 session 实际仍然有效，access token 继续工作直到自然过期
- 再次调用 Logout 不会检测到已撤销（幂等检查永远不触发）
- session-refresh slice 会愉快地续期一个"已撤销"的 session

**修复**: 在 `session.Revoke()` 之后添加 `s.sessionRepo.Update(ctx, session)`。

### S-1 [High] ERR_AUTH_INVALID_TOKEN 映射到 HTTP 500 而不是 401

**文件**: `src/pkg/httputil/response.go:68-88`

`ERR_AUTH_INVALID_TOKEN` 不匹配 `mapCodeToStatus` 的任何规则（包含 `INVALID` 但映射检查的是 `INVALID_INPUT`，也不匹配 `UNAUTHORIZED`/`LOGIN_FAILED`/`REFRESH_FAILED`），fallthrough 到默认 500。

**修复**: 在 `mapCodeToStatus` 中添加 `INVALID_TOKEN` → 401 映射。

### S-R1 [High] RealIP 中间件无条件信任 X-Forwarded-For

**文件**: `src/runtime/http/middleware/real_ip.go:25-30`

攻击者可设置 `X-Forwarded-For: 127.0.0.1` 绕过 IP 级 rate limit。无 trusted proxies 列表。

**修复**: 添加 `TrustedProxies []string` 参数，仅在连接 RemoteAddr 属于可信代理时信任转发头。

### S-R2 [High] ServiceToken HMAC 只签 method+path

**文件**: `src/runtime/auth/servicetoken.go:30-31`

```go
mac.Write([]byte(r.Method + " " + r.URL.Path))
```

不含 query string、body 或 timestamp/nonce。Token 可重放，且 `GET /internal/v1/users` 的 token 对 `GET /internal/v1/users?admin=true` 同样有效。

**修复**: 至少包含 query string。理想方案：加入 timestamp + 短窗口（如 5 分钟）。

### S-R3 [Medium] ServiceToken 不拒绝空 secret

**文件**: `src/runtime/auth/servicetoken.go:14`

`ServiceTokenMiddleware(nil)` 或 `ServiceTokenMiddleware([]byte{})` 会用空 key 计算 HMAC，任何人可伪造有效 token。

**修复**: `len(secret) == 0` 时 panic 或返回 error。

### S-R4 [Low] RequestID 接受任意长度客户端输入

**文件**: `src/runtime/http/middleware/request_id.go:22-24`

无长度限制或字符校验。恶意客户端可注入超长字符串或控制字符，污染日志（log injection）。

**修复**: 限制最大 128 字符，拒绝含控制字符的输入，不合法时重新生成。

---

## BLOCKING — 架构违规（1 个）

### V-1 slice/verify.go 使用 fmt.Errorf 导出错误

**文件**: `src/kernel/slice/verify.go` — 7 处（行 57, 84, 113, 153, 156, 159, 215）

导出方法 `VerifySlice`、`VerifyCell`、`RunJourney` 返回 `fmt.Errorf(...)` 给调用方。违反 CLAUDE.md 规定的 `pkg/errcode` 规范。

**修复**: 将 7 处 `fmt.Errorf` 替换为 `errcode.New` 或 `errcode.Wrap`。

---

## CONCERNS — kernel 层（6 个）

### C-K1 Start 和 StartWithConfig ~95% 重复代码

**文件**: `src/kernel/assembly/assembly.go:83-132` vs `174-220`

~40 行 copy-paste，仅 `deps.Config` 赋值方式不同。任何 start/rollback 逻辑 bug 需修两处。

**建议**: `Start` 委托 `StartWithConfig(ctx, make(map[string]any))`，或提取 `startInternal`。

### C-K2 Stop 允许从 stateStopped 重复调用

**文件**: `src/kernel/assembly/assembly.go:138-161`

仅防护 `stateStopping` 重入，未防护 `stateStopped` 再次调用。依赖子 Cell 自身处理 double-stop。

**建议**: 添加 `if a.state != stateStarted { return nil }`。

### C-K3 depcheck.go 重复定义 isProviderRole

**文件**: `src/kernel/governance/depcheck.go:214-221`

与 `cell.IsProviderRole` 功能重复，仅参数类型不同（`string` vs `ContractRole`）。

**建议**: 调用 `cell.IsProviderRole(cell.ContractRole(cu.Role))`。

### C-K4 FMT-09 / FMT-08 顺序问题

**文件**: `src/kernel/governance/rules_fmt.go:222-269`

源码中 FMT-09 在 FMT-08 前面。`validate.go` 调用序 FMT-08 → FMT-09，导致 invalid-kind 的 contract 同时报两个错误。

**建议**: 先调 FMT-09（kind 合法性）再调 FMT-08（前缀匹配）。

### C-K5 helpers.go isWithinRoot 跨平台路径边界

**文件**: `src/kernel/governance/helpers.go:120-124`

前缀匹配依赖 `os.PathSeparator`，跨平台可能有边界问题。GoCell 当前仅 Linux/macOS，风险低。

### C-K6 StatusBoardEntry YAML tag journeyId

**文件**: `src/kernel/metadata/types.go:138`

`yaml:"journeyId"` — 需确认与现有 YAML 文件和命名约定一致。项目禁用了 `cellId`/`sliceId` 等旧名，但 `journeyId` 未在禁用列表中。

---

## CONCERNS — cells 层（7 个）

### C-C1 ID 生成用 time.Now().UnixNano()，并发碰撞

**文件**: 5 处 service.go（identitymanage, sessionlogin, auditappend, configwrite, configpublish）

逻辑在 service 层，会带入生产。并发请求在同一纳秒内产生相同 ID → map 覆盖。

**建议**: 使用 UUID 或 atomic counter。

### C-C2 订阅 goroutine 用 context.Background()，Stop 时泄漏

**文件**: `src/cells/audit-core/cell.go:148-155`, `src/cells/config-core/cell.go:159-165`

`RegisterSubscriptions` 的 goroutine 无取消机制。

**建议**: Init 时创建 `context.WithCancel`，Stop 时调 `cancel()`。

### C-C3 UserRepository.Update byName 索引残留

**文件**: `src/cells/access-core/internal/mem/user_repo.go:73-85`

改 username 时未删除旧 `byName` 条目。当前 `identitymanage.Update` 不改 username，但 repo 实现有潜伏 bug。

### C-C4 audit hash chain 状态纯内存，重启后断链

**文件**: `src/cells/audit-core/slices/auditappend/service.go:47-53`

重启后 `PrevHash = ""`，不连接已持久化条目。破坏链完整性。

**建议**: Init 时从 repo 加载最后一条 entry 来 seed chain 初始状态。

### C-C5 issueToken 重复实现

**文件**: `sessionlogin/service.go:147-160`, `sessionrefresh/service.go:118-131`

完全相同的 token 生成逻辑。

**建议**: 提取到 `access-core/internal/token` 共享包。

### C-C6 audit query 时间参数解析失败静默忽略

**文件**: `src/cells/audit-core/slices/auditquery/handler.go:29-38`

`time.Parse` 失败时不报错，零值导致返回全部条目。应返回 400。

### C-C7 TopicConfigChanged 常量在 3 个 slice 中重复定义

**文件**: configwrite/configpublish/configsubscribe 各自定义

违反 EventBus 规范："stream 名 ≥ 3 次使用抽常量，禁止重复定义"。

**建议**: 提取到 config-core 包级或 internal/constants。

---

## CONCERNS — runtime 层（8 个）

### C-R1 shutdown.runHooks 成功但 ctx 过期时返回 DeadlineExceeded

**文件**: `src/runtime/shutdown/shutdown.go:87`

所有 hook 成功执行但 context 恰好过期 → 返回 `context.DeadlineExceeded`，误导调用方。

**建议**: hook 全部成功时返回 `nil`。

### C-R2 statusRecorder 在 3 个包中重复

**文件**: `middleware/access_log.go`, `observability/tracing/tracing.go`, `observability/metrics/metrics.go`

三个近乎相同的 ResponseWriter wrapper，且都不支持 `http.Flusher`/`http.Hijacker`（SSE/WebSocket 不工作）。

**建议**: 提取到 `pkg/httputil`，实现接口委托。

### C-R3 access_log statusRecorder 未处理隐式 WriteHeader

**文件**: `src/runtime/http/middleware/access_log.go:12-18`

`Write()` 无显式 `WriteHeader()` 时，status 字段初始化为 200 掩盖了未观测。`metrics.go` 的 `metricsRecorder` 正确处理了此场景。

### C-R4 EventBus entry ID 用 UnixNano，高并发不唯一

**文件**: `src/runtime/eventbus/eventbus.go:88`

dev/test 场景可接受，但应文档注明或改用 UUID。

### C-R5 auth middleware warn 日志缺 request_id

**文件**: `src/runtime/auth/middleware.go:26-29, 72-75`

token 验证失败的 warn 日志缺少 `request_id` 关联字段，违反可观测性规范。

### C-R6 config watcher 无 debounce

**文件**: `src/runtime/config/watcher.go:58`

vim/IDE 保存触发多次 Write/Create 事件，导致多次 reload（可能读到半写文件）。

**建议**: 添加 ~100ms debounce timer。

### C-R7 WorkerGroup.Stop 注释说 reverse order 但实际并发

**文件**: `src/runtime/worker/worker.go:75-99`

循环逆序但每个 Stop 在独立 goroutine，无实际顺序保证。注释误导。

### C-R8 httputil.mapCodeToStatus 用 strings.Contains 匹配

**文件**: `src/pkg/httputil/response.go:67-89`

顺序敏感且脆弱。`ERR_SOMETHING_NOT_FOUND_VALIDATION` 会匹配 `NOT_FOUND` 而不是 `VALIDATION`。

**建议**: 改用显式 map 或 `strings.HasSuffix`。

---

## 亮点

1. **0 架构依赖违规** — kernel 不依赖 runtime/cells，cells 不跨 Cell import，runtime 不依赖 cells
2. **kernel 覆盖率 93-100%**，table-driven 测试质量高
3. **bcrypt 密码哈希正确**，`UserResponse` DTO 正确排除 `PasswordHash`
4. **in-memory repo 全部 clone 语义 + RWMutex**，无竞态
5. **Recovery 中间件不泄露内部错误**，AccessLog 不 dump body
6. **Bootstrap LIFO 回滚模式**（ref: uber-go/fx）实现正确
7. **governance validate** 注入 `now`/`fileExists`，测试可确定性执行

---

## 建议修复优先级

| 优先级 | 项目 | 工作量 |
|--------|------|--------|
| P0 | S-2 logout 不生效 | 1 行 |
| P0 | S-1 token 错误码映射 | 1 行 |
| P1 | S-R2+S-R3 ServiceToken HMAC + 空 secret | ~20 行 |
| P1 | S-R1 RealIP trusted proxies | ~30 行 |
| P1 | V-1 errcode 替换 | 7 处 |
| P2 | C-C2 goroutine 泄漏 | ~15 行/cell |
| P2 | C-C1 UUID 替换 UnixNano | 5 处 |
| P2 | C-R2 statusRecorder 提取 | ~40 行 |
| P3 | 其余 concerns | 按需 |
