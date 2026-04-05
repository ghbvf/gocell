# Product Acceptance Criteria — Phase 2: Runtime + Built-in Cells

> 日期: 2026-04-05
> 基于: spec.md (FR-1 ~ FR-13, NFR-1 ~ NFR-5) + tasks.md (42 tasks) + decisions.md (14 decisions)
> 验收者: S7 QA / 产品经理

---

## AC 分级说明

| 级别 | 含义 | 通过要求 |
|------|------|---------|
| **P1（核心功能）** | Phase 2 目标直接相关，运行时正确性必需 | 100% PASS，任一 FAIL 即阻塞交付 |
| **P2（增强功能）** | 提升体验、健壮性但非核心路径 | 允许 SKIP 附书面理由，不阻塞交付 |
| **P3（基础设施）** | 工具链 / CI / 文档 / DevOps | 允许 SKIP，但须在 Phase 3 前补齐 |

---

## FR-1: runtime/http — HTTP 基础设施

### AC-1.1: RequestID 中间件
- **优先级**: P1
- **验收标准**: 请求不携带 `X-Request-Id` 时，中间件生成合法 UUID 写入 ctx 和响应头；请求携带该头时，中间件原样透传不覆盖
- **验证方式**: [单元测试]
- **关联任务**: T-010

### AC-1.2: RealIP 中间件
- **优先级**: P1
- **验收标准**: 从 `X-Forwarded-For`（取第一个非空 IP）或 `X-Real-Ip` 头提取客户端真实 IP，写入 ctx；无代理头时回退到 `RemoteAddr`
- **验证方式**: [单元测试]
- **关联任务**: T-011

### AC-1.3: Recovery 中间件
- **优先级**: P1
- **验收标准**: handler 内 panic 被捕获，返回 HTTP 500 + 标准错误响应体 `{"error": {"code": "ERR_INTERNAL", ...}}`，同时写 `slog.Error` 含完整 stack trace
- **验证方式**: [单元测试]
- **关联任务**: T-012

### AC-1.4: AccessLog 中间件
- **优先级**: P1
- **验收标准**: 每个请求结束后输出一条 slog 结构化日志，包含 method / path / status / duration_ms / request_id 五个字段
- **验证方式**: [单元测试]
- **关联任务**: T-013

### AC-1.5: SecurityHeaders 中间件
- **优先级**: P2
- **验收标准**: 响应头包含 `X-Content-Type-Options: nosniff`、`X-Frame-Options: DENY`、`Strict-Transport-Security: max-age=63072000; includeSubDomains`
- **验证方式**: [单元测试]
- **关联任务**: T-014

### AC-1.6: BodyLimit 中间件
- **优先级**: P1
- **验收标准**: 请求体超过配置限制（默认 1MB）时返回 HTTP 413 + 错误响应体；未超限时正常透传
- **验证方式**: [单元测试]
- **关联任务**: T-015

### AC-1.7: RateLimit 中间件 — 正常限流
- **优先级**: P1
- **验收标准**: 默认 100 req/s, burst 200；超限请求返回 HTTP 429 + `Retry-After` 头（值为退避秒数）
- **验证方式**: [单元测试]
- **关联任务**: T-016

### AC-1.8: RateLimit 中间件 — IP 隔离
- **优先级**: P1
- **验收标准**: 不同 IP 的限流桶相互隔离，IP-A 耗尽配额不影响 IP-B 的正常请求
- **验证方式**: [单元测试]
- **关联任务**: T-016

### AC-1.9: RateLimit 中间件 — 接口可替换
- **优先级**: P2
- **验收标准**: RateLimit 接受 `RateLimiter` 接口（`Allow(key string) bool`），Phase 2 提供 in-memory 实现；接口可在 Phase 3 替换为 Redis 实现
- **验证方式**: [代码审查]
- **关联任务**: T-016

### AC-1.10: 健康检查 — liveness
- **优先级**: P1
- **验收标准**: `GET /healthz` 返回 200 + `{"status": "healthy", "checks": {...}}`；聚合 `Assembly.Health()` 各 Cell 结果，任一 Cell unhealthy 则整体返回 503
- **验证方式**: [集成测试]
- **关联任务**: T-030

### AC-1.11: 健康检查 — readiness
- **优先级**: P1
- **验收标准**: `GET /readyz` 支持注册自定义 readiness checker；所有 checker 通过返回 200，任一失败返回 503 + body 含失败 checker 名
- **验证方式**: [集成测试]
- **关联任务**: T-030

### AC-1.12: 路由构建器 — chi 集成
- **优先级**: P1
- **验收标准**: 支持 `Route(pattern, handler)` / `Group(prefix, middlewares..., routes)` / `Mount(prefix, handler)`；自动注册 `/healthz` + `/readyz` + `/metrics`
- **验证方式**: [集成测试]
- **关联任务**: T-031

