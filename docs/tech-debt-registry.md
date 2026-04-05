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
| P2-SEC-03 | [TECH] | OPEN | cmd/core-bundle 密钥硬编码（有 "replace-in-prod" 注释） | Phase 2 无生产部署，风险可控 | Phase 3 |
| P2-SEC-04 | [TECH] | OPEN | JWT 使用 HS256 对称签名，缺少 aud claim | 单进程部署可接受 | Phase 3 迁移 RS256 |
| P2-SEC-06 | [TECH] | OPEN | RealIP 无条件信任 XFF，可绕过限流 | 需 trustedProxies 配置机制 | Phase 3 |
| P2-SEC-07 | [TECH] | OPEN | ServiceToken HMAC 无 timestamp，可重放 | 内部 API 风险可控 | Phase 3 |
| P2-SEC-08 | [TECH] | OPEN | Session/User ID 用 UnixNano 可预测 | Phase 2 in-memory 存储风险低 | Phase 3 改 UUID |
| P2-SEC-09 | [TECH] | OPEN | refresh token 验证未显式检查 signing method | jwt/v5 内置防护，defense-in-depth | Phase 3 |
| P2-SEC-10 | [TECH] | OPEN | refresh token 无 rotation reuse detection | ARCH-08 已修复 persist，完善 rotation 需更多逻辑 | Phase 3 |
| P2-SEC-11 | [TECH] | OPEN | API 端点无认证中间件保护 | Phase 2 无外部暴露 | Phase 3 |

### 架构

| # | 标签 | 状态 | 问题 | 影响 | 建议修复时机 |
|---|------|------|------|------|-------------|
| P2-ARCH-04 | [TECH] | OPEN | BaseSlice 是空壳，与实际 Service/Handler 无关联 | kernel Slice 接口重构影响面大 | Phase 3 |
| P2-ARCH-05 | [TECH] | OPEN | cells/ 直接 import chi，应仅用 RouteMux 抽象 | 需扩展 RouteMux 接口支持子路由 | Phase 3 |
| P2-ARCH-06 | [TECH] | OPEN | 订阅 goroutine 用 context.Background()，shutdown 时无法取消 | 功能正确（eventbus.Close 间接终止），优雅关闭优化 | Phase 3 |
| P2-ARCH-07 | [TECH] | OPEN | L2 事件发布不在事务中，Phase 3 需改为 outbox.Writer | Phase 2 无 DB，事务语义无意义 | Phase 3 |

### 测试/回归

| # | 标签 | 状态 | 问题 | 影响 | 建议修复时机 |
|---|------|------|------|------|-------------|
| P2-T-01 | [TECH] | OPEN | 10/16 slices handler 层覆盖率 < 80%（Cell 级聚合达标 85-87%） | handler 层 httptest 测试工作量大 | Phase 3 补充 |
| P2-T-02 | [TECH] | OPEN | 无 J-audit-login-trail 端到端集成测试 | Soft Gate 允许 stub 辅助 | Phase 3 adapter 就绪后 |
| P2-T-03 | [TECH] | OPEN | bootstrap.go 覆盖率 51.4%（sandbox 限制 net.Listen） | CI 环境可补充 | Phase 3 |
| P2-T-05 | [TECH] | OPEN | in-memory repo 掩盖集成问题 | Phase 3 adapter 替换后自然解决 | Phase 3 |
| P2-T-06 | [TECH] | OPEN | go vet copylocks warning (time.Time in User struct) | Go 版本特定行为 | Phase 3 |
| P2-T-07 | [TECH] | OPEN | cmd/core-bundle 无冒烟测试 | 功能已在 cell_test.go 覆盖 | Phase 3 |
| P2-router | [TECH] | OPEN | runtime/http/router 覆盖率 78.8%（接近 80% 阈值） | Route/Mount/Group 委托方法未独立测试 | Phase 3 |

### 运维/部署

| # | 标签 | 状态 | 问题 | 影响 | 建议修复时机 |
|---|------|------|------|------|-------------|
| P2-D-06 | [TECH] | OPEN | Assembly.Stop 可在 Starting 状态被调用 | 竞态窗口极小 | Phase 3 |
| P2-D-07 | [TECH] | OPEN | config watcher 未集成到 bootstrap 生命周期 | J-config-hot-reload 需要 | Phase 3 |
| P2-D-09 | [TECH] | OPEN | eventbus 无健康状态暴露到 /healthz | 可观测性增强 | Phase 3 |

### 开发者体验 (DX)

| # | 标签 | 状态 | 问题 | 影响 | 建议修复时机 |
|---|------|------|------|------|-------------|
| P2-DX-02 | [TECH] | OPEN | 11 个 runtime 包缺少 doc.go | 文档增量补充 | Phase 3-4 |
| P2-DX-03 | [TECH] | OPEN | TopicConfigChanged 常量定义 3 次 | 抽取到共享 events 包 | Phase 3 |

### 产品/UX

| # | 标签 | 状态 | 问题 | 影响 | 建议修复时机 |
|---|------|------|------|------|-------------|
| P2-PM-03 | [PRODUCT] | OPEN | RateLimit Retry-After 硬编码 1 秒 | 需扩展 RateLimiter 接口 | Phase 3 |
| P2-PM-audit | [PRODUCT] | OPEN | 审计查询 time.Parse 错误静默忽略 | 应返回 400 | Phase 3 |
| P2-PM-user | [PRODUCT] | OPEN | Update user 仅支持 email 字段 | 扩展可更新字段 | Phase 3 |

---

## 统计

| Phase | [TECH] | [PRODUCT] | 合计 | OPEN | RESOLVED |
|-------|--------|-----------|------|------|----------|
| Phase 2 | 23 | 3 | 26 | 26 | 0 |
| **总计** | **23** | **3** | **26** | **26** | **0** |
