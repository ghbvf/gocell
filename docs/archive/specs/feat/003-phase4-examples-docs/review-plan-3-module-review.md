# Review Plan 3: 分模块 Review（依赖驱动）

> 先分析项目架构生成完整依赖图，再按模块自底向上逐层 review。

## 设计理念

与 Plan 1（按 PR 时间线）和 Plan 2（按规则维度）不同，本计划从**模块依赖拓扑**出发：先 review 底层模块，确认其接口正确后，再 review 上层模块。这样上层 review 时可以信任下层接口。

## Phase 0: 架构分析 — 生成依赖关系图

### 0.1 模块清单（34 个包）

```
Layer 0 — pkg（共享工具，无内部依赖）
├── pkg/errcode        错误码定义
├── pkg/ctxkeys        Context 键
├── pkg/id             ID 生成（已弃用？）
├── pkg/uid            UUID 生成（crypto/rand）
└── pkg/httputil       HTTP 响应工具 → pkg/errcode

Layer 1 — kernel（运行时核心，只依赖 pkg）
├── kernel/outbox       Outbox 类型定义（纯结构体）
├── kernel/idempotency  幂等检查接口
├── kernel/cell         Cell 基类 + 注册器 → kernel/outbox, pkg/errcode
├── kernel/metadata     YAML 解析器 → pkg/errcode
├── kernel/slice        Slice 验证 → kernel/metadata, pkg/errcode
├── kernel/registry     Cell/Contract 注册表 → kernel/metadata
├── kernel/governance   治理规则引擎 → kernel/cell, kernel/metadata, kernel/registry
├── kernel/assembly     Assembly 生命周期 → kernel/cell, pkg/errcode
├── kernel/scaffold     脚手架生成 → pkg/errcode
└── kernel/journey      Journey 验收目录

Layer 2 — runtime（运行时服务，依赖 kernel + pkg）
├── runtime/auth        JWT 签发/验证（stdlib only）
├── runtime/config      配置热更新（stdlib only）
├── runtime/shutdown    优雅关停（stdlib only）
├── runtime/worker      后台 Worker（stdlib only）
├── runtime/eventbus    事件总线 → kernel/outbox, pkg/errcode, pkg/uid
├── runtime/http
│   ├── middleware       中间件链 → pkg/ctxkeys
│   ├── health          健康检查 → kernel/assembly
│   └── router          路由器 → kernel/cell, runtime/http/*, runtime/observability/*
├── runtime/observability
│   ├── logging          结构化日志 → pkg/ctxkeys
│   ├── metrics          指标（stdlib only）
│   └── tracing          链路追踪 → pkg/ctxkeys, pkg/httputil
└── runtime/bootstrap   引导器（依赖大量 runtime + kernel 模块）

Layer 3 — adapters（外部系统适配，实现 kernel/runtime 接口）
├── adapters/postgres    PG 连接池 + 迁移 + Outbox → pkg/errcode, kernel/outbox, runtime/worker ⚠️
├── adapters/redis       Redis 客户端 + 分布式锁 + 幂等 → pkg/errcode, kernel/idempotency
├── adapters/rabbitmq    消息队列 Publisher/Subscriber → pkg/errcode, kernel/outbox, kernel/idempotency
├── adapters/oidc        OIDC Provider → pkg/errcode
├── adapters/s3          对象存储 → pkg/errcode
└── adapters/websocket   WebSocket Hub（stdlib only）

Layer 4 — cells（业务 Cell）
├── cells/access-core    认证 Cell → runtime/auth, kernel/cell, runtime/eventbus
├── cells/audit-core     审计 Cell → kernel/cell, kernel/outbox
├── cells/config-core    配置 Cell → kernel/cell, kernel/outbox
├── cells/device-cell    设备 Cell（新增 Phase 4）
└── cells/order-cell     订单 Cell（新增 Phase 4）

Layer 5 — examples（示例项目，依赖所有层）
├── examples/sso-bff     SSO BFF 示例
├── examples/todo-order  订单示例
└── examples/iot-device  IoT 设备示例

Layer X — 横切
├── cmd/gocell           CLI 入口
├── cmd/core-bundle      Core Bundle 打包
└── tests/integration    集成测试
```

### 0.2 依赖关系图（DAG）