### AC-1.13: 路由构建器 — API 版本前缀
- **优先级**: P1
- **验收标准**: 支持 `/api/v1/` 前缀分组，Cell 注册的路由自动挂载到版本前缀下
- **验证方式**: [集成测试]
- **关联任务**: T-031

---

## FR-2: runtime/config — 框架启动配置

### AC-2.1: YAML + 环境变量加载
- **优先级**: P1
- **验收标准**: 从指定路径 YAML 文件加载配置；环境变量覆盖同名 YAML key（例: `SERVER_HTTP_PORT=9090` 覆盖 `server.http.port`）
- **验证方式**: [单元测试]
- **关联任务**: T-020

### AC-2.2: Config 接口
- **优先级**: P1
- **验收标准**: 支持 `Get(key) any`（含嵌套 key 如 `server.http.port`）和 `Scan(dest interface{}) error`（反序列化到 struct）
- **验证方式**: [单元测试]
- **关联任务**: T-020

### AC-2.3: Cell.Init 阶段配置获取
- **优先级**: P1
- **验收标准**: Cell.Init 通过 Dependencies.Config 获取 runtime/config 加载的配置，可读取 server.http.port 等启动参数
- **验证方式**: [集成测试]
- **关联任务**: T-020, T-032

### AC-2.4: 文件变更 Watcher
- **优先级**: P2
- **验收标准**: 配置文件变更后触发注册的 `OnChange(key, value)` 回调；支持注册多个 watcher；watcher 内部错误通过 slog.Warn 记录但不影响主进程运行
- **验证方式**: [单元测试]
- **关联任务**: T-020

### AC-2.5: runtime/config 与 config-core 隔离 [S3 决策点: 决策 5]
- **优先级**: P1
- **验收标准**: config-core Cell 不 import `runtime/config` 包；runtime/config 不 import `cells/` 包。通过 `go build` 和 import 分析验证
- **验证方式**: [代码审查] [单元测试]
- **关联任务**: T-020, T-050, T-067

---

## FR-3: runtime/bootstrap — 统一启动器

### AC-3.1: 启动流程编排
- **优先级**: P1
- **验收标准**: `Bootstrap.Run()` 按序执行: parse config -> init eventbus -> init assembly -> Cell.Init -> Cell.Start -> HTTPRegistrar 路由注册 -> EventRegistrar 事件订阅注册 -> start HTTP server -> start workers -> 阻塞等待信号
- **验证方式**: [集成测试]
- **关联任务**: T-032

### AC-3.2: 选项模式
- **优先级**: P1
- **验收标准**: 支持 `WithConfig(path)` / `WithHTTP(addr)` / `WithWorkers(workers...)` / `WithAssembly(assembly)` 选项函数
- **验证方式**: [单元测试]
- **关联任务**: T-032

### AC-3.3: 启动失败回滚
- **优先级**: P1
- **验收标准**: 启动阶段任一步骤失败（如 HTTP bind 失败），已启动组件按 LIFO 顺序有序关闭，进程退出并输出错误日志
- **验证方式**: [集成测试]
- **关联任务**: T-032

---

## FR-4: runtime/shutdown — 优雅关闭

### AC-4.1: 信号监听
- **优先级**: P1
- **验收标准**: 监听 SIGINT / SIGTERM，收到信号后触发优雅关闭流程
- **验证方式**: [集成测试]
- **关联任务**: T-021

### AC-4.2: 关闭顺序
- **优先级**: P1
- **验收标准**: 关闭按序执行: stop workers -> drain HTTP connections -> stop assembly（反序 Stop cells）-> close config watcher
- **验证方式**: [集成测试]
- **关联任务**: T-021

### AC-4.3: 关闭超时
- **优先级**: P1
- **验收标准**: 关闭超时可配置（默认 30s），超时后强制退出并记录 slog.Error
- **验证方式**: [单元测试]
- **关联任务**: T-021

---

## FR-5: runtime/observability — 可观测性

### AC-5.1: Prometheus 指标注册
- **优先级**: P1
- **验收标准**: HTTP 中间件自动采集请求计数（`http_requests_total`）和延迟直方图（`http_request_duration_seconds`），按 method / path / status 分组
- **验证方式**: [单元测试]
- **关联任务**: T-024

### AC-5.2: /metrics 端点
- **优先级**: P1
- **验收标准**: `GET /metrics` 返回 Prometheus text format 指标数据，包含 AC-5.1 定义的指标
- **验证方式**: [集成测试]
- **关联任务**: T-024, T-031

### AC-5.3: Cell 自定义指标
- **优先级**: P2
- **验收标准**: Cell 可通过 observability API 注册自定义 Counter / Gauge / Histogram，指标在 `/metrics` 端点可见
- **验证方式**: [单元测试]
- **关联任务**: T-024

