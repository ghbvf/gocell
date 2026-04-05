# Review Findings — Phase 2

## 审查基准版本
Commit: ab166c90dfccc78566fb0f0296a9ce2770540ecc
Branch: feat/001-phase2-runtime-cells
变更范围: ~120 files changed

## P0（阻塞）

| # | 席位 | 文件 | 问题 | 建议修复 |
|---|------|------|------|---------|
| SEC-01 | 安全/权限 | sessionlogin/service.go, identitymanage/service.go | 密码使用 subtle.ConstantTimeCompare 直接比较，无 bcrypt/argon2。数据库泄露后凭据可直接重放 | 使用 golang.org/x/crypto/bcrypt 进行 hash+compare |
| SEC-02 | 安全/权限 | identitymanage/handler.go | domain.User 直接序列化返回，PasswordHash 字段泄露给客户端 | 创建 UserResponse DTO 排除 PasswordHash |

## P1（重要）

| # | 席位 | 文件 | 问题 | 建议修复 |
|---|------|------|------|---------|
| ARCH-01 | 架构 | 多个 handler.go | 500 响应使用 err.Error() 泄露内部细节，违反 error-handling.md | 500 固定返回 "internal server error"，原始错误写 slog |
| ARCH-04 | 架构 | 3 个 cell.go | BaseSlice 是空壳，与实际 Service/Handler 无关联 | Phase 2 接受，Phase 3 重构 |
| ARCH-08 | 架构 | sessionrefresh/service.go | refresh 后未 persist session，旧 refresh token 仍有效 | 添加 sessionRepo.Update |
| SEC-03 | 安全 | cmd/core-bundle/main.go | JWT/HMAC 密钥硬编码为字面量 | 改为环境变量读取 |
| SEC-04 | 安全 | sessionlogin/service.go | JWT 使用 HS256 对称签名，缺少 aud claim | Phase 2 添加 aud，RS256 延迟 Phase 3 |
| SEC-06 | 安全 | real_ip.go | 无条件信任 X-Forwarded-For，可绕过限流 | 添加文档说明，Phase 3 加 trustedProxies |
| SEC-10 | 安全 | sessionrefresh/service.go | refresh token 无 rotation reuse detection | Phase 2 修复 persist，Phase 3 完善 rotation |
| T-01 | 测试 | 10/16 slices | Handler 层覆盖率 < 80%（service 层已覆盖） | Cell 级聚合达标(85-87%)，handler 测试记 tech-debt |
| T-03 | 测试 | bootstrap.go | 覆盖率 51.4%（sandbox 限制 net.Listen） | 记 tech-debt，CI 环境补充 |
| D-03 | 运维 | audit-core/cell.go, config-core/cell.go | 订阅 goroutine 用 context.Background()，shutdown 时无法取消 | 传入可取消 context |
| D-07 | 运维 | bootstrap.go | config watcher 未集成到 bootstrap 生命周期 | 添加 watcher 启动/停止 |
| DX-01 | DX | 10+ handler.go | writeJSON/writeError 重复定义 12 处 | 抽取到共享包 |
| DX-02 | DX | runtime/ | 11 个包缺少 doc.go | 记 tech-debt |
| PM-01 | 产品 | 所有 handler | 错误响应缺少 "details" 字段 | 统一加 details: {} |
| PM-02 | 产品 | identitymanage handler | 所有 service 错误 → 404，应区分 404 vs 500 | 用 errors.As 检查 errcode |

## P2（建议）

| # | 席位 | 文件 | 问题 |
|---|------|------|------|
| ARCH-03 | 架构 | handler.go | writeJSON/writeError 重复 (同 DX-01) |
| ARCH-05 | 架构 | cell.go | cells/ 直接 import chi，应仅用 RouteMux |
| ARCH-06 | 架构 | cell.go | 订阅 goroutine 无生命周期管理 (同 D-03) |
| ARCH-07 | 架构 | sessionlogin/service.go | L2 事件发布不在事务中，需 Phase 3 outbox |
| SEC-07 | 安全 | servicetoken.go | HMAC 无 timestamp，可重放 |
| SEC-08 | 安全 | sessionlogin/service.go | Session ID 用 UnixNano 可预测 |
| SEC-09 | 安全 | sessionrefresh/service.go | refresh token 验证未检查 signing method |
| SEC-11 | 安全 | cell.go RegisterRoutes | API 端点无认证中间件 |
| T-05 | 测试 | mem/*.go | in-memory repo 掩盖集成问题 |
| T-06 | 测试 | user_repo.go | go vet copylocks warning |
| T-07 | 测试 | cmd/core-bundle | 无 TestMain 冒烟测试 |
| D-04 | 运维 | Makefile | 假设 cwd=src/ |
| D-06 | 运维 | assembly.go | Stop 可在 Starting 状态被调用 |
| D-08 | 运维 | Makefile | 缺 vet/lint 目标 |
| D-09 | 运维 | health.go | eventbus 无健康状态暴露 |
| DX-03 | DX | configwrite/configpublish/configsubscribe | TopicConfigChanged 定义 3 次 |
| PM-03 | 产品 | rate_limit.go | Retry-After 硬编码 1 |
