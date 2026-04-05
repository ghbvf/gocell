# Phase Report -- Phase 2: Runtime + Built-in Cells

## 日期

2026-04-05

## 1. Phase 目标 vs 实际交付

### 目标

在 Phase 0+1 的 kernel 底座（59 Go 文件，元数据治理框架）上构建 runtime 运行时层和 3 个内建 Cell，使 GoCell 从"可编译的元数据治理框架"进化为"可运行的 Cell-native Go 框架"。

### 交付物清单

| 模块 | 计划 | 实际 | 状态 |
|------|------|------|------|
| runtime/http/middleware | 7 个 chi 中间件 | 7 个（request_id, real_ip, recovery, access_log, security_headers, body_limit, rate_limit） | 完成 |
| runtime/http/health | /healthz + /readyz | HealthHandler 聚合 Assembly.Health() | 完成 |
| runtime/http/router | chi-based 路由构建器 | router.go + RouteMux 抽象 | 完成 |
| runtime/config | YAML/env 配置加载 + watcher | config.go + watcher.go | 完成 |
| runtime/bootstrap | 统一启动器 | bootstrap.go（parse config -> init assembly -> start HTTP -> start workers） | 完成 |
| runtime/shutdown | graceful shutdown | shutdown.go（signal -> timeout -> 有序 teardown） | 完成 |
| runtime/observability | Prometheus + OTel + slog | metrics/ + tracing/ + logging/ | 完成 |
| runtime/worker | 后台 worker 生命周期 | worker.go + periodic.go | 完成 |
| runtime/auth | JWT 验证 + RBAC + 服务间认证 | interfaces + middleware + servicetoken（Phase 2 用 HS256，RS256 延至 Phase 3） | 完成（降级） |
| runtime/eventbus | in-memory Pub/Sub | eventbus.go（at-most-once + 3x 重试 + dead letter） | 完成 |
| cells/access-core | 5 slices | identity-manage / session-login / session-refresh / session-logout / authorization-decide | 完成 |
| cells/audit-core | 3 slices | audit-write / audit-verify / audit-archive（HMAC-SHA256 hash chain） | 完成 |
| cells/config-core | 4 slices | config-manage / config-publish / config-subscribe / feature-flag | 完成 |
| kernel/ 接口扩展 | Subscriber + HTTPRegistrar + EventRegistrar | kernel/outbox + kernel/cell 新增接口 | 完成 |
| cmd/core-bundle | 3 Cell 编排启动 | 硬编码注册顺序 config-core -> access-core -> audit-core | 完成 |
| YAML 元数据修正 | Wave 0 对齐 | slice.yaml / contract.yaml 全部修正，gocell validate 零 error | 完成 |

### 降级/延迟项

| 项 | 原因 |
|----|------|
| JWT RS256 非对称签名 | Phase 2 单进程部署，HS256 可接受，RS256 延至 Phase 3 |
| OIDC 登录流程 | 需 Phase 3 OIDC 适配器 |
| 分布式限流（Redis-backed） | 需 Phase 3 Redis adapter |
| Assembly 自动拓扑排序 | Phase 2 仅 3 Cell，硬编码成本低 |

### 总结

Phase 2 核心目标**已达成**。GoCell 已从元数据治理框架升级为可运行框架。所有计划模块已交付，部分安全特性（RS256、OIDC）按决策合理降级。

---

## 2. 关键数字

| 指标 | 数值 |
|------|------|
| 变更文件数 | **173 files** |
| 新增代码行数 | **15,117 lines** |
| 测试包数 | **48 packages** |
| 测试失败数 | **0 failures** |
| gocell validate | **0 error, 0 warning** |
| 外部依赖 | **6 个白名单**（chi, x/crypto, fsnotify, prometheus, otel, golang-jwt） |
| kernel/ 覆盖率 | **>= 90%**（全部达标） |
| runtime/ 覆盖率 | **10/12 达标 >= 80%**（bootstrap 51.4% sandbox 限制，router 78.8% 接近阈值） |
| cells/ Cell 级覆盖率 | **85-87%**（全部达标 >= 80%） |
| Hard Gate Journey | 5/5 PASS |
| Soft Gate Journey | 3/3 PASS（in-memory EventBus 验证） |
| 工作流阶段门 | S1-S8 全部 PASS |