### AC-5.4: OTel Tracing — span 创建
- **优先级**: P2
- **验收标准**: HTTP 中间件自动创建 span，trace_id / span_id 注入 context；支持 stdout 和 OTLP 两种 exporter 配置
- **验证方式**: [单元测试]
- **关联任务**: T-025

### AC-5.5: slog 结构化日志 — ctx 字段提取
- **优先级**: P1
- **验收标准**: slog Handler 自动从 ctx 提取 trace_id / span_id / request_id / cell_id 字段并附加到日志输出；支持 JSON 和 text 两种输出格式
- **验证方式**: [单元测试]
- **关联任务**: T-026

### AC-5.6: 日志级别运行时可调
- **优先级**: P2
- **验收标准**: 运行时可通过 API 或配置变更调整 slog 日志级别，无需重启进程
- **验证方式**: [单元测试]
- **关联任务**: T-026

---

## FR-6: runtime/worker — 后台 Worker

### AC-6.1: Worker 生命周期
- **优先级**: P1
- **验收标准**: Worker 接口 `Start(ctx) error` / `Stop(ctx) error` 可被 WorkerGroup 并发管理；启动失败的 worker 不阻塞其他 worker
- **验证方式**: [单元测试]
- **关联任务**: T-022

### AC-6.2: 周期性 Job
- **优先级**: P2
- **验收标准**: `NewPeriodicWorker(interval, fn)` 按配置间隔周期执行 fn；Stop 时优雅等待当前执行完成
- **验证方式**: [单元测试]
- **关联任务**: T-022

### AC-6.3: Worker panic 隔离
- **优先级**: P1
- **验收标准**: 单个 worker panic 被捕获并记录 slog.Error，不影响 WorkerGroup 中其他 worker 正常运行
- **验证方式**: [单元测试]
- **关联任务**: T-022

---

## FR-7: runtime/auth — 认证鉴权抽象框架 [S3 决策点: 决策 4]

### AC-7.1: AuthMiddleware — token 验证
- **优先级**: P1
- **验收标准**: `AuthMiddleware(verifier TokenVerifier)` 从 `Authorization: Bearer <token>` 提取 token，调用 `verifier.Verify(ctx, token)`；验证通过时 Claims（sub, roles, exp 等）注入 ctx；验证失败返回 401 + 标准错误响应体
- **验证方式**: [单元测试]
- **关联任务**: T-023

### AC-7.2: runtime/auth 仅定义抽象接口 [S3 决策点]
- **优先级**: P1
- **验收标准**: `runtime/auth` 只定义 `TokenVerifier` 和 `Authorizer` 接口 + 中间件函数，不包含 JWT 解析、密钥管理或 RBAC 策略判定的具体实现。具体实现由 access-core 提供
- **验证方式**: [代码审查]
- **关联任务**: T-023

### AC-7.3: RequireRole — 授权中间件
- **优先级**: P1
- **验收标准**: `RequireRole(authorizer Authorizer, roles ...string)` 从 ctx 获取 Claims，调用 `authorizer.Authorize(ctx, subject, resource, action)`；授权通过放行，失败返回 403
- **验证方式**: [单元测试]
- **关联任务**: T-023

### AC-7.4: ServiceToken — 服务间认证
- **优先级**: P2
- **验收标准**: 基于 shared secret 的 HMAC 签名校验中间件实现完成，签名不匹配返回 401。Journey 验证延迟到 Phase 3
- **验证方式**: [单元测试]
- **关联任务**: T-023

---

## FR-8: cells/access-core — 身份与会话管理

### AC-8.1: identity-manage — 用户 CRUD
- **优先级**: P1
- **验收标准**: 支持 Create / Get / Update / Delete / Lock / Unlock User 操作；Create 成功后发布 `event.user.created.v1` 事件
- **验证方式**: [单元测试]
- **关联任务**: T-051

### AC-8.2: session-login — 密码登录 [S3 决策点: 决策 8]
- **优先级**: P1
- **验收标准**: 密码验证通过后创建 Session，签发 JWT access token + refresh token（RS256 签名）；发布 `event.session.created.v1` 事件。Phase 2 仅支持密码登录，不支持 OIDC
- **验证方式**: [单元测试] [集成测试]
- **关联任务**: T-051

### AC-8.3: session-refresh — token 刷新
- **优先级**: P1
- **验收标准**: 验证 refresh token 有效性后签发新 access + refresh token pair；旧 refresh token 失效（滚动过期）
- **验证方式**: [单元测试]
- **关联任务**: T-051

### AC-8.4: session-logout — session 吊销
- **优先级**: P1
- **验收标准**: 吊销指定 session；发布 `event.session.revoked.v1` 事件；吊销后使用该 session 的 token 调用 session-validate 返回认证失败
- **验证方式**: [单元测试]
- **关联任务**: T-051

