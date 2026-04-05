# Tasks — Phase 2: Runtime + Built-in Cells

> [P] = 可并行, [S] = 串行依赖前置任务
> 依赖关系: `→` 表示"依赖于"

---

## Wave 0: 前置准备

### T-001: YAML 元数据修正 [S]
- 修正 audit-append slice.yaml 添加 6 个 subscribe contractUsage
- 修正 config-subscribe slice.yaml 添加 subscribe contractUsage
- 修正 identity-manage slice.yaml 添加 http.auth.me.v1 serve
- 新建 http.auth.refresh.v1 contract.yaml
- 修正 session-refresh slice.yaml 添加 serve contractUsage
- 更新 event contract subscribers 列表 (audit-core 加入)
- 更新 J-session-refresh.yaml contracts
- 更新 J-sso-login.yaml OIDC passCriteria mode=manual
- 验证: `gocell validate` 零 error

### T-002: kernel/outbox Subscriber 接口 [S] → T-001
- 在 kernel/outbox/outbox.go 新增 Subscriber 接口
- 更新 outbox_test.go 接口合规性测试
- 验证: go build + go test

### T-003: kernel/cell 可选注册钩子 [P] → T-001
- 在 kernel/cell/interfaces.go 新增 HTTPRegistrar / EventRegistrar / RouteMux
- 更新 interfaces_test.go 类型断言
- 验证: go build + go test

### T-004: CLAUDE.md 依赖规则更新 [P] → T-001
- 补充 "runtime/ 可依赖 kernel/ 和 pkg/"

### T-005: Wave 0 Gate 验证 [S] → T-002, T-003, T-004
- `go build ./... && go test ./... && gocell validate`

---

## Wave 1: runtime/ 独立模块

### T-010: runtime/http/middleware — request_id [P] → T-005
- request_id.go + request_id_test.go
- 对标: Kratos middleware
- 验证: 覆盖率 >= 80%

### T-011: runtime/http/middleware — real_ip [P] → T-005
- real_ip.go + real_ip_test.go

### T-012: runtime/http/middleware — recovery [P] → T-005
- recovery.go + recovery_test.go
- 对标: go-zero recoverhandler

### T-013: runtime/http/middleware — access_log [P] → T-005
- access_log.go + access_log_test.go

### T-014: runtime/http/middleware — security_headers [P] → T-005
- security_headers.go + security_headers_test.go

### T-015: runtime/http/middleware — body_limit [P] → T-005
- body_limit.go + body_limit_test.go

### T-016: runtime/http/middleware — rate_limit [P] → T-005
- rate_limit.go + rate_limit_test.go
- 包含 RateLimiter 接口 + in-memory 实现
- AC: 429 + Retry-After + 默认 100 req/s burst 200

### T-020: runtime/config [P] → T-005
- config.go + watcher.go + config_test.go + watcher_test.go
- 对标: go-micro config
- 验证: 覆盖率 >= 80%

### T-021: runtime/shutdown [P] → T-005
- shutdown.go + shutdown_test.go
- SIGINT/SIGTERM + 可配置超时

### T-022: runtime/worker [P] → T-005
- worker.go + periodic.go + worker_test.go
- 对标: go-zero ServiceGroup

### T-023: runtime/auth — 抽象接口 + 中间件 [P] → T-005
- auth.go (TokenVerifier + Authorizer 接口定义)
- middleware.go (AuthMiddleware + RequireRole)
- servicetoken.go
- tests
- 对标: Kratos middleware/auth

### T-024: runtime/observability/metrics [P] → T-005
- metrics.go + metrics_test.go
- Prometheus 注册器 + HTTP 中间件

### T-025: runtime/observability/tracing [P] → T-005
- tracing.go + tracing_test.go
- OTel tracer + stdout exporter

### T-026: runtime/observability/logging [P] → T-005
- logging.go + logging_test.go
- slog Handler + ctx 字段提取

### T-027: runtime/eventbus [P] → T-002, T-005
- eventbus.go + eventbus_test.go
- 实现 outbox.Publisher + outbox.Subscriber
- at-most-once + 3x 重试 + dead letter
- 对标: Watermill message

### T-028: Wave 1 Gate 验证 [S] → T-010..T-027
- `go test ./runtime/... -cover` >= 80%

---

## Wave 2: runtime/ 集成 + Cell domain

### T-030: runtime/http/health [S] → T-028
- health.go + health_test.go
- /healthz + /readyz + Assembly.Health() 集成

### T-031: runtime/http/router [S] → T-010..T-016, T-030
- router.go + router_test.go
- chi-based + 自动挂载 /healthz /readyz /metrics

### T-032: runtime/bootstrap [S] → T-020, T-021, T-022, T-027, T-031
- bootstrap.go + bootstrap_test.go
- 对标: Uber fx app.go, Kratos app.go
- 启动流程: config → eventbus → assembly → Cell.Init → Cell.Start → HTTPRegistrar → EventRegistrar → HTTP server → workers

### T-040: access-core domain + ports [P] → T-005
- internal/domain/user.go + session.go + role.go + tests
- internal/ports/user_repo.go + session_repo.go + role_repo.go

### T-041: audit-core domain + ports [P] → T-005
- internal/domain/entry.go + hashchain.go + tests
- internal/ports/audit_repo.go + archive_store.go
- HMAC-SHA256 hash chain 算法 + 验证

