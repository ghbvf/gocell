# Requirements Checklist — Phase 2: Runtime + Built-in Cells

## runtime/ 层

### FR-1: HTTP 基础设施
- [ ] FR-1.1.1: middleware/request_id.go + test
- [ ] FR-1.1.2: middleware/real_ip.go + test
- [ ] FR-1.1.3: middleware/recovery.go + test
- [ ] FR-1.1.4: middleware/access_log.go + test
- [ ] FR-1.1.5: middleware/security_headers.go + test
- [ ] FR-1.1.6: middleware/body_limit.go + test
- [ ] FR-1.1.7: middleware/rate_limit.go + test
- [ ] FR-1.2: health/health.go + test（/healthz + /readyz）
- [ ] FR-1.3: router/router.go + test（chi-based 路由构建器）

### FR-2: 配置管理
- [ ] FR-2.1: config/config.go + test（YAML/env 加载 + Scan）
- [ ] FR-2.2: config/watcher.go + test（文件变更 watcher）

### FR-3: 统一启动器
- [ ] FR-3: bootstrap/bootstrap.go + test

### FR-4: 优雅关闭
- [ ] FR-4: shutdown/shutdown.go + test

### FR-5: 可观测性
- [ ] FR-5.1: observability/metrics/metrics.go + test
- [ ] FR-5.2: observability/tracing/tracing.go + test
- [ ] FR-5.3: observability/logging/logging.go + test

### FR-6: 后台 Worker
- [ ] FR-6: worker/worker.go + periodic.go + test

### FR-7: 认证鉴权
- [ ] FR-7.1: auth/jwt/jwt.go + test
- [ ] FR-7.2: auth/rbac/rbac.go + test
- [ ] FR-7.3: auth/servicetoken/servicetoken.go + test

## cells/ 层

### FR-8: access-core
- [ ] FR-8.0: cell.go + cell_test.go（Cell 接口实现 + 生命周期测试）
- [ ] FR-8.D: internal/domain/（User, Session, Role 领域模型）
- [ ] FR-8.P: internal/ports/（UserRepository, SessionRepository, RoleRepository 接口）
- [ ] FR-8.1: slices/identity-manage（handler + service + test）
- [ ] FR-8.2: slices/session-login（handler + service + test）
- [ ] FR-8.3: slices/session-refresh（handler + service + test）
- [ ] FR-8.4: slices/session-logout（handler + service + test）
- [ ] FR-8.5: slices/session-validate（handler + service + test）
- [ ] FR-8.6: slices/authorization-decide（handler + service + test）
- [ ] FR-8.7: slices/rbac-check（handler + service + test）

### FR-9: audit-core
- [ ] FR-9.0: cell.go + cell_test.go
- [ ] FR-9.D: internal/domain/（AuditEntry, HashChain 领域模型）
- [ ] FR-9.P: internal/ports/（AuditRepository, ArchiveStore 接口）
- [ ] FR-9.1: slices/audit-append（handler + service + test）
- [ ] FR-9.2: slices/audit-verify（handler + service + test）
- [ ] FR-9.3: slices/audit-archive（handler + service + test）
- [ ] FR-9.4: slices/audit-query（handler + service + test）

### FR-10: config-core
- [ ] FR-10.0: cell.go + cell_test.go
- [ ] FR-10.D: internal/domain/（ConfigEntry, ConfigVersion, FeatureFlag 领域模型）
- [ ] FR-10.P: internal/ports/（ConfigRepository, FlagRepository 接口）
- [ ] FR-10.1: slices/config-write（handler + service + test）
- [ ] FR-10.2: slices/config-read（handler + service + test）
- [ ] FR-10.3: slices/config-publish（handler + service + test）
- [ ] FR-10.4: slices/config-subscribe（handler + service + test）
- [ ] FR-10.5: slices/feature-flag（handler + service + test）

## 横切关注点

### FR-11: 文档
- [ ] FR-11.1: runtime/ 各 package doc.go
- [ ] FR-11.2: Cell 开发指南文档
- [ ] FR-11.3: README.md 更新

### FR-12: DevOps
- [ ] FR-12.1: `go build ./...` 编译通过
- [ ] FR-12.2: `gocell validate` 全部 PASS
- [ ] FR-12.3: Makefile/taskfile build/test/validate/generate 目标

### FR-13: 测试
- [ ] FR-13.1: runtime/ 覆盖率 >= 80%
- [ ] FR-13.2: cells/ 覆盖率 >= 80%
- [ ] FR-13.3: kernel/ 覆盖率维持 >= 90%
- [ ] FR-13.4: 每个 Cell 生命周期集成测试
- [ ] FR-13.5: contract test（provider slice 契约验证）
- [ ] FR-13.6: 8 条 journey 端到端验证

## NFR
- [ ] NFR-1: 依赖隔离（runtime/ 不 import cells/adapters/; cells/ 不 import adapters/）
- [ ] NFR-2: 外部依赖白名单控制
- [ ] NFR-3: 错误处理使用 pkg/errcode
- [ ] NFR-4: 日志使用 slog 结构化
- [ ] NFR-5: 函数认知复杂度 <= 15

## Gate 验证
- [ ] 3 个 Cell 在 core-bundle assembly 中编译启动
- [ ] 8 条 Journey 端到端通过