### AC-8.5: session-validate — TokenVerifier 实现
- **优先级**: P1
- **验收标准**: 实现 `runtime/auth.TokenVerifier` 接口；JWT RS256 签名验证 + session 状态查询（session 已吊销则验证失败）；返回完整 Claims（sub, roles, exp）
- **验证方式**: [单元测试]
- **关联任务**: T-051

### AC-8.6: authorization-decide — Authorizer 实现
- **优先级**: P1
- **验收标准**: 实现 `runtime/auth.Authorizer` 接口；基于 RBAC 策略判定 `Authorize(ctx, subject, resource, action)` 返回 Allow/Deny
- **验证方式**: [单元测试]
- **关联任务**: T-051

### AC-8.7: rbac-check — 角色查询
- **优先级**: P2
- **验收标准**: 支持 `HasRole(subject, role) bool` 和 `ListRoles(subject) []Role` 查询
- **验证方式**: [单元测试]
- **关联任务**: T-051

### AC-8.8: identity-manage — 账户锁定
- **优先级**: P1
- **验收标准**: 连续登录失败达阈值后自动锁定账户；锁定后发布 `event.user.locked.v1` 事件；锁定期间登录请求被拒绝；管理员可通过 Unlock 操作解锁
- **验证方式**: [单元测试] [集成测试]
- **关联任务**: T-051

### AC-8.9: access-core Cell 生命周期
- **优先级**: P1
- **验收标准**: AccessCore 实现 Cell + HTTPRegistrar + EventRegistrar 接口；Init -> Start -> Health -> Stop 生命周期正常运转
- **验证方式**: [集成测试]
- **关联任务**: T-051

### AC-8.10: Slice 构造时注入 [S3 决策点: 决策 3]
- **优先级**: P1
- **验收标准**: 所有 7 个 Slice 通过 `NewXxxSlice(repo, publisher, logger)` 构造函数注入依赖；`Slice.Init(ctx)` 仅做状态检查不接收新依赖；4 个 session-* Slice 共享 Cell 级 domain/ports
- **验证方式**: [代码审查] [单元测试]
- **关联任务**: T-040, T-051

---

## FR-9: cells/audit-core — 审计链

### AC-9.1: audit-append — HMAC hash chain 写入
- **优先级**: P1
- **验收标准**: `Append(entry)` 写入审计条目，条目使用 HMAC-SHA256 与前一条目的 hash 链接形成 hash chain；发布 `event.audit.appended.v1` 事件
- **验证方式**: [单元测试]
- **关联任务**: T-052

### AC-9.2: audit-verify — 完整性验证
- **优先级**: P1
- **验收标准**: `Verify(range)` 校验指定范围内 hash chain 连续性和 HMAC 签名正确性；验证通过发布 `event.audit.integrity-verified.v1`；任一条目被篡改则返回验证失败 + 失败位置
- **验证方式**: [单元测试]
- **关联任务**: T-052

### AC-9.3: audit-archive — stub 实现
- **优先级**: P2
- **验收标准**: `Archive(before date)` 返回 not-implemented 错误（使用 `pkg/errcode`），不 panic 不静默丢弃
- **验证方式**: [单元测试]
- **关联任务**: T-052

### AC-9.4: audit-query — 审计查询
- **优先级**: P1
- **验收标准**: `Query(filters)` 按时间范围 / 事件类型 / 来源 Cell 过滤审计记录，返回结果列表
- **验证方式**: [单元测试]
- **关联任务**: T-052

### AC-9.5: 事件订阅消费
- **优先级**: P1
- **验收标准**: audit-core 通过 EventRegistrar 注册订阅以下 6 个事件 topic: `event.session.created.v1` / `event.session.revoked.v1` / `event.user.created.v1` / `event.user.locked.v1` / `event.config.changed.v1` / `event.config.rollback.v1`；收到事件后调用 audit-append 写入 hash chain
- **验证方式**: [集成测试]
- **关联任务**: T-052

### AC-9.6: HMAC 密钥管理
- **优先级**: P1
- **验收标准**: HMAC 密钥从启动配置注入；密钥配置为空时 Cell.Init 返回明确错误（`ERR_AUDIT_HMAC_KEY_MISSING` 或类似），阻止启动
- **验证方式**: [单元测试]
- **关联任务**: T-052

### AC-9.7: audit-core Cell 生命周期
- **优先级**: P1
- **验收标准**: AuditCore 实现 Cell + EventRegistrar 接口；Init -> Start -> Health -> Stop 生命周期正常运转
- **验证方式**: [集成测试]
- **关联任务**: T-052

---

## FR-10: cells/config-core — 配置管理

### AC-10.1: config-write — 配置 CRUD
- **优先级**: P1
- **验收标准**: 支持 Create / Update / Delete 配置条目，每次变更自动创建新版本；变更后发布 `event.config.changed.v1` 事件
- **验证方式**: [单元测试]
- **关联任务**: T-050