```
                    pkg/errcode  pkg/ctxkeys  pkg/uid  pkg/id
                         │            │          │
                    pkg/httputil      │          │
                         │            │          │
          ┌──────────────┼────────────┼──────────┼──────────────┐
          ▼              ▼            ▼          ▼              ▼
    kernel/outbox  kernel/metadata  kernel/idempotency  kernel/cell
    kernel/journey kernel/scaffold                    kernel/slice
          │              │                              │
          │         kernel/registry ◄───────────────────┘
          │              │
          │         kernel/governance
          │              │
          ▼         kernel/assembly
          │              │
   ┌──────┼──────────────┼──────────────────────────────┐
   ▼      ▼              ▼                              ▼
runtime/auth  runtime/eventbus  runtime/http/*  runtime/observability/*
runtime/config  runtime/shutdown  runtime/worker
          │
          ▼
   runtime/bootstrap
          │
   ┌──────┼──────────────────────────────────────┐
   ▼      ▼              ▼         ▼             ▼
adapters/ adapters/    adapters/  adapters/  adapters/
postgres  redis        rabbitmq   oidc       s3, websocket
   │         │              │
   ▼         ▼              ▼
cells/access-core  cells/audit-core  cells/config-core
cells/device-cell  cells/order-cell
   │
   ▼
examples/sso-bff  examples/todo-order  examples/iot-device
tests/integration
```

### 0.3 已知依赖异常

| # | 异常 | 说明 | 严重性 |
|---|------|------|--------|
| 1 | `adapters/postgres` → `runtime/worker` | adapters 层应只实现接口，不应直接依赖 runtime。outbox_relay 用了 `runtime/worker.RunConfig` | P1 |
| 2 | `kernel/assembly` generator 依赖 `kernel/registry` 和 `kernel/metadata` | kernel 内部依赖 OK，但 generator 可能属于 cmd/ 层 | P2 |

## Phase 1: 自底向上分模块 Review

### Review 批次与 Agent 分配

按依赖拓扑排序，**下层 review 完成后上层才开始**：

---

#### Batch L0: pkg 层（2 个 agent 并行）

| Agent | 模块 | 文件数(估) | 关注点 |
|-------|------|-----------|--------|
| L0-A | `pkg/errcode` + `pkg/ctxkeys` + `pkg/id` + `pkg/uid` | ~12 | API 稳定性、命名规范、crypto/rand 正确使用、常量覆盖率 |
| L0-B | `pkg/httputil` | ~4 | 与 errcode 集成、响应格式统一 `{"data":...}` |

**Review 要点**:
- errcode 常量是否覆盖所有模块前缀（ERR_AUTH_*, ERR_VALIDATION_*, ...）
- uid 是否全部使用 crypto/rand（PR#8 修复项）
- httputil 响应格式是否符合 API 版本策略

---

#### Batch L1: kernel 层（4 个 agent 并行）

| Agent | 模块 | 文件数(估) | 关注点 |
|-------|------|-----------|--------|
| L1-A | `kernel/outbox` + `kernel/idempotency` + `kernel/journey` | ~8 | 接口定义完整性、类型安全、无多余依赖 |
| L1-B | `kernel/cell` + `kernel/slice` | ~15 | BaseCell 生命周期（LIFO）、mutex 正确性、Slice 验证逻辑 |
| L1-C | `kernel/metadata` + `kernel/registry` + `kernel/governance` | ~20 | YAML 解析健壮性、治理规则覆盖度、依赖检查算法 |
| L1-D | `kernel/assembly` + `kernel/scaffold` | ~15 | Assembly 注册/启动/关停顺序、代码生成模板 |

**Review 要点**:
- kernel 是否只依赖 stdlib + pkg/（覆盖率 ≥ 90% 要求）
- BaseCell mutex 是否完整保护所有状态读写
- governance 规则是否覆盖 CLAUDE.md 中的所有约束
- metadata parser 对 malformed YAML 的处理

---

#### Batch L2: runtime 层（4 个 agent 并行）

| Agent | 模块 | 文件数(估) | 关注点 |
|-------|------|-----------|--------|
| L2-A | `runtime/auth` | ~10 | RS256 实现、JWT 签发/验证、密钥轮换、middleware |
| L2-B | `runtime/eventbus` + `runtime/config` + `runtime/worker` | ~12 | 事件总线线程安全、配置热更新、worker 生命周期 |
| L2-C | `runtime/http/*` (middleware, health, router) | ~15 | 中间件链顺序、rate limit、real IP、健康检查 |
| L2-D | `runtime/observability/*` + `runtime/shutdown` + `runtime/bootstrap` | ~15 | 日志级别合规、tracing 集成、优雅关停顺序、bootstrap 编排 |

**Review 要点**:
- RS256 是否正确替代 HS256（PR#16 核心变更）
- bootstrap 是否正确编排所有生命周期
- middleware 链中间件顺序是否合理（recovery → logging → auth → rate_limit）
- shutdown 是否 LIFO 关停

---

#### Batch L3: adapters 层（3 个 agent 并行）

