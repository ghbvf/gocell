# Tech Debt — Phase 2

## 分类说明
- [TECH]: 技术债务（代码质量、架构退化、测试缺失）
- [PRODUCT]: 产品债务（降级体验、缺失功能、临时方案）

## 延迟项

| # | 标签 | 来源席位 | 问题 | 延迟理由 | 建议修复时机 |
|---|------|---------|------|---------|-------------|
| 1 | [TECH] | 架构一致性 | ARCH-04: BaseSlice 是空壳，与实际 Service/Handler 无关联 | kernel Slice 接口重构影响面大 | Phase 3 |
| 2 | [TECH] | 架构一致性 | ARCH-05: cells/ 直接 import chi，应仅用 RouteMux 抽象 | 需扩展 RouteMux 接口支持子路由 | Phase 3 |
| 3 | [TECH] | 架构一致性 | ARCH-06/D-03: 订阅 goroutine 用 context.Background()，shutdown 时无法取消 | 功能正确（eventbus.Close 间接终止），优雅关闭优化 | Phase 3 |
| 4 | [TECH] | 架构一致性 | ARCH-07: L2 事件发布不在事务中，Phase 3 需改为 outbox.Writer | Phase 2 无 DB，事务语义无意义 | Phase 3 |
| 5 | [TECH] | 安全/权限 | SEC-03: cmd/core-bundle 密钥硬编码（有 "replace-in-prod" 注释） | Phase 2 无生产部署 | Phase 3 |
| 6 | [TECH] | 安全/权限 | SEC-04: JWT 使用 HS256 对称签名，缺少 aud claim | Phase 2 单进程部署可接受 | Phase 3 迁移 RS256 |
| 7 | [TECH] | 安全/权限 | SEC-06: RealIP 无条件信任 XFF，可绕过限流 | 需 trustedProxies 配置机制 | Phase 3 |
| 8 | [TECH] | 安全/权限 | SEC-07: ServiceToken HMAC 无 timestamp，可重放 | 内部 API 风险可控 | Phase 3 |
| 9 | [TECH] | 安全/权限 | SEC-08: Session/User ID 用 UnixNano 可预测 | Phase 2 in-memory 存储风险低 | Phase 3 改 UUID |
| 10 | [TECH] | 安全/权限 | SEC-09: refresh token 验证未显式检查 signing method | jwt/v5 内置防护，defense-in-depth | Phase 3 |
| 11 | [TECH] | 安全/权限 | SEC-10: refresh token 无 rotation reuse detection | ARCH-08 已修复 persist，完善 rotation 需更多逻辑 | Phase 3 |
| 12 | [TECH] | 安全/权限 | SEC-11: API 端点无认证中间件保护 | Phase 2 无外部暴露 | Phase 3 |
| 13 | [TECH] | 测试/回归 | T-01: 10/16 slices handler 层覆盖率 < 80%（Cell 级聚合达标 85-87%） | handler 层 httptest 测试工作量大 | Phase 3 补充 |
| 14 | [TECH] | 测试/回归 | T-02: 无 J-audit-login-trail 端到端集成测试 | Soft Gate 允许 stub 辅助 | Phase 3 adapter 就绪后 |
| 15 | [TECH] | 测试/回归 | T-03: bootstrap.go 覆盖率 51.4%（sandbox 限制 net.Listen） | CI 环境可补充 | Phase 3 |
| 16 | [TECH] | 测试/回归 | T-05: in-memory repo 掩盖集成问题 | Phase 3 adapter 替换后自然解决 | Phase 3 |
| 17 | [TECH] | 测试/回归 | T-06: go vet copylocks warning (time.Time in User struct) | Go 版本特定行为 | Phase 3 |
| 18 | [TECH] | 测试/回归 | T-07: cmd/core-bundle 无冒烟测试 | 功能已在 cell_test.go 覆盖 | Phase 3 |
| 19 | [TECH] | 运维/部署 | D-06: Assembly.Stop 可在 Starting 状态被调用 | 竞态窗口极小 | Phase 3 |
| 20 | [TECH] | 运维/部署 | D-07: config watcher 未集成到 bootstrap 生命周期 | J-config-hot-reload 需要 | Phase 3 |
| 21 | [TECH] | 运维/部署 | D-09: eventbus 无健康状态暴露到 /healthz | 可观测性增强 | Phase 3 |
| 22 | [TECH] | DX | DX-02: 11 个 runtime 包缺少 doc.go | 文档增量补充 | Phase 3-4 |
| 23 | [TECH] | DX | DX-03: TopicConfigChanged 常量定义 3 次 | 抽取到共享 events 包 | Phase 3 |
| 24 | [PRODUCT] | 产品/UX | PM-03: RateLimit Retry-After 硬编码 1 秒 | 需扩展 RateLimiter 接口 | Phase 3 |
| 25 | [PRODUCT] | 产品/UX | 审计查询 time.Parse 错误静默忽略 | 应返回 400 | Phase 3 |
| 26 | [PRODUCT] | 产品/UX | Update user 仅支持 email 字段 | 扩展可更新字段 | Phase 3 |

## 统计
- [TECH] 新增: 23 条
- [PRODUCT] 新增: 3 条
- 上一 Phase 遗留已解决: 0 条（首次启用工作流）