### AC-10.2: config-read — 配置读取
- **优先级**: P1
- **验收标准**: 支持 Get（按 key 获取最新版本）和 List（分页列出配置条目）；通过 `http.config.get.v1` contract 对外暴露
- **验证方式**: [单元测试]
- **关联任务**: T-050

### AC-10.3: config-publish — 配置发布与回滚
- **优先级**: P1
- **验收标准**: `Publish(version)` 将指定版本标记为生效并发布 `event.config.changed.v1`；`Rollback(version)` 回退到指定历史版本并发布 `event.config.rollback.v1`
- **验证方式**: [单元测试]
- **关联任务**: T-050

### AC-10.4: config-subscribe — 事件订阅
- **优先级**: P1
- **验收标准**: 消费 `event.config.changed.v1` 事件后更新本地缓存；订阅方可通过缓存获取最新配置值
- **验证方式**: [集成测试]
- **关联任务**: T-050

### AC-10.5: feature-flag — 布尔开关 [S3 决策点: 决策 7]
- **优先级**: P1
- **验收标准**: 支持创建布尔类型 Feature Flag（on/off）；`Evaluate(flagKey, subject)` 对布尔 flag 返回 true/false
- **验证方式**: [单元测试]
- **关联任务**: T-050

### AC-10.6: feature-flag — 百分比 rollout [S3 决策点: 决策 7]
- **优先级**: P1
- **验收标准**: 支持百分比 rollout 类型 flag；按 subject hash 取模计算是否命中；相同 subject 对同一 flag 结果稳定（幂等）
- **验证方式**: [单元测试]
- **关联任务**: T-050

### AC-10.7: feature-flag — 范围限制 [S3 决策点: 决策 7]
- **优先级**: P1
- **验收标准**: Phase 2 不支持基于规则的灰度（租户/IP/属性匹配）；传入规则参数时返回明确的 not-supported 错误或忽略规则字段
- **验证方式**: [代码审查]
- **关联任务**: T-050

### AC-10.8: config-core Cell 生命周期
- **优先级**: P1
- **验收标准**: ConfigCore 实现 Cell + HTTPRegistrar + EventRegistrar 接口；Init -> Start -> Health -> Stop 生命周期正常运转
- **验证方式**: [集成测试]
- **关联任务**: T-050

---

## FR-11: 文档需求

### AC-11.1: runtime/ doc.go
- **优先级**: P3
- **验收标准**: runtime/http/middleware / runtime/config / runtime/bootstrap 各有 doc.go，包含可编译的使用示例（`go doc` 可解析）
- **验证方式**: [代码审查]
- **关联任务**: T-063

### AC-11.2: Cell 开发指南
- **优先级**: P3
- **验收标准**: `docs/guides/cell-development-guide.md` 覆盖: 从零创建 Cell -> 声明 cell.yaml -> 实现 Cell 接口 -> 注册路由 -> 注册事件订阅 -> 编写测试 -> 在 assembly 中启动
- **验证方式**: [手动验证]
- **关联任务**: T-064

> **手动验证操作指南（AC-11.2）**:
> 1. 打开 `docs/guides/cell-development-guide.md`
> 2. 按文档步骤创建一个名为 `demo-core` 的最小 Cell（含 1 个 Slice）
> 3. 确认 cell.yaml 必填字段说明完整（id / type / consistencyLevel / owner / schema.primary / verify.smoke）
> 4. 确认文档中的代码示例可编译（复制到临时文件执行 `go build`）
> 5. 确认按文档在 cmd/core-bundle 注册 demo-core 后可正常启动
> 6. 记录任何步骤中的错误或遗漏

### AC-11.3: README.md 更新
- **优先级**: P3
- **验收标准**: README.md 反映 Phase 2 新增能力（runtime 层列表、3 个内建 Cell、Journey 验证状态）
- **验证方式**: [代码审查]
- **关联任务**: T-065

---

## FR-12: DevOps 需求

### AC-12.1: 全量编译
- **优先级**: P1
- **验收标准**: `cd src && go build ./...` 零编译错误，含 cmd/core-bundle 入口
- **验证方式**: [集成测试]
- **关联任务**: T-060, T-066

### AC-12.2: 元数据校验
- **优先级**: P1
- **验收标准**: `gocell validate` 对所有 YAML 元数据校验通过，零 error
- **验证方式**: [集成测试]
- **关联任务**: T-001, T-067

### AC-12.3: Makefile 目标
- **优先级**: P3
- **验收标准**: Makefile 包含 `build` / `test` / `validate` / `generate` / `cover` 目标，各目标可独立执行
- **验证方式**: [手动验证]
- **关联任务**: T-066

