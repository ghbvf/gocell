# Roadmap Update — Phase 2 完成

> 日期: 2026-04-05
> 来源: phase-report.md, tech-debt.md, kernel-review-report.md, product-review-report.md
> 原始 roadmap: docs/product/roadmap/202604050853-000-gocell框架补充计划.md (Phase 2 章节)

---

## Phase 2 实际 vs 计划

| 计划项 | 状态 | 偏差说明 |
|--------|------|---------|
| **Week 4-5: Runtime 层** | | |
| runtime/http/middleware (7 个中间件) | 完成 | request_id, real_ip, recovery, access_log, security_headers, body_limit, rate_limit 全部交付 |
| runtime/http/health (/healthz + /readyz) | 完成 | HealthHandler 聚合 Assembly.Health()，符合计划 |
| runtime/http/router (chi-based 路由构建器) | 完成 | 新增 RouteMux 抽象层，覆盖率 78.8% 略低于 80% 阈值（差 1.2%） |
| runtime/config (YAML/env 配置 + watcher) | 完成 | config.go + watcher.go 交付；watcher 未集成到 bootstrap 生命周期（tech-debt #20） |
| runtime/bootstrap (统一启动器) | 完成 | bootstrap.go 功能完整，覆盖率 51.4%（sandbox 限制 net.Listen） |
| runtime/shutdown (graceful shutdown) | 完成 | 符合计划 |
| runtime/observability (Prometheus + OTel + slog) | 完成 | metrics/ + tracing/ + logging/ 三个子包 |
| runtime/worker (后台 worker) | 完成 | worker.go + periodic.go |
| runtime/auth/jwt (RS256 验证) | 完成（降级） | 实际使用 HS256 对称签名，RS256 延至 Phase 3。决策 D8 明确记录理由：Phase 2 单进程部署 |
| runtime/auth/rbac (RBAC 中间件) | 完成 | 抽象中间件框架（TokenVerifier + Authorizer 接口），不耦合具体策略 |
| runtime/auth/servicetoken (服务间认证) | 完成 | HMAC 无 timestamp（tech-debt #8），Phase 3 修复 |
| runtime/eventbus (计划外新增) | 新增完成 | in-memory Pub/Sub，at-most-once + 3x 重试 + dead letter。计划中未列出但为 Cell 间事件通信必要补充 |
| **Week 6: access-core Cell** | | |
| identity-manage slice | 完成 | 用户 CRUD + 锁定/解锁 |
| session-login slice | 完成 | 密码登录 + JWT(HS256) 签发。OIDC 延至 Phase 3（需 OIDC adapter） |
| session-refresh slice | 完成 | token 刷新。S6 修复了 refresh 后未 persist session 的问题（ARCH-08） |
| session-logout slice | 完成 | session 吊销 + event.session.revoked 发布 |
| authorization-decide slice | 完成 | RBAC 权限判定 |
| 补齐契约 (event.session.revoked.v1 等) | 完成 | YAML 元数据全部修正，gocell validate 零 error |
| 补齐 Journey (J-session-refresh 等) | 完成 | Hard Gate 5/5 PASS |
| **Week 7: audit-core Cell** | | |
| audit-write slice | 完成 | HMAC-SHA256 hash chain 实现 |
| audit-verify slice | 完成 | 符合计划 |
| audit-archive slice | 完成 | 符合计划 |
| 补齐契约 (event.audit.integrity-verified.v1) | 完成 | 符合计划 |
| 补齐 Journey (J-audit-login-trail 跨 cell) | 部分完成 | Soft Gate 通过（in-memory EventBus 验证），端到端集成测试待 Phase 3 adapter |
| **Week 8: config-core Cell** | | |
| config-manage slice | 完成 | 配置 CRUD + 版本管理 |
| config-publish slice | 完成 | 符合计划 |
| config-subscribe slice | 完成 | 符合计划 |
| feature-flag slice | 完成 | 仅布尔开关 + 百分比 rollout（决策 D7：最小可用集） |
| 补齐契约 (event.config.rollback.v1 等) | 完成 | 符合计划 |
| 补齐 Journey (J-config-hot-reload 等) | 部分完成 | watcher 未集成 bootstrap，J-config-hot-reload 完整链路需 Phase 3 |
| **Phase 2 Gate** | | |
| 3 个 Cell 在 core-bundle assembly 中运行 | 完成 | cmd/core-bundle 硬编码注册顺序 config-core -> access-core -> audit-core |
| 8 条 journey 通过 | 部分完成 | Hard Gate 5/5 PASS + Soft Gate 3/3 PASS（in-memory 验证）。端到端全链路待 Phase 3 |
| **计划外交付** | | |
| kernel/outbox 新增 Subscriber 接口 | 新增完成 | 4 方一致认为缺 Subscriber 是最高风险卡点（决策 D1） |
| kernel/cell 新增 HTTPRegistrar + EventRegistrar + RouteMux | 新增完成 | Cell 接口从治理扩展到运行时，可选接口保持向后兼容（决策 D12） |
| YAML 元数据全量修正 (Wave 0) | 新增完成 | 元数据不一致阻碍 gate 检查，决策 D10 |
| runtime/http/httputil 共享包 | 新增完成 | S6 修复 DX-01: writeJSON/writeError 重复定义 12 处 |
| S6 安全修复 (bcrypt + DTO) | 新增完成 | SEC-01 密码比较迁移 bcrypt，SEC-02 UserResponse DTO 排除 PasswordHash |