---

## 3. 架构决策摘要（14 项）

以下决策记录于 `decisions.md`，经 4 方审查（架构师、Roadmap 规划师、Kernel Guardian、产品经理）后裁决。

| # | 决策 | 核心理由 |
|---|------|---------|
| D1 | kernel/outbox 新增 Subscriber 接口，runtime/eventbus 同时实现 Publisher + Subscriber | 4 方一致认为缺 Subscriber 是最高风险卡点 |
| D2 | in-memory EventBus 标记为 at-most-once + 3x 重试 + dead letter | 内存无法真正 at-least-once，诚实标注避免 Phase 3 迁移意外 |
| D3 | Slice 采用构造时注入模式，Init 仅做状态检查 | 与 Uber fx 风格一致，编译时类型安全 |
| D4 | runtime/auth 仅提供抽象中间件框架（TokenVerifier + Authorizer 接口） | runtime/ 不耦合具体认证策略 |
| D5 | runtime/config = 框架配置，config-core = 业务配置，二者不交叉 import | 分层职责清晰 |
| D6 | Phase 2 硬编码 Cell 注册顺序（config -> access -> audit） | 仅 3 Cell，硬编码成本低且可靠 |
| D7 | Feature Flag 仅支持布尔开关 + 百分比 rollout | 最小可用集，避免范围蔓延 |
| D8 | Phase 2 仅密码登录 + JWT 签发，OIDC 延至 Phase 3 | OIDC 适配器是 Phase 3 非目标 |
| D9 | Journey 分为 Hard Gate (5) + Soft Gate (3) | 跨 Cell Journey 依赖 EventBus 完备度，分级确保核心路径不阻塞 |
| D10 | Wave 0 修正全部 YAML 元数据遗漏 | 元数据不一致会阻碍每个 batch 的 gate 检查 |
| D11 | runtime/ 可依赖 kernel/ 和 pkg/（明确合规） | runtime/http/health 需要 kernel/cell.HealthStatus 等 |
| D12 | kernel/cell 新增 HTTPRegistrar + EventRegistrar 可选接口 | Cell 接口从治理扩展到运行时，可选接口保持向后兼容 |
| D13 | Session 4 Slice 保留，共享 Cell 级 domain/ports | 合并需删除 YAML，保留成本更低 |
| D14 | 外部依赖白名单统一为 6 个 | 4 方一致指出矛盾，Prometheus/OTel/JWT 无纯标准库替代 |

---

## 4. S6 修复的安全问题

Stage 6（Review-Fix 循环）修复了以下 P0/P1 安全问题：

### P0 -- 阻塞级

| 编号 | 问题 | 修复 |
|------|------|------|
| SEC-01 | 密码使用 `subtle.ConstantTimeCompare` 直接比较，无 bcrypt/argon2。数据库泄露后凭据可直接重放 | 迁移至 `golang.org/x/crypto/bcrypt` 进行 hash + compare |
| SEC-02 | `domain.User` 直接序列化返回，`PasswordHash` 字段泄露给客户端 | 创建 `UserResponse` DTO 排除 `PasswordHash` |

### P1 -- 重要级

| 编号 | 问题 | 修复 |
|------|------|------|
| ARCH-01 | 500 响应使用 `err.Error()` 泄露内部细节 | 500 固定返回 "internal server error"，原始错误写 slog |
| DX-01 | writeJSON/writeError 重复定义 12 处 | 抽取到 `runtime/http/httputil` 共享包 |
| PM-01 | 错误响应缺少 "details" 字段 | 统一加 `details: {}` |
| PM-02 | 所有 service 错误统一返回 404，应区分 404 vs 500 | 用 `errors.As` 检查 errcode，WriteDomainError 统一映射 |
| ARCH-08 | refresh 后未 persist session，旧 refresh token 仍有效 | 添加 `sessionRepo.Update` |