> **手动验证操作指南（AC-12.3）**:
> 1. 在项目根目录执行 `make build` — 确认编译成功
> 2. 执行 `make test` — 确认测试运行并输出结果
> 3. 执行 `make validate` — 确认 gocell validate 被调用
> 4. 执行 `make generate` — 确认生成产物更新
> 5. 执行 `make cover` — 确认覆盖率报告输出
> 6. 记录任何目标的执行错误

---

## FR-13: 测试需求

### AC-13.1: runtime/ 覆盖率
- **优先级**: P1
- **验收标准**: `go test ./runtime/... -cover` 每个包覆盖率 >= 80%
- **验证方式**: [集成测试]
- **关联任务**: T-028, T-068

### AC-13.2: cells/ 覆盖率
- **优先级**: P1
- **验收标准**: `go test ./cells/... -cover` 每个 Cell 的 service 层覆盖率 >= 80%，使用 table-driven 测试
- **验证方式**: [集成测试]
- **关联任务**: T-053, T-068

### AC-13.3: kernel/ 覆盖率维持
- **优先级**: P1
- **验收标准**: `go test ./kernel/... -cover` 覆盖率维持 >= 90%，无新增编译错误或测试失败
- **验证方式**: [集成测试]
- **关联任务**: T-068

### AC-13.4: Cell 生命周期集成测试
- **优先级**: P1
- **验收标准**: 每个 Cell（access-core / audit-core / config-core）的 `cell_test.go` 覆盖 Init -> Start -> Health -> Stop 完整生命周期
- **验证方式**: [集成测试]
- **关联任务**: T-050, T-051, T-052

### AC-13.5: 契约测试
- **优先级**: P2
- **验收标准**: 每个 contract 的 provider slice 有契约测试，验证接口签名与 contract.yaml 声明一致
- **验证方式**: [单元测试]
- **关联任务**: T-050, T-051, T-052

### AC-13.6: Journey 测试入口
- **优先级**: P1
- **验收标准**: 8 条 Journey 的 auto passCriteria 各有对应的 Go 测试函数入口
- **验证方式**: [集成测试]
- **关联任务**: T-061, T-062

---

## kernel/ 接口扩展

### AC-K.1: outbox.Subscriber 接口
- **优先级**: P1
- **验收标准**: `kernel/outbox/outbox.go` 新增 `Subscriber` 接口: `Subscribe(ctx, topic, handler) error` + `Close() error`，与已有 `Publisher` 对称
- **验证方式**: [单元测试]
- **关联任务**: T-002

### AC-K.2: Cell 可选注册钩子
- **优先级**: P1
- **验收标准**: `kernel/cell/interfaces.go` 新增 `HTTPRegistrar` / `EventRegistrar` / `RouteMux` 三个接口；不修改现有 Cell 接口签名；kernel/ 不 import chi
- **验证方式**: [单元测试] [代码审查]
- **关联任务**: T-003

---

## runtime/eventbus — 内存事件总线 [S3 决策点: 决策 2]

### AC-EB.1: 事件发布与订阅
- **优先级**: P1
- **验收标准**: 同时实现 `outbox.Publisher` 和 `outbox.Subscriber` 接口；topic-based pub/sub 模式，发布的事件被同 topic 所有订阅者接收
- **验证方式**: [单元测试]
- **关联任务**: T-027

### AC-EB.2: at-most-once 语义 [S3 决策点]
- **优先级**: P1
- **验收标准**: EventBus 为内存实现，进程重启后未消费事件丢失；代码注释和文档明确标注 at-most-once 语义，不模拟持久化
- **验证方式**: [代码审查]
- **关联任务**: T-027

### AC-EB.3: 错误重试与 dead letter
- **优先级**: P1
- **验收标准**: consumer handler 返回 error 时自动重试 3 次（指数退避）；超限路由到 dead letter channel；dead letter 中的消息可通过日志或指标观测
- **验证方式**: [单元测试]
- **关联任务**: T-027

### AC-EB.4: Phase 3 可替换性
- **优先级**: P1
- **验收标准**: Cell 代码通过 `outbox.Publisher` / `outbox.Subscriber` 接口与 EventBus 交互，不直接依赖 `runtime/eventbus` 包的具体类型
- **验证方式**: [代码审查]
- **关联任务**: T-027

---

## Journey Hard Gate (5 条) [S3 决策点: 决策 9]

### AC-J.1: J-sso-login — 密码登录完整路径
- **优先级**: P1
- **验收标准**: 创建用户 -> 密码登录 -> 获得 access + refresh token -> session 记录存在 -> `event.session.created.v1` 事件发布。OIDC 相关 passCriteria 为 manual（Phase 3 验证）
- **验证方式**: [集成测试] [手动验证]（OIDC 部分）
- **关联任务**: T-061

