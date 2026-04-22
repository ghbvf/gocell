# Phase Charter — Phase 4: Examples + Documentation

## Phase 目标

在已完成的 Kernel（Phase 0-1）、Runtime + Built-in Cells（Phase 2）、Adapters（Phase 3）基础上，交付 3 个端到端示例项目和完整的开发者文档体系，使 GoCell 框架从"内部可验证的工程底座"升级为"外部可评估、可采纳的开源框架"。

核心交付价值是**开发者体验闭环**：框架评估者 clone 仓库后 30 分钟内可创建第一个 cell + slice + journey 并跑通。3 个梯度示例（sso-bff → todo-order → iot-device）覆盖 L1-L4 一致性等级的真实场景，配合 README Getting Started、项目模板和完整 godoc，形成从"快速体验"到"深度理解"的文档漏斗。

同时系统性关闭 Phase 3 遗留的验证缺口：testcontainers 集成测试、RS256 安全迁移、outboxWriter fail-fast、S3 环境变量对齐、CI 工作流，确保示例项目运行在经过端到端验证的基础设施之上。

## 范围

### 目标（In Scope）

1. **3 个梯度示例项目**
   - `examples/sso-bff/` — SSO 完整登录流程（access-core + OIDC + PostgreSQL），演示 L1-L2 一致性
   - `examples/todo-order/` — CRUD + 事件驱动 + 自定义 Cell（outbox pattern + RabbitMQ），演示 L2-L3 一致性
   - `examples/iot-device/` — L4 设备管理（高延迟闭环 + 命令回执），演示 L4 一致性
   - 每个示例独立可运行（`docker compose up -d && go run .`），附 curl 命令验证

2. **README Getting Started**
   - 安装指南、5 分钟快速开始、30 分钟完整教程
   - 项目结构说明、核心概念介绍（Cell/Slice/Contract/Journey/Assembly）

3. **项目模板（6 个）**
   - ADR（Architecture Decision Record）
   - cell-design（Cell 设计文档模板）
   - contract-review（Contract 审查清单）
   - runbook（运维手册模板）
   - postmortem（事后复盘模板）
   - Grafana dashboard（监控面板 JSON 模板）

4. **Phase 3 must-fix 技术债务关闭（8 项 INCLUDE）**
   - testcontainers 集成测试（postgres + redis + rabbitmq）
   - postgres adapter 覆盖率提升至 ≥80%
   - access-core RS256 完成切换（默认 RS256，fail-fast 无 key pair）
   - S3 adapter ConfigFromEnv 环境变量前缀对齐 GOCELL_S3_*
   - outboxWriter nil guard 改为 fail-fast（L2+ Cell 必须注入）
   - CI 工作流 (.github/workflows)
   - docker-compose start_period 补全
   - WithEventBus Deprecated 注释

5. **文档完善**
   - 全包 godoc 验证
   - CHANGELOG 更新
   - 能力清单（capability inventory）最终版

### 非目标（Out of Scope）

| 非目标 | 理由 |
|--------|------|
| 生产级 Kubernetes 部署配置 | 超出框架底座范围，属于应用层部署 |
| 前端 UI 界面 | GoCell 是纯后端 Go 框架，无前端交付物 |
| 性能基准测试和调优 | post-v1.0 优化项 |
| 多租户支持 | 未在 roadmap 中 |
| 可选 adapter（MySQL、Kafka、gRPC 等） | 超出一等 adapter 范围，按需后续版本添加 |
| VictoriaMetrics adapter | master-plan 列为一等适配器但 Phase 3 未实现，延迟至 v1.1 |
| WinMDM POC 集成 | 外部项目关注点，不在 GoCell 框架范围内 |
| Optional adapter 接口桩 | 空接口无评估价值，延迟至有真实实现需求时 |
| #10: Session refresh TOCTOU 竞态修复 | 高风险重构，需完整 session 生命周期重设计 |
| #11: Domain model refactoring | 高风险重构，影响面大，post-v1.0 |
| #12: configpublish.Rollback version validation | 需持久化版本管理，post-v1.0 |
| #4: sandbox httptest 端口绑定 panic | CI 环境特定问题，非代码缺陷 |

### N/A 声明

| 标准文件 | 理由 |
|---------|------|
| evidence/playwright/result.txt | N/A:SCOPE_IRRELEVANT — Phase 4 无 UI，前端开发者 OFF |
| playwright.config.ts | N/A:SCOPE_IRRELEVANT — 同上 |