### 延迟至 Phase 3 的安全项（记入 tech-debt）

SEC-03（密钥硬编码）、SEC-04（HS256 -> RS256）、SEC-06（XFF trustedProxies）、SEC-07（ServiceToken HMAC 无 timestamp）、SEC-08（ID 可预测）、SEC-09（signing method 显式检查）、SEC-10（refresh token rotation）、SEC-11（API 端点认证中间件）。

---

## 5. 已知差距（26 条 Tech Debt）

完整列表见 `tech-debt.md`，按类别汇总如下：

| 类别 | 数量 | 典型项 |
|------|------|--------|
| 安全/权限 [TECH] | 8 条 | 密钥硬编码、HS256、XFF 信任、ServiceToken 重放、ID 可预测、signing method 检查、refresh rotation、API 无认证 |
| 架构 [TECH] | 4 条 | BaseSlice 空壳、chi 直接 import、subscription goroutine 生命周期、L2 事件非事务发布 |
| 测试/回归 [TECH] | 7 条 | handler 覆盖率 < 80%（10/16 slices）、无 audit-trail E2E、bootstrap 覆盖率低、in-memory repo 掩盖集成问题、copylocks warning、cmd 无冒烟测试 |
| 运维/部署 [TECH] | 3 条 | Assembly.Stop 竞态、config watcher 未集成 bootstrap、eventbus 无健康暴露 |
| DX [TECH] | 2 条 | 11 个 runtime 包缺 doc.go、TopicConfigChanged 定义 3 次 |
| 产品/UX [PRODUCT] | 3 条 | Retry-After 硬编码、审计查询错误静默忽略、Update user 仅支持 email |

全部 26 条均标记为 Phase 3 修复。无 P0 遗留。

---

## 6. 用户验收结果

| 视角 | 平均分 | 达标 |
|------|--------|------|
| A (PM/UI) | N/A | Phase 2 无 UI |
| B (开发者 API + 代码) | 3.7 / 5 | >= 3 通过 |
| C (API 消费者) | 4.0 / 5 | >= 3 通过 |
| D (框架集成者) | 3.7 / 5 | >= 3 通过 |

**总体判定: CONDITIONAL APPROVE**

主要摩擦点：11 个 runtime 包缺 doc.go（B3/D2）、Cell 开发指南缺 contract test 说明（D6）。均已记入 tech-debt。

---

## 7. 下一步建议

### Phase 3 优先事项

1. **adapters/ 层实现** -- postgres / redis / rabbitmq adapter，替换 in-memory 桩，解决 7 条 tech-debt（T-05, T-14, D-20 等）
2. **安全加固** -- RS256 迁移（SEC-04）、密钥环境变量化（SEC-03）、trustedProxies（SEC-06）、UUID 替换 UnixNano ID（SEC-08）
3. **OIDC 适配器** -- 支持 J-sso-login OIDC 路径，完成 Phase 2 延迟的 PM-4
4. **Assembly 自动拓扑排序** -- 利用 kernel/governance 已有依赖图数据，替代硬编码注册顺序
5. **文档补充** -- 11 个 runtime 包 doc.go、Cell 开发指南补充 contract test 和错误处理模式

### 架构演进方向

- runtime/eventbus 从 at-most-once 迁移到 RabbitMQ at-least-once（adapters/rabbitmq 实现 outbox.Publisher + outbox.Subscriber）
- L2 事件发布引入 outbox pattern（事务性发布）
- BaseSlice 重构为与 Service/Handler 有实质关联
- cells/ 消除 chi 直接 import，全部通过 RouteMux 抽象

### 流程改进

- 建立 tech-debt 全局注册表（`docs/tech-debt-registry.md`），跨 Phase 追踪
- Phase 3 需要 kernel-review-report.md 和 product-review-report.md（Phase 2 首次启用工作流，无上一 Phase 报告可参考）
