# Implementation Plan — Phase 4: Examples + Documentation

## 概述

Phase 4 分为 5 个 Wave 交付，总计约 14 个工作日。Wave 0 处理 tech-debt 和安全加固（后续 Wave 的前置依赖），Wave 1-3 并行交付 3 个示例项目，Wave 4 交付文档和收尾。

## 架构决策摘要

| # | 决策 | 影响 |
|---|------|------|
| D1 | IoT L4 应用层实现 + disclaimer | examples/ only |
| D2 | RS256 via Cell Option，WithSigningKey deprecated | cells/ + runtime/auth |
| D3 | outboxWriter fail-fast in Cell.Init | cells/ |
| D4 | 单 module + build tag 隔离 | go.mod + adapters/ tests |
| D5 | v1.0 scope cut 声明 | docs/ |
| D6 | S3 env fallback + deprecation warn | adapters/s3 |
| D7 | git clone 首次体验路径 | README |
| D8 | Grafana Prometheus-compatible placeholders | templates/ |

## Wave 划分

### Wave 0: Tech-Debt 关闭 + 安全加固（Days 1-4）

**前置条件**: 无
**理由**: RS256 迁移和 outboxWriter fail-fast 影响所有 Cell 的测试，必须先完成。testcontainers 需先引入才能验证 adapter。

1. **FR-7.1 RS256 默认化**
   - `runtime/auth/jwt.go`: NewIssuer/NewVerifier 默认 RS256
   - `runtime/auth/keys.go`: 添加 `MustGenerateTestKeyPair()`
   - 更新所有 runtime/auth 测试使用 RSA key pair

2. **FR-7.2 access-core RS256 切换**
   - `cells/access-core/cell.go`: WithJWTIssuer/WithJWTVerifier Option
   - 3 个 slice 构造函数改 auth.JWTIssuer/JWTVerifier
   - 更新 60+ 相关单元测试使用 MustGenerateTestKeyPair()
   - ref: Kubernetes service-account-token 签名模式

3. **FR-7.3 outboxWriter fail-fast**
   - access-core/audit-core/config-core Init 中校验
   - 添加 `ERR_CELL_MISSING_OUTBOX` 错误码
   - 更新测试注入 noop outboxWriter

4. **FR-7.4 S3 env prefix**
   - `adapters/s3/client.go`: GOCELL_S3_* fallback + slog.Warn
   - 更新 .env.example + client_test.go

5. **FR-8.2 docker-compose start_period**

6. **FR-8.3 WithEventBus Deprecated**

7. **FR-6.1 testcontainers-go 引入 go.mod**

### Wave 1: testcontainers + 集成测试（Days 3-6，与 Wave 0 尾部重叠）

**前置条件**: Wave 0 的 testcontainers-go 引入
**理由**: 集成测试是 Phase 3 核心验证缺口，示例项目需要验证过的 adapter。

1. **FR-6.2 PostgreSQL 集成测试**
   - Pool, TxManager, Migrator, OutboxWriter
   - 目标: postgres 覆盖率 ≥ 80%
   - ref: Watermill watermill-sql 集成测试模式

2. **FR-6.3 Redis 集成测试**
   - Client, DistLock, IdempotencyChecker

3. **FR-6.4 RabbitMQ 集成测试**
   - Connection, Publisher, Subscriber, ConsumerBase DLQ

4. **FR-6.5 Outbox 全链路测试**
   - TestIntegration_OutboxFullChain

### Wave 2: 示例项目（Days 5-10，与 Wave 1 尾部重叠）

**前置条件**: Wave 0 (RS256, outboxWriter fail-fast) + Wave 1 (adapter 验证)
**理由**: 示例需要安全加固完成 + adapter 可信。3 个示例可并行开发。

#### Wave 2a: todo-order（P1 golden path）
1. **FR-2.1-2.8** 完整实现
   - order-cell 目录结构 + cell.yaml + slice.yaml
   - Cell 接口实现 + handler + service + repository
   - Contract YAML + Journey YAML
   - docker-compose.yml + README.md + curl 命令
   - ref: go-zero goctl 生成的 CRUD 项目结构

#### Wave 2b: sso-bff（P2 内建 Cell 组合）
1. **FR-1.1-1.7** 完整实现
   - main.go Assembly 配线（3 Cell + 6 adapter）
   - docker-compose.yml + README.md + curl 序列
   - ref: Kratos 认证中间件模式

#### Wave 2c: iot-device（P2 L4 演示）
1. **FR-3.1-3.6** 完整实现
   - device-cell L4 命令队列模式
   - WebSocket hub 集成
   - docker-compose.yml + README.md

### Wave 3: CI + 文档 + 模板（Days 9-12）

**前置条件**: Wave 0 (基础设施) + Wave 2 至少 todo-order 完成

1. **FR-8.1 CI workflow**
   - .github/workflows/ci.yml
   - go build + test + vet + validate + grep checks + coverage gate

2. **FR-4 README Getting Started**
   - 项目简介 + 架构图 + 核心概念
   - 快速开始 (git clone) + 30 分钟教程
   - 示例索引 + 目录结构

3. **FR-5 项目模板（6 个）**
   - ADR, cell-design, contract-review, runbook, postmortem, grafana

4. **FR-9 文档完善**
   - FR-9.1 示例 godoc
   - FR-9.2 CHANGELOG
   - FR-9.3 能力清单
   - FR-9.4 v1.0 scope cut 声明

### Wave 4: 验证 + 收尾（Days 12-14）

**前置条件**: Wave 0-3 全部完成

1. **FR-10 测试验证**
   - go build ./... 全通过
   - go test ./... 全通过
   - go test -tags=integration 全通过
   - gocell validate 零 error
   - 分层 grep 验证
   - kernel/ 覆盖率 ≥ 90%

2. Gate 验证: 30 分钟首个 Cell 体验

## 风险缓解

| 风险 | 概率 | 影响 | 缓解 |
|------|------|------|------|
| RS256 迁移级联破坏 60+ 测试 | 高 | 高 | Wave 0 首先提供 MustGenerateTestKeyPair + noop outboxWriter |
| testcontainers CI 需要 Docker | 中 | 中 | 独立 CI job + ubuntu-latest 原生 Docker |
| sso-bff 配线复杂（3 Cell + 6 adapter） | 中 | 中 | 参考 Phase 3 core-bundle Assembly 已有配线 |
| 14 天工作量紧张 | 高 | 高 | Wave 2 三个示例并行 + Wave 3 文档与 Wave 2 尾部重叠 |

## 对标参考

| 模块 | Primary 对标 | Secondary 对标 |
|------|-------------|---------------|
| 示例项目结构 | go-zero: examples/ | Kratos: examples/ |
| README Getting Started | Uber fx: README quickstart | go-micro: README |
| CI workflow | Kratos: .github/workflows | Watermill: .github/workflows |
| 项目模板 | Kubernetes: hack/boilerplate | go-zero: template/ |
| 集成测试 | Watermill: _examples/tests | pgx: pgxpool_test |