> **手动验证操作指南（AC-J.1 OIDC 部分）**:
> 1. 确认 J-sso-login.yaml 中 OIDC 相关 passCriteria 的 mode 已设为 `manual`
> 2. 在 gate-audit.log 中记录: "OIDC 验证 SKIP，Phase 3 OIDC 适配器就绪后验证"
> 3. 确认密码登录路径的所有 auto passCriteria PASS

### AC-J.2: J-session-refresh — token 刷新
- **优先级**: P1
- **验收标准**: 使用有效 refresh token 调用刷新接口 -> 获得新 token pair -> 旧 refresh token 失效
- **验证方式**: [集成测试]
- **关联任务**: T-061

### AC-J.3: J-session-logout — 登出
- **优先级**: P1
- **验收标准**: 调用登出接口 -> session 标记已吊销 -> `event.session.revoked.v1` 事件发布 -> 使用已吊销 session 的 token 认证失败
- **验证方式**: [集成测试]
- **关联任务**: T-061

### AC-J.4: J-user-onboarding — 用户入职
- **优先级**: P1
- **验收标准**: 创建用户 -> 默认角色分配 -> `event.user.created.v1` 事件发布 -> 新用户可成功密码登录
- **验证方式**: [集成测试]
- **关联任务**: T-061

### AC-J.5: J-account-lockout — 账户锁定
- **优先级**: P1
- **验收标准**: 连续失败登录达阈值 -> 账户自动锁定 -> `event.user.locked.v1` 事件发布 -> 锁定期间登录被拒 -> 管理员解锁后可正常登录
- **验证方式**: [集成测试]
- **关联任务**: T-061

---

## Journey Soft Gate (3 条) [S3 决策点: 决策 9]

### AC-J.6: J-audit-login-trail — 登录审计追踪
- **优先级**: P1
- **验收标准**: 密码登录成功 -> `event.session.created.v1` 通过 in-memory EventBus 传递到 audit-core -> 审计记录写入 hash chain -> `audit-verify` 验证 hash chain 完整性通过
- **验证方式**: [集成测试]
- **关联任务**: T-062

### AC-J.7: J-config-hot-reload — 配置热更新
- **优先级**: P1
- **验收标准**: 通过 config-write 变更配置 -> `event.config.changed.v1` 通过 EventBus 传播 -> 订阅方（access-core / audit-core）接收并应用新配置 -> 所有 Cell 健康检查通过
- **验证方式**: [集成测试]
- **关联任务**: T-062

### AC-J.8: J-config-rollback — 配置回滚
- **优先级**: P1
- **验收标准**: 调用 config-publish rollback(version) -> 配置回退到指定版本 -> `event.config.rollback.v1` 事件发布 -> 订阅 Cell 应用回滚配置 -> 审计记录回滚操作
- **验证方式**: [集成测试]
- **关联任务**: T-062

---

## NFR 验收标准

### AC-NFR.1: 分层依赖隔离
- **优先级**: P1
- **验收标准**: (a) kernel/ 不 import runtime/ / cells/ / adapters/；(b) runtime/ 不 import cells/ / adapters/；(c) cells/ 不 import adapters/；(d) cells/ 之间不 import 另一个 Cell 的 internal/。通过 `go build` + `gocell validate` TOPO 规则验证
- **验证方式**: [集成测试] [代码审查]
- **关联任务**: T-067

### AC-NFR.2: 外部依赖白名单
- **优先级**: P1
- **验收标准**: Phase 2 新增直接依赖仅限 6 个（go-chi/chi/v5, golang.org/x/crypto, fsnotify/fsnotify, prometheus/client_golang, go.opentelemetry.io/otel, golang-jwt/jwt/v5）；`go mod graph` 无白名单外的新增直接依赖
- **验证方式**: [手动验证]
- **关联任务**: T-069

> **手动验证操作指南（AC-NFR.2）**:
> 1. 执行 `cd src && go mod graph | grep -v '@' | sort` 查看直接依赖列表
> 2. 逐一确认每个直接依赖在以下白名单中:
>    - `github.com/go-chi/chi/v5`
>    - `golang.org/x/crypto`
>    - `github.com/fsnotify/fsnotify`
>    - `github.com/prometheus/client_golang`
>    - `go.opentelemetry.io/otel`
>    - `github.com/golang-jwt/jwt/v5`
> 3. 如有白名单外新增直接依赖，记录包名并标注引入理由
> 4. 传递依赖不计入限制

### AC-NFR.3: 错误处理规范
- **优先级**: P1
- **验收标准**: (a) 新增代码无裸 `errors.New` 对外暴露，全部使用 `pkg/errcode`；(b) 对外错误响应格式统一为 `{"error": {"code": "ERR_*", "message": "...", "details": {}}}`；(c) domain 层不返回 HTTP 状态码
- **验证方式**: [代码审查]
- **关联任务**: T-067