### T-042: config-core domain + ports [P] → T-005
- internal/domain/config_entry.go + version.go + feature_flag.go + tests
- internal/ports/config_repo.go + flag_repo.go
- Feature flag evaluate (boolean + percentage rollout)

### T-033: Wave 2 Gate 验证 [S] → T-032, T-040..T-042
- bootstrap 可编排 Assembly 启动/关闭
- domain 模型编译通过 + 测试绿色

---

## Wave 3: Cell 完整实现

### T-050: config-core Cell 实现（参考实现）[S] → T-033
- cell.go — ConfigCore 实现 Cell + HTTPRegistrar + EventRegistrar
- cell_test.go — 生命周期集成测试
- 5 slices: handler.go + service.go + service_test.go 各
- in-memory ConfigRepository + FlagRepository 桩
- 验证: go test ./cells/config-core/... -cover >= 80%

### T-051: access-core Cell 实现 [S] → T-033, T-050(模式参考)
- cell.go — AccessCore + HTTPRegistrar + EventRegistrar
- cell_test.go — 生命周期集成测试
- session-validate → TokenVerifier 实现
- authorization-decide → Authorizer 实现
- 7 slices: handler + service + test
- in-memory repos 桩
- JWT RS256 签发/验证 (golang-jwt/jwt/v5)
- 验证: go test ./cells/access-core/... -cover >= 80%

### T-052: audit-core Cell 实现 [S] → T-033, T-050(模式参考)
- cell.go — AuditCore + EventRegistrar
- cell_test.go — 生命周期集成测试
- 4 slices: handler + service + test (audit-archive = stub)
- 注册 6 个事件订阅
- HMAC 密钥从配置注入
- in-memory AuditRepository 桩
- 验证: go test ./cells/audit-core/... -cover >= 80%

### T-053: Wave 3 Gate 验证 [S] → T-050..T-052
- 3 Cell 各自编译 + 测试绿色 + 覆盖率 >= 80%

---

## Wave 4: 集成 + 验证 + 文档

### T-060: cmd/core-bundle 更新 [S] → T-053
- 更新 main.go 使用 Bootstrap 编排
- 注册顺序: config-core → access-core → audit-core
- 验证: go build + 可启动

### T-061: Journey Hard Gate 测试 [S] → T-060
- J-sso-login (密码登录路径)
- J-session-refresh
- J-session-logout
- J-user-onboarding
- J-account-lockout
- 全部 PASS

### T-062: Journey Soft Gate 测试 [S] → T-060
- J-audit-login-trail (跨 Cell EventBus)
- J-config-hot-reload (跨 Cell EventBus)
- J-config-rollback (跨 Cell EventBus)

### T-063: 文档 — runtime/ doc.go [P] → T-053
- runtime/http/middleware/doc.go
- runtime/config/doc.go
- runtime/bootstrap/doc.go
- 每个含可编译示例

### T-064: 文档 — Cell 开发指南 [P] → T-053
- docs/guides/cell-development-guide.md
- 从零创建自定义 Cell 的完整步骤

### T-065: 文档 — README.md 更新 [P] → T-053
- 更新 README.md 反映 Phase 2 新增能力

### T-066: DevOps — Makefile [P] → T-005
- Makefile 目标: build / test / validate / generate / cover

### T-067: 内核集成验证 [S] → T-060
- C-01~C-04: 分层隔离验证 (go build + gocell validate)
- C-05~C-08: Cell 生命周期状态机验证
- C-09~C-13: 元数据合规验证
- C-14~C-20: 契约完整性验证
- C-21~C-23: 一致性等级验证
- C-24~C-26: 验证闭环
- C-27~C-29: 错误处理规范

### T-068: 覆盖率 Gate [S] → T-060
- runtime/ >= 80%
- cells/ >= 80%
- kernel/ >= 90% (维持)

### T-069: Wave 4 最终 Gate [S] → T-061..T-068
- go build + go test + gocell validate
- 5 Hard Gate Journey PASS
- 3 Soft Gate Journey PASS (允许 stub 辅助)
- 覆盖率达标

---

## 非代码任务补充

### T-070: Docker 环境配置 [P] → T-005
- 暂不适用（Phase 2 无 Docker 部署，N/A per phase-charter.md）
- Makefile 中 docker 目标预留为 placeholder

### T-071: E2E/集成测试编写 [S] → T-060
- 暂无 Playwright（纯后端 Phase）
- 集成测试: 使用 go test 的 TestMain 启动 assembly 并执行 HTTP 请求
- 覆盖 J-sso-login 完整密码登录流程

### T-072: OpenAPI 文档生成 [P] → T-053
- Phase 2 暂不自动生成 OpenAPI（无 adapter，接口尚在变动）
- 在 handler.go 中通过 GoDoc 注释描述端点签名

---

## 任务统计

| Wave | 任务数 | 类型 |
|------|--------|------|
| Wave 0 | 5 | 前置准备 |
| Wave 1 | 14 | runtime/ 独立 |
| Wave 2 | 7 | runtime/ 集成 + domain |
| Wave 3 | 4 | Cell 实现 |
| Wave 4 | 12 | 集成 + 验证 + 文档 |
| **合计** | **42** | |