## 连续性处理

### 从上一 Phase 继承的必须修复项

**RED WARNING: 合计 18 项 > 9 项阈值，过载保护触发。总负责人裁决如下。**

**来源: kernel-review-report.md (3 条)**

| # | 来源文件 | 项目 | 处理方式 |
|---|---------|------|---------|
| K1 | kernel-review-report.md | [TECH #1/#2/#7] 集成测试从 stub 升级为 testcontainers | 纳入本 Phase — examples 需要可验证的基础设施 |
| K2 | kernel-review-report.md | [TECH #9] access-core RS256 完成切换 | 纳入本 Phase — 跨 2 Phase 延迟的安全债务 |
| K3 | kernel-review-report.md | [TECH #6] outboxWriter nil guard fail-fast | 纳入本 Phase — L2 架构完整性 |

**来源: product-review-report.md (3 条)**

| # | 来源文件 | 项目 | 处理方式 |
|---|---------|------|---------|
| MF-1 | product-review-report.md | 实现 testcontainers 集成测试 | 纳入本 Phase（与 K1 合并） |
| MF-2 | product-review-report.md | postgres adapter 覆盖率 ≥80% | 纳入本 Phase（与 K1 联动） |
| MF-3 | product-review-report.md | S3 adapter ConfigFromEnv 前缀对齐 | 纳入本 Phase — 1 行修复 |

**来源: tech-debt.md (12 条)**

| # | tech-debt 编号 | 项目 | 处理方式 |
|---|---------------|------|---------|
| TD-1 | #1/#7 | testcontainers-go 未引入 + integration_test stub | 纳入本 Phase（与 K1/MF-1 合并） |
| TD-2 | #2 | postgres 覆盖率 46.6% | 纳入本 Phase（与 MF-2 合并） |
| TD-3 | #3 | CI 工作流缺失 | 纳入本 Phase — examples 需要 CI |
| TD-4 | #4 | sandbox httptest 端口 panic | DEFERRED — CI 环境特定，非代码缺陷 |
| TD-5 | #5 | docker-compose start_period | 纳入本 Phase — 低工作量 |
| TD-6 | #6 | outboxWriter nil guard 静默 fallback | 纳入本 Phase（与 K3 合并） |
| TD-7 | #8 | WithEventBus 未标 Deprecated | 纳入本 Phase — 低工作量 |
| TD-8 | #9 | RS256 默认仍 HS256 | 纳入本 Phase（与 K2 合并） |
| TD-9 | #10 | TOCTOU 竞态 | DEFERRED — 高风险重构，post-v1.0 |
| TD-10 | #11 | domain 模型重构 | DEFERRED — 高风险重构，post-v1.0 |
| TD-11 | #12 | Rollback version 校验 | DEFERRED — 需持久化版本管理 |

**去重后统计: 8 项 INCLUDE（纳入本 Phase），4 项 DEFERRED**

### 延迟处理（过载保护触发）

| 项目 | 延迟理由 | 计划修复时机 |
|------|---------|-------------|
| #4 sandbox httptest 端口 panic | CI 环境特定的 sandbox 限制，非 Go 代码缺陷，在非 sandbox 环境正常通过 | CI 环境配置修复时 |
| #10 Session refresh TOCTOU 竞态 | 需重新设计 Session 生命周期模型 + Redis 分布式锁深度集成，影响面超出文档/示例 Phase | post-v1.0 |
| #11 Domain model refactoring | Service 接口返回类型重构影响所有 Cell，需独立重构 Phase | post-v1.0 |
| #12 configpublish.Rollback version 校验 | 需持久化版本管理基础设施，当前无持久化 version store | post-v1.0 |

### 前端开发者连续跳过处理方案

前端开发者已连续 3 Phase 跳过（Phase 2 + Phase 3 + Phase 4）。

**处理方案**: GoCell 项目定位为纯后端 Go 框架，从立项至今无 UI 层交付物。Phase 4 的 3 个 examples 全部是 Go 后端服务（sso-bff 是 Go HTTP server，非前端 SPA）。前端开发者角色在本项目生命周期内无适用场景。若未来引入 Web Dashboard 或前端 SDK，需重新评估。

## Gate 验证

```bash
# Phase 4 Gate: 新项目 30 分钟内创建第一个 cell + slice + journey 并跑通
cd examples/todo-order && docker compose up -d && go run .
# 验证 curl 命令按 README 执行成功
```