### AC-NFR.4: 日志规范
- **优先级**: P1
- **验收标准**: (a) 新增代码无 `fmt.Println` / `log.Printf`，全部使用 `slog`；(b) slog.Error 调用必须含完整 error 和至少一个关联业务字段
- **验证方式**: [代码审查]
- **关联任务**: T-067

### AC-NFR.5: 认知复杂度
- **优先级**: P2
- **验收标准**: 新增函数认知复杂度 <= 15（可通过静态分析工具验证）
- **验证方式**: [代码审查]
- **关联任务**: T-067

---

## YAML 元数据修正（Wave 0）

### AC-W0.1: 元数据一致性
- **优先级**: P1
- **验收标准**: (a) audit-append slice.yaml 含 6 个 subscribe contractUsage；(b) config-subscribe slice.yaml 含 subscribe contractUsage；(c) identity-manage slice.yaml 含 http.auth.me.v1 serve；(d) 新建 http.auth.refresh.v1 contract.yaml；(e) session-refresh slice.yaml 含 serve contractUsage
- **验证方式**: [集成测试]
- **关联任务**: T-001

### AC-W0.2: gocell validate 零 error
- **优先级**: P1
- **验收标准**: 完成 Wave 0 所有 YAML 修正后，`gocell validate` 输出零 error
- **验证方式**: [集成测试]
- **关联任务**: T-001, T-005

---

## core-bundle 集成

### AC-CB.1: 启动入口
- **优先级**: P1
- **验收标准**: `cmd/core-bundle/main.go` 使用 Bootstrap 编排 3 个 Cell，注册顺序: config-core -> access-core -> audit-core（provider 先于 consumer）
- **验证方式**: [集成测试]
- **关联任务**: T-060

### AC-CB.2: Cell 注册顺序硬编码 [S3 决策点: 决策 6]
- **优先级**: P1
- **验收标准**: Phase 2 注册顺序在 main.go 中硬编码，不使用自动拓扑排序（Phase 3 再实现）
- **验证方式**: [代码审查]
- **关联任务**: T-060

### AC-CB.3: 可启动验证
- **优先级**: P1
- **验收标准**: `go build ./cmd/core-bundle/...` 编译成功；启动后 `/healthz` 返回 200 + 3 个 Cell 均 healthy
- **验证方式**: [集成测试] [手动验证]
- **关联任务**: T-060

> **手动验证操作指南（AC-CB.3）**:
> 1. 执行 `cd src && go build -o /tmp/core-bundle ./cmd/core-bundle`
> 2. 准备最小配置文件 `config.yaml`（含 server.http.port、log.level、audit.hmac_key）
> 3. 执行 `/tmp/core-bundle --config config.yaml`
> 4. 在另一终端执行 `curl http://localhost:<port>/healthz`
> 5. 确认响应 200 + body 中 access-core / audit-core / config-core 三个 Cell 均为 healthy
> 6. 发送 SIGINT（Ctrl+C）确认优雅关闭日志输出
> 7. 记录启动耗时和关闭耗时

---

## 汇总

| 级别 | 总数 | 必须 PASS |
|------|------|----------|
| P1 | 64 | 全部 |
| P2 | 12 | 允许 SKIP 附理由 |
| P3 | 4 | 允许 SKIP |
| **合计** | **80** | P1 全部 PASS 方可交付 |

### S3 决策点覆盖清单

| 决策 | 对应 AC |
|------|---------|
| 决策 2: EventBus at-most-once | AC-EB.2, AC-EB.3 |
| 决策 3: Slice 构造时注入 | AC-8.10 |
| 决策 4: runtime/auth 抽象框架 | AC-7.1, AC-7.2, AC-7.3 |
| 决策 5: runtime/config vs config-core 边界 | AC-2.5 |
| 决策 6: Cell 注册顺序硬编码 | AC-CB.2 |
| 决策 7: Feature Flag 最小集 | AC-10.5, AC-10.6, AC-10.7 |
| 决策 8: OIDC 仅密码登录 | AC-8.2, AC-J.1 |
| 决策 9: Journey Hard/Soft Gate 分级 | AC-J.1 ~ AC-J.8 |

### 手动验证清单（供 S7 使用者）

| AC 编号 | 内容 | 操作指南位置 |
|---------|------|-------------|
| AC-11.2 | Cell 开发指南可操作性 | 本文 AC-11.2 下方 |
| AC-12.3 | Makefile 目标可执行性 | 本文 AC-12.3 下方 |
| AC-J.1 | J-sso-login OIDC 部分 SKIP 确认 | 本文 AC-J.1 下方 |
| AC-NFR.2 | 外部依赖白名单审计 | 本文 AC-NFR.2 下方 |
| AC-CB.3 | core-bundle 可启动 + 健康检查 | 本文 AC-CB.3 下方 |
