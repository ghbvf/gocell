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
| P2-SEC-04 | [TECH] | PARTIAL | JWT 使用 HS256 对称签名，缺少 aud claim | RS256 迁移为 Option 注入，默认仍 HS256；Phase 4 强制迁移 | Phase 4 |
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
| P2-T-02 | [TECH] | OPEN | 无 J-audit-login-trail 端到端集成测试 | stub 已就位，需 Docker + testcontainers 激活 | Phase 4 |
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
| P3-TD-01 | [TECH] | OPEN | integration_test.go 全为 t.Skip stub；testcontainers-go 未引入 go.mod | 需 Docker 环境；stub 结构正确，待 Docker CI 就绪后填充 | Phase 4 |
| P3-TD-02 | [TECH] | OPEN | postgres adapter 覆盖率 46.6%（要求 ≥80%）；Pool/TxManager/Migrator 真实路径未覆盖 | 需要真实 PostgreSQL 的集成测试才能覆盖连接/事务/迁移路径 | Phase 4 |
| P3-TD-04 | [TECH] | OPEN | websocket/oidc/s3 单元测试在 sandbox 环境 httptest 端口绑定 panic | 测试在非 sandbox 环境正常通过，sandbox 限制 net.Listen | Phase 4 CI |
| P3-TD-07 | [TECH] | OPEN | testcontainers-go 未在 go.mod（与 #1 联动） | 同 P3-TD-01 | Phase 4 |

### 运维/部署

| # | 标签 | 状态 | 问题 | 延迟理由 | 建议修复时机 |
|---|------|------|------|---------|-------------|
| P3-TD-03 | [TECH] | OPEN | 无 .github/workflows CI 配置 | Phase 3 聚焦 adapter 实现，CI 配置待 Phase 4 examples 一并设置 | Phase 4 |
| P3-TD-05 | [TECH] | OPEN | docker-compose.yml 缺 start_period；rabbitmq 冷启动占 retries 配额 | 非阻塞，docker compose up --wait 仍可 30s 内完成 | Phase 4 |

### 架构一致性

| # | 标签 | 状态 | 问题 | 延迟理由 | 建议修复时机 |
|---|------|------|------|---------|-------------|
| P3-TD-06 | [TECH] | OPEN | outboxWriter nil guard 静默 fallback 到 publisher.Publish | 向后兼容设计，生产应注入 outboxWriter；建议添加 slog.Warn | Phase 4 |

### DX/可维护性

| # | 标签 | 状态 | 问题 | 延迟理由 | 建议修复时机 |
|---|------|------|------|---------|-------------|
| P3-TD-08 | [TECH] | OPEN | WithEventBus 保留具体类型参数未标注 Deprecated | 向后兼容，Phase 4 可添加 // Deprecated 注释 | Phase 4 |

### 安全/权限

| # | 标签 | 状态 | 问题 | 延迟理由 | 建议修复时机 |
|---|------|------|------|---------|-------------|
| P3-TD-09 | [TECH] | OPEN | RS256 迁移为 Option 注入，默认仍 HS256 | 完整迁移需要 Cell 构造时强制注入 RSA key pair，当前为渐进式 | Phase 4 |

### 产品/UX（Phase 2 DEFERRED 继承）

| # | 标签 | 状态 | 问题 | 延迟理由 | 建议修复时机 |
|---|------|------|------|---------|-------------|
| P3-TD-10 | [PRODUCT] | OPEN | Phase 2 遗留 #54 TOCTOU 竞态未修复 | 需 Redis 分布式锁 + 持久化 session 稳定 | Phase 4 |
| P3-TD-11 | [PRODUCT] | OPEN | Phase 2 遗留 #56-59 domain 模型重构未执行 | 高风险重构，需 adapter 稳定后 | Phase 4 |
| P3-TD-12 | [PRODUCT] | OPEN | Phase 2 遗留 #62 configpublish.Rollback 版本校验 | 需持久化版本管理 | Phase 4 |

---

## 统计

| Phase | [TECH] | [PRODUCT] | 合计 | OPEN | RESOLVED | PARTIAL |
|-------|--------|-----------|------|------|----------|---------|
| Phase 2 | 23 | 3 | 26 | 2 | 23 | 1 |
| Phase 3 新增 | 9 | 3 | 12 | 12 | 0 | 0 |
| **总计** | **32** | **6** | **38** | **14** | **23** | **1** |

**活跃债务（OPEN + PARTIAL）**: 15 条（Phase 4 处理目标）

**注**: Phase 2 原始 80 条 tech-debt 中，26 条进入全局 registry（代表架构/安全/测试层面），其余约 54 条为编码规范/DX 细节项，已在 Phase 3 执行中修复（不重复录入）。全局 registry 仅追踪跨 Phase 持续影响的条目。