### 计划偏差总结

- **功能完整度**: 计划内模块全部交付，无删减
- **降级项 (4 项)**: JWT RS256->HS256、OIDC 延迟、分布式限流延迟、Assembly 自动拓扑排序延迟。均为有意识决策，记入 tech-debt
- **超额交付 (5 项)**: runtime/eventbus、kernel/ 接口扩展（Subscriber + HTTPRegistrar + EventRegistrar + RouteMux）、Wave 0 YAML 修正、httputil 共享包、S6 安全修复
- **覆盖率缺口**: bootstrap 51.4%（环境限制）、router 78.8%（差 1.2%）

---

## Phase 3 前置输入

### 必须修复（来自 kernel-review-report + product-review-report）

以下 6 条来自两份审查报告的"必须修复"项，去重合并后为 5 条：

| # | 来源 | 问题 | 要求动作 | 性质 |
|---|------|------|---------|------|
| MF-1 | kernel #3 + product #-- | **密钥硬编码 + JWT HS256** (SEC-03 + SEC-04) | (a) 密钥改环境变量，缺失时 fail-fast；(b) JWT 签名迁移 RS256，Cell.Init 注入公私钥对 | 代码修复 |
| MF-2 | kernel #2 | **handler 层覆盖率 < 80%** (10/16 slices) | httptest.NewRecorder + chi.NewRouter 补充请求-响应级测试，覆盖正常路径 + 参数错误 + 认证失败 | 测试补充 |
| MF-3 | kernel #1 | **config watcher 未集成 bootstrap 生命周期** (tech-debt #20) | bootstrap.Run() 启动 watcher，Stop() 关闭，watcher context 从 shutdownCtx 派生 | 代码修复 |
| MF-4 | product #1 | **AC-8.2 签名算法描述与决策不一致** | 修订 product-acceptance-criteria.md AC-8.2 描述，标注 HS256(Phase 2) + RS256 延迟至 Phase 3 | 文档对齐 |
| MF-5 | product #2 + #3 | **router.go 覆盖率 78.8% + 审计查询 time.Parse 静默忽略** | (a) 补 1-2 个 Route/Mount/Group 单测推至 >= 80%；(b) audit-query handler time.Parse 失败返回 400 + ERR_VALIDATION_INVALID_TIME_FORMAT | 代码+测试 |

### Tech debt 优先处理项

从 26 条 tech-debt 中按风险和 Phase 3 依赖关系排序，建议 Phase 3 优先处理的前 10 条：

| 优先级 | tech-debt # | 问题 | 理由 |
|--------|-------------|------|------|
| P0 | #5 (SEC-03) | 密钥硬编码 | Phase 3 引入 Docker 部署后直接暴露，也是 MF-1 要求 |
| P0 | #6 (SEC-04) | JWT HS256 | 多实例部署密钥分发困难，spec 原始要求 RS256，也是 MF-1 要求 |
| P0 | #20 (D-07) | config watcher 未集成 bootstrap | J-config-hot-reload 依赖此修复，也是 MF-3 要求 |
| P1 | #13 (T-01) | 10/16 slices handler 覆盖率 < 80% | Phase 3 认证中间件(SEC-11)验证依赖 handler 测试，也是 MF-2 要求 |
| P1 | #12 (SEC-11) | API 端点无认证中间件 | Phase 3 adapter 引入后外部可访问，必须加认证 |
| P1 | #4 (ARCH-07) | L2 事件发布不在事务中 | Phase 3 引入 postgres adapter 后必须改为 outbox.Writer |
| P1 | #16 (T-05) | in-memory repo 掩盖集成问题 | Phase 3 adapter 替换后需真实验证 |
| P2 | #7 (SEC-06) | RealIP 无条件信任 XFF | 部署到反代后端时限流可被绕过 |
| P2 | #9 (SEC-08) | Session/User ID 用 UnixNano 可预测 | Phase 3 引入持久化后 ID 不可变更，必须在此之前迁移 UUID |
| P2 | #1 (ARCH-04) | BaseSlice 空壳 | kernel Slice 接口重构影响面大，越早越好 |