| Agent | 模块 | 文件数(估) | 关注点 |
|-------|------|-----------|--------|
| L3-A | `adapters/postgres` | ~20 | Pool 管理、TxManager、Migrator、OutboxWriter/Relay、对 runtime/worker 的依赖合理性 |
| L3-B | `adapters/redis` + `adapters/rabbitmq` | ~20 | 分布式锁正确性、幂等检查、ConsumerBase DLQ、Publisher 可靠性 |
| L3-C | `adapters/oidc` + `adapters/s3` + `adapters/websocket` | ~15 | OIDC token 验证、S3 presigned URL、WebSocket 并发安全 |

**Review 要点**:
- 是否正确实现 kernel/ 定义的接口（outbox.Writer, idempotency.Checker）
- postgres outbox_relay 对 runtime/worker 的依赖是否可消除
- testcontainers 集成测试覆盖度（PR 中的新增测试）
- 连接池 / 重连策略健壮性

---

#### Batch L4: cells 层（3 个 agent 并行）

| Agent | 模块 | 文件数(估) | 关注点 |
|-------|------|-----------|--------|
| L4-A | `cells/access-core` | ~20 | Session 管理、RS256 迁移完整性、outbox 集成、禁止跨 Cell import |
| L4-B | `cells/audit-core` + `cells/config-core` | ~15 | 审计写入事务性、配置发布/订阅、L2 OutboxFact 正确使用 |
| L4-C | `cells/device-cell` + `cells/order-cell` | ~20 | 新 Cell 结构规范、cell.yaml/slice.yaml 完整性、内存实现合理性 |

**Review 要点**:
- Cell 之间是否通过 contract 通信（禁止直接 import）
- consistencyLevel 声明 vs 实际实现是否匹配
- 内存 repository 线程安全性（sync.RWMutex）
- 每个 handler 的 HTTP 状态码映射

---

#### Batch L5: examples + integration（2 个 agent 并行）

| Agent | 模块 | 文件数(估) | 关注点 |
|-------|------|-----------|--------|
| L5-A | `examples/sso-bff` + `examples/todo-order` + `examples/iot-device` | ~10 | 示例能否编译运行、是否展示核心 feature、README 准确性 |
| L5-B | `tests/integration` + `cmd/*` | ~10 | outbox 全链路测试覆盖、CLI 入口正确性 |

**Review 要点**:
- 示例是否覆盖 L0-L4 各一致性等级
- docker-compose 配置与代码是否匹配
- 集成测试是否有 skip guard（sandbox 环境）

---

#### Batch LX: 横切关注点（1 个 agent）

| Agent | 范围 | 关注点 |
|-------|------|--------|
| LX-A | `contracts/**/*.yaml` + `journeys/*.yaml` + `*.yaml`(root) | 全量契约完整性、Journey 规格、YAML 格式统一 |

## Phase 2: 跨模块整合 Review

Batch L0-LX 完成后，启动 **1 个汇总 agent**：

1. **依赖合规汇总** — 从所有 batch findings 中提取分层违规
2. **接口一致性** — 上层使用的接口 vs 下层定义的接口是否匹配
3. **错误传播链** — errcode 从 adapter → cell → handler → HTTP 响应的完整链路
4. **事件流完整性** — outbox → relay → rabbitmq → consumer → idempotency 全链路

## 汇总输出

### 产出文件

| 文件 | 内容 |
|------|------|
| `review-plan3-L0-pkg.md` | pkg 层 findings |
| `review-plan3-L1-kernel.md` | kernel 层 findings |
| `review-plan3-L2-runtime.md` | runtime 层 findings |
| `review-plan3-L3-adapters.md` | adapters 层 findings |
| `review-plan3-L4-cells.md` | cells 层 findings |
| `review-plan3-L5-examples.md` | examples + tests findings |
| `review-plan3-LX-contracts.md` | 契约 + Journey findings |
| `review-plan3-cross-module.md` | 跨模块整合 findings |
| **`review-plan3-summary.md`** | **最终汇总 + 依赖图 + 分层健康度** |

### 最终报告格式

```markdown
# 分模块 Review 汇总

## 分层健康度评分
| Layer | 模块数 | 代码行 | P0 | P1 | P2 | 健康度 |
|-------|--------|--------|-----|-----|-----|--------|
| pkg | 5 | ~500 | 0 | 1 | 2 | 🟢 |
| kernel | 10 | ~3000 | 0 | 2 | 3 | 🟢 |
| runtime | 12 | ~2500 | 1 | 3 | 2 | 🟡 |
| adapters | 6 | ~4000 | 1 | 4 | 3 | 🟡 |
| cells | 5 | ~2000 | 2 | 3 | 2 | 🟡 |
| examples | 3 | ~300 | 0 | 1 | 1 | 🟢 |

## 依赖异常列表
...

## 跨模块 Issue
...
```

## 预计总 agent 数: 20 (L0:2 + L1:4 + L2:4 + L3:3 + L4:3 + L5:2 + LX:1 + 汇总:1)
