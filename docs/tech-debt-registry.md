# Tech Debt Registry -- GoCell

全局技术债务注册表。跨 Phase 追踪所有已知技术债务和产品债务。

## 状态说明

| 状态 | 含义 |
|------|------|
| OPEN | 尚未开始修复 |
| IN_PROGRESS | 正在修复中 |
| RESOLVED | 已修复并验证 |
| WONTFIX | 不再计划修复（附理由） |

## 分类说明

| 标签 | 含义 |
|------|------|
| [TECH] | 技术债务（代码质量、架构退化、测试缺失） |
| [PRODUCT] | 产品债务（降级体验、缺失功能、临时方案） |

---

## 来自 Phase 2: Runtime + Built-in Cells

来源: `specs/feat/001-phase2-runtime-cells/tech-debt.md`

### 安全/权限

| # | 标签 | 状态 | 问题 | 影响 | 建议修复时机 |
|---|------|------|------|------|-------------|
| P2-SEC-03 | [TECH] | RESOLVED | cmd/core-bundle 密钥硬编码（有 "replace-in-prod" 注释） | Phase 2 无生产部署，风险可控 | Phase 3 ✓ |
| P2-SEC-04 | [TECH] | RESOLVED | JWT 使用 HS256 对称签名，缺少 aud claim | RS256 完整迁移在 Phase 4 完成（JWTIssuer/JWTVerifier RS256-only） | Phase 4 ✓ |
| P2-SEC-06 | [TECH] | RESOLVED | RealIP 无条件信任 XFF，可绕过限流 | trustedProxies 配置已实现 | Phase 3 ✓ |
| P2-SEC-07 | [TECH] | RESOLVED | ServiceToken HMAC 无 timestamp，可重放 | timestamp 5min 窗口已实现 | Phase 3 ✓ |
| P2-SEC-08 | [TECH] | RESOLVED | Session/User ID 用 UnixNano 可预测 | pkg/uid crypto/rand UUID 已替换 7 处 | Phase 3 ✓ |
| P2-SEC-09 | [TECH] | RESOLVED | refresh token 验证未显式检查 signing method | 已添加显式 signing method 校验 | Phase 3 ✓ |
| P2-SEC-10 | [TECH] | RESOLVED | refresh token 无 rotation reuse detection | rotation + reuse detection 已实现 | Phase 3 ✓ |
| P2-SEC-11 | [TECH] | RESOLVED | API 端点无认证中间件保护 | auth middleware 已保护 /api/v1/* 端点 | Phase 3 ✓ |

### 架构

| # | 标签 | 状态 | 问题 | 影响 | 建议修复时机 |
|---|------|------|------|------|-------------|
| P2-ARCH-04 | [TECH] | RESOLVED | BaseSlice 是空壳，与实际 Service/Handler 无关联 | lifecycle + mutex 已重构 | Phase 3 ✓ |
| P2-ARCH-05 | [TECH] | RESOLVED | cells/ 直接 import chi，应仅用 RouteMux 抽象 | RouteMux 抽象已到位 | Phase 3 ✓ |
| P2-ARCH-06 | [TECH] | RESOLVED | 订阅 goroutine 用 context.Background()，shutdown 时无法取消 | goroutine context 取消已修复 | Phase 3 ✓ |
| P2-ARCH-07 | [TECH] | RESOLVED | L2 事件发布不在事务中，Phase 3 需改为 outbox.Writer | 7 处 Publish 已替换为 outbox.Writer.Write + TxManager | Phase 3 ✓ |

### 测试/回归

| # | 标签 | 状态 | 问题 | 影响 | 建议修复时机 |
|---|------|------|------|------|-------------|
| P2-T-01 | [TECH] | RESOLVED | 10/16 slices handler 层覆盖率 < 80%（Cell 级聚合达标 85-87%） | handler 层测试已补充 | Phase 3 ✓ |
| P2-T-02 | [TECH] | OPEN | 无 J-audit-login-trail 端到端集成测试 | stub 已就位，需 Docker + testcontainers 激活 | Phase 5 |
| P2-T-03 | [TECH] | RESOLVED | bootstrap.go 覆盖率 51.4%（sandbox 限制 net.Listen） | 覆盖率已提升 | Phase 3 ✓ |
| P2-T-05 | [TECH] | RESOLVED | in-memory repo 掩盖集成问题 | adapter PG Repository 已实现（audit + config） | Phase 3 ✓ |
| P2-T-06 | [TECH] | RESOLVED | go vet copylocks warning (time.Time in User struct) | copylocks 警告已修复 | Phase 3 ✓ |
| P2-T-07 | [TECH] | RESOLVED | cmd/core-bundle 无冒烟测试 | 冒烟测试已补充 | Phase 3 ✓ |
| P2-router | [TECH] | RESOLVED | runtime/http/router 覆盖率 78.8%（接近 80% 阈值） | 覆盖率已提升 | Phase 3 ✓ |

### 运维/部署

| # | 标签 | 状态 | 问题 | 影响 | 建议修复时机 |
|---|------|------|------|------|-------------|
| P2-D-06 | [TECH] | RESOLVED | Assembly.Stop 可在 Starting 状态被调用 | LIFO + mutex 已修复 | Phase 3 ✓ |
| P2-D-07 | [TECH] | RESOLVED | config watcher 未集成到 bootstrap 生命周期 | config watcher 已集成 | Phase 3 ✓ |
| P2-D-09 | [TECH] | RESOLVED | eventbus 无健康状态暴露到 /healthz | 健康检查已集成 | Phase 3 ✓ |

### 开发者体验 (DX)

| # | 标签 | 状态 | 问题 | 影响 | 建议修复时机 |
|---|------|------|------|------|-------------|
| P2-DX-02 | [TECH] | RESOLVED | 11 个 runtime 包缺少 doc.go | 24 个 doc.go 已补全（runtime 9 + kernel 10 + pkg 4 + adapters 6 = 29 total） | Phase 3 ✓ |
| P2-DX-03 | [TECH] | RESOLVED | TopicConfigChanged 常量定义 3 次 | 抽取到共享 events 包 | Phase 3 ✓ |

### 产品/UX

| # | 标签 | 状态 | 问题 | 影响 | 建议修复时机 |
|---|------|------|------|------|-------------|
| P2-PM-03 | [PRODUCT] | RESOLVED | RateLimit Retry-After 硬编码 1 秒 | RateLimiter 接口已扩展 | Phase 3 ✓ |
| P2-PM-audit | [PRODUCT] | RESOLVED | 审计查询 time.Parse 错误静默忽略 | 已返回 400 | Phase 3 ✓ |
| P2-PM-user | [PRODUCT] | RESOLVED | Update user 仅支持 email 字段 | PATCH user 扩展可更新字段 | Phase 3 ✓ |

---

## 来自 Phase 3: Adapters

来源: `specs/feat/002-phase3-adapters/tech-debt.md`

### 测试/回归

| # | 标签 | 状态 | 问题 | 延迟理由 | 建议修复时机 |
|---|------|------|------|---------|-------------|
| P3-TD-01 | [TECH] | RESOLVED | integration_test.go 全为 t.Skip stub；testcontainers-go 未引入 go.mod | Phase 4 实现 testcontainers 集成测试（postgres/redis/rabbitmq）；go.mod 添加 v0.41.0 | Phase 4 ✓ |
| P3-TD-02 | [TECH] | PARTIAL | postgres adapter 覆盖率 46.6%（要求 ≥80%）；Pool/TxManager/Migrator 真实路径未覆盖 | testcontainers 测试已实现；精确覆盖率未在流水线中测量（需 Docker + -tags=integration） | Phase 5（测量验证） |
| P3-TD-04 | [TECH] | OPEN | websocket/oidc/s3 单元测试在 sandbox 环境 httptest 端口绑定 panic | 测试在非 sandbox 环境正常通过，sandbox 限制 net.Listen；Phase 4 添加跳过 guard | Phase 4 CI ✓（guard 已加）|
| P3-TD-07 | [TECH] | RESOLVED | testcontainers-go 未在 go.mod | go.mod 已添加 v0.41.0（注意：标记为 indirect，Phase 5 修正） | Phase 4 ✓ |

### 运维/部署

| # | 标签 | 状态 | 问题 | 延迟理由 | 建议修复时机 |
|---|------|------|------|---------|-------------|
| P3-TD-03 | [TECH] | RESOLVED | 无 .github/workflows CI 配置 | Phase 4 创建 ci.yml（build/test/vet/validate/integration/coverage） | Phase 4 ✓ |
| P3-TD-05 | [TECH] | PARTIAL | docker-compose.yml 缺 start_period；rabbitmq 冷启动占 retries 配额 | root compose 已补全；3 个示例 compose 文件仍缺失（P4-TD-07） | v1.1 |

### 架构一致性

| # | 标签 | 状态 | 问题 | 延迟理由 | 建议修复时机 |
|---|------|------|------|---------|-------------|
| P3-TD-06 | [TECH] | RESOLVED | outboxWriter nil guard 静默 fallback 到 publisher.Publish | Phase 4 在 Cell.Init 实现 fail-fast（ERR_CELL_MISSING_OUTBOX） | Phase 4 ✓ |

### DX/可维护性

| # | 标签 | 状态 | 问题 | 延迟理由 | 建议修复时机 |
|---|------|------|------|---------|-------------|
| P3-TD-08 | [TECH] | RESOLVED | WithEventBus 保留具体类型参数未标注 Deprecated | Phase 4 添加 // Deprecated 注释 | Phase 4 ✓ |

### 安全/权限

| # | 标签 | 状态 | 问题 | 延迟理由 | 建议修复时机 |
|---|------|------|------|---------|-------------|
| P3-TD-09 | [TECH] | RESOLVED | RS256 迁移为 Option 注入，默认仍 HS256 | Phase 4 完成完整切换（JWTIssuer/JWTVerifier RS256-only；旧路径标记 Deprecated） | Phase 4 ✓ |

### 产品/UX（Phase 2 DEFERRED 继承）

| # | 标签 | 状态 | 问题 | 延迟理由 | 建议修复时机 |
|---|------|------|------|---------|-------------|
| P3-TD-10 | [PRODUCT] | OPEN | Phase 2 遗留 #54 TOCTOU 竞态未修复 | 需 Redis 分布式锁 + 持久化 session 稳定；Phase 4 DEFERRED | post-v1.0 |
| P3-TD-11 | [PRODUCT] | OPEN | Phase 2 遗留 #56-59 domain 模型重构未执行 | 高风险重构，需 adapter 稳定后；Phase 4 DEFERRED | post-v1.0 |
| P3-TD-12 | [PRODUCT] | OPEN | Phase 2 遗留 #62 configpublish.Rollback 版本校验 | 需持久化版本管理；Phase 4 DEFERRED | post-v1.0 |

---

## 来自 Phase 4: Examples + Documentation

来源: `specs/feat/003-phase4-examples-docs/tech-debt.md`

### 架构一致性

| # | 标签 | 状态 | 问题 | 影响 | 建议修复时机 |
|---|------|------|------|------|-------------|
| P4-TD-04 | [TECH] | OPEN | order-cell 声明 L2 但使用 publisher.Publish 而非事务性 outbox write；Init 不强制 outboxWriter 注入 | todo-order 示例 L2 一致性为"声明型"而非"事务型"；semantic gap | v1.1 |

### 测试/回归

| # | 标签 | 状态 | 问题 | 影响 | 建议修复时机 |
|---|------|------|------|------|-------------|
| P4-TD-05 | [TECH] | RESOLVED | 无 outbox 全链路集成测试（FR-6.5） | Phase 4 实现 TestIntegration_OutboxFullChain（postgres→relay→rabbitmq→idempotency）| Phase 4 ✓ |

### CI/运维

| # | 标签 | 状态 | 问题 | 影响 | 建议修复时机 |
|---|------|------|------|------|-------------|
| P4-TD-06 | [TECH] | OPEN | CI 的 example validation 步骤使用 `\|\| true`，验证错误被静默吞咽 | example 元数据 CI gate 形式化，无实际阻断效果 | v1.1 |
| P4-TD-07 | [TECH] | OPEN | 3 个示例 docker-compose.yml 缺少 start_period（rabbitmq healthcheck）；使用已废弃的 version: "3.9" 键 | 冷启动时 rabbitmq healthcheck 可能超时；docker compose v2 警告 | v1.1 |
| P4-TD-10 | [TECH] | OPEN | `runtime/observability/metrics.Middleware` 直接将 `r.URL.Path` 作为 `path` label 传给 Collector，Prometheus adapter 会把参数化路由展开成高基数时间序列 | `/users/123`、`/orders/42` 等不同资源 ID 会持续扩张 metrics cardinality，增加 scrape / storage 压力，极端情况下可能导致 Prometheus 内存问题 | v1.1 |

### DX/可维护性

| # | 标签 | 状态 | 问题 | 影响 | 建议修复时机 |
|---|------|------|------|------|-------------|
| P4-TD-01 | [TECH] | OPEN | 各处定义 ad-hoc noop outbox.Writer / idempotency.Checker；KG-02 建议提取到共享包 | 代码重复；测试辅助工具散落各处 | v1.1 |
| P4-TD-02 | [TECH] | OPEN | Cell handler 代码直接 import chi.URLParam；应通过 pkg/httputil 抽象层 | cell 代码与 chi router 实现耦合，违背 RouteMux 抽象意图 | v1.1 |
| P4-TD-03 | [TECH] | OPEN | IssueTestToken 仍保留 HS256 dead code（[]byte 参数路径）；JWTVerifier 会拒绝所有 HS256 token | 测试陷阱：测试编写者可能误用 HS256 路径，产生永远失败的 token | v1.1 |

### 测量缺口（非代码问题）

| # | 标签 | 状态 | 问题 | 影响 | 建议修复时机 |
|---|------|------|------|------|-------------|
| P4-TD-08 | [TECH] | OPEN | postgres adapter 集成覆盖率（-tags=integration）未在流水线中测量；基准为 46.6%（Phase 3） | 无法确认 testcontainers 测试后覆盖率是否达到 ≥80% 要求 | Phase 5（CI 集成覆盖率测量）|
| P4-TD-09 | [TECH] | OPEN | testcontainers-go 在 go.mod 中标记为 `// indirect`，实际为直接依赖 | go.mod 声明不准确，go mod tidy 可能移除 | v1.1 |

---

## 统计

| Phase | [TECH] | [PRODUCT] | 合计 | OPEN | RESOLVED | PARTIAL |
|-------|--------|-----------|------|------|----------|---------|
| Phase 2 | 23 | 3 | 26 | 1 | 24 | 1 |
| Phase 3 新增 | 9 | 3 | 12 | 5 | 5 | 2 |
| Phase 4 新增 | 9 | 0 | 9 | 8 | 1 | 0 |
| **总计** | **41** | **6** | **47** | **14** | **30** | **3** |

**活跃债务（OPEN + PARTIAL）**: 17 条（v1.1 处理目标）

**Phase 4 关闭**: P3-TD-01、P3-TD-03、P3-TD-06、P3-TD-07、P3-TD-08、P3-TD-09、P4-TD-05（共 7 条）
**Phase 4 新增 OPEN**: P4-TD-01 through P4-TD-04、P4-TD-06 through P4-TD-10（共 9 条）

**注**: 全局 registry 仅追踪跨 Phase 持续影响的条目（架构/安全/测试层面）。编码规范/DX 细节项在 Phase 执行中修复时不重复录入。Phase 3 PARTIAL 项（P3-TD-02、P3-TD-05）在 Phase 4 仍为 PARTIAL，目标 v1.1 完全关闭。