剩余 16 条（#2 chi 直接 import、#3 subscription goroutine、#8 ServiceToken 重放、#10 signing method、#11 refresh rotation、#14 audit E2E、#15 bootstrap 覆盖率、#17 copylocks、#18 cmd 冒烟测试、#19 Assembly.Stop 竞态、#21 eventbus 健康暴露、#22 doc.go、#23 TopicConfigChanged 重复、#24 Retry-After、#25 审计查询错误、#26 Update user 字段）按 Phase 3 进度穿插处理。

### Phase 3 范围确认

根据原始 roadmap Phase 3 定义（Days 64-77, Adapters 层）+ Phase 2 产出的前置修复需求，Phase 3 范围确认如下：

**一、原始计划范围（维持）**

| 包 | 内容 |
|---|---|
| adapters/postgres | 连接池 + TxManager + Migrator + outbox Writer/Relay 实现 |
| adapters/redis | 连接 + 分布式锁 + idempotency.Checker 实现 |
| adapters/oidc | OIDC provider client + token exchange |
| adapters/s3 | S3/MinIO client + presigned URL |
| adapters/rabbitmq | Publisher + Consumer（ConsumerBase + DLQ + retry） |
| adapters/websocket | WebSocket hub + signal-first 模式 |

**二、Phase 2 回灌的必修项（新增到 Phase 3 scope）**

| 项 | 工作量估计 | 来源 |
|---|---|---|
| 密钥环境变量化 + fail-fast | 0.5d | MF-1 / SEC-03 |
| JWT HS256 -> RS256 迁移 | 1d | MF-1 / SEC-04 |
| config watcher 集成 bootstrap | 0.5d | MF-3 / D-07 |
| handler 层覆盖率补充 (10 slices) | 2d | MF-2 / T-01 |
| router.go 覆盖率补充 | 0.5h | MF-5 |
| 审计查询 time.Parse 返回 400 | 0.5h | MF-5 |
| AC-8.2 文档对齐 | 0.5h | MF-4 |
| API 认证中间件 | 1d | SEC-11 |
| L2 事件改 outbox pattern | 1d | ARCH-07（依赖 postgres adapter） |
| Session/User ID 改 UUID | 0.5d | SEC-08 |

**三、Phase 3 可选增强（如时间允许）**

| 项 | 来源 |
|---|---|
| Assembly 自动拓扑排序 | Phase 2 降级项 |
| BaseSlice 重构为与 Service/Handler 有实质关联 | ARCH-04 |
| cells/ 消除 chi 直接 import | ARCH-05 |
| 11 个 runtime 包 doc.go | DX-02 |
| TopicConfigChanged 常量去重 | DX-03 |
| RealIP trustedProxies | SEC-06 |
| ServiceToken 加 timestamp | SEC-07 |
| refresh token rotation reuse detection | SEC-10 |

**四、Phase 3 Gate 更新**

原始 Gate:
> 全链路 outbox->relay->consume, OIDC login, RabbitMQ DLQ, WebSocket push

更新后 Gate:
> 1. 全链路 outbox->relay->consume（testcontainers 集成测试）
> 2. OIDC login 完整流程
> 3. RabbitMQ DLQ 验证
> 4. WebSocket push 验证
> 5. JWT RS256 签名验证（新增，来自 MF-1）
> 6. config watcher + bootstrap 集成生命周期测试（新增，来自 MF-3）
> 7. runtime/ 全部包覆盖率 >= 80%（新增，来自 MF-2/MF-5）
> 8. API 端点认证中间件覆盖（新增，来自 SEC-11）

---

## 附录: Phase 2 关键数字

| 指标 | 数值 |
|------|------|
| 变更文件数 | 173 |
| 新增代码行 | 15,117 |
| 测试包数 | 48（0 failures） |
| 架构决策 | 14 项（D1-D14） |
| S6 安全修复 | P0: 2 条, P1: 5 条 |
| Tech debt | 26 条（23 TECH + 3 PRODUCT） |
| User signoff | CONDITIONAL APPROVE（B: 3.7, C: 4.0, D: 3.7） |
| Kernel Guardian 判定 | PASS |
| Product 判定 | CONDITIONAL APPROVE |
