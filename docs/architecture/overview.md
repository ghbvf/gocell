# GoCell 架构概览

## 什么是 GoCell

GoCell 是以 Cell 为核心构建单元的 Go 工程底座。提供：

1. **Cell/Slice 运行时** — 接口定义 + 基础实现，用于构建可组合的 Cell 服务
2. **治理工具链** — 元数据校验、Assembly 代码生成、契约注册、影响面分析
3. **内置 Cell** — access-core（认证）、audit-core（审计追踪）、config-core（热更新 + 功能开关）
4. **适配器** — PostgreSQL、Redis、OIDC、S3、VictoriaMetrics、RabbitMQ、WebSocket

---

## 核心概念

### Cell

**Cell** 是运行时、数据主权和部署的边界。

- 拥有权威数据（定义业务真相的表）
- 通过契约发布和消费
- 声明一致性等级（L0-L4）
- 包含一个或多个 Slice

三种类型：
- **core** — 强一致性状态机（如 access-core、audit-core、config-core）
- **edge** — 边缘节点 Cell
- **support** — 辅助 Cell

L0 Cell 是特殊类型：纯计算分区，同 Assembly 内直接导入，不参与契约。

### Slice

**Slice** 是最小的开发与验证边界。

- 属于且仅属于一个 Cell（one-slice-one-cell）
- 是 AI Agent 的默认工作单元
- 拥有自己的 unit + contract 测试
- **不拥有数据** — 数据主权属于 Cell
- 是**依赖真相的唯一写入方**（通过 `contractUsages`）

### Assembly

**Assembly** 是将一个或多个 Cell 打包为可部署二进制的物理配置。

- 由 `assembly.yaml` + `cell.yaml` 生成
- 管理 Cell 启动/关闭顺序
- 不是业务边界 — 仅是部署边界
- 边界信息由工具生成到 `generated/boundary.yaml`，禁止手编

### Contract

**Contract** 是 Cell 之间的显式接口。L1+ Cell 之间的所有交互都需要契约。

四种类型及端点字段：

| 类型 | 提供方字段 | 消费方字段 | 一致性 |
|------|-----------|-----------|--------|
| `http` | `server` | `clients` | L1 |
| `event` | `publisher` | `subscribers` | L2 |
| `command` | `handler` | `invokers` | L2 |
| `projection` | `provider` | `readers` | L3 |

契约目录结构：
```
src/contracts/{kind}/{domain...}/{version}/
├── contract.yaml           # 关系声明（谁提供/谁消费）
├── request.schema.json     # 请求格式定义
└── response.schema.json    # 响应格式定义
```

生命周期：`draft → active → deprecated`，单向不可逆。

Event 额外必填：`replayable`、`idempotencyKey`、`deliverySemantics`。

### Journey

**Journey** 是跨越一个或多个 Cell 的用户级业务闭环。

- 是**验收真相**，不是依赖真相
- `cells` 是路由锚点（best-effort），不是完备参与方集合
- `contracts` 是验收策展，不是完整依赖图
- 需要完整依赖图时，从 `slice.contractUsages` 聚合

### Status Board

**运营层**。不参与架构合法性判定。

- `validate-meta` 对缺失条目仅发警告，不阻断 CI
- Release 门禁是流水线策略，不是模型规则

---

## 六条真相（V3 核心原则）

1. `slice.contractUsages` 是实现级依赖真相
2. `contract.yaml` 是边界协议真相
3. `journey.yaml` 是验收真相，不是依赖真相
4. `status-board.yaml` 是运营层，不参与架构合法性判定
5. L0 是例外模型，必须显式声明依赖（`l0Dependencies`）
6. 每条声明都必须对应 `verify` 或 `waiver`

---

## 五层信息模型

| 层 | 文件 | 职责 | 事实类型 |
|---|------|------|---------|
| Journey Catalog | `src/journeys/catalog.yaml` | 产品全量 Journey 总表 | 蓝图事实 |
| Journey Spec | `src/journeys/J-*.yaml` | 单条 Journey 验收规格 | 验收事实 |
| cell.yaml | `src/cells/*/cell.yaml` | 稳定边界 + 治理事实 | 边界事实 |
| slice.yaml | `src/cells/*/slices/*/slice.yaml` | 施工映射 + 影响面 | 施工映射事实 |
| Status Board | `src/journeys/status-board.yaml` | 唯一动态状态快照 | 动态事实 |

**规则：稳定治理事实留在 cell/slice。动态状态只在 Status Board。**

---

## 真相归属

每个关键事实只有一个 owner。

| 事实 | Owner | 不是 owner |
|------|-------|-----------|
| Slice 用了哪些契约 | `slice.contractUsages` | journey.contracts |
| 契约边界协议 | `contract.yaml` | slice 或 journey |
| 验收标准 | `journey.passCriteria` | slice.verify |
| Cell 归属 | `slice.belongsToCell`（或目录路径） | cell.yaml 反向索引 |
| 交付状态 | `src/journeys/status-board.yaml` | 任何其他元数据文件 |
| Assembly 边界 | `src/assemblies/*/generated/boundary.yaml` | assembly.yaml 内联 |

---

## 一致性等级（L0-L4）

| 等级 | 含义 | 场景 | 验证 |
|------|------|------|------|
| L0 LocalOnly | 单 Slice 内部本地处理 | 纯计算、校验 | Unit 测试 |
| L1 LocalTx | 单 Cell 本地事务 | Session 创建、审计写入 | 事务测试 |
| L2 OutboxFact | 本地事务 + Outbox 发布 | session.created 事件 | Outbox + Consumer 测试 |
| L3 WorkflowEventual | 跨 Cell 最终一致 | 查询投影、合规追踪 | Replay + 投影测试 |
| L4 DeviceLatent | 设备长延迟闭环 | 命令回执、证书续期 | 超时 + 迟到处理测试 |

详见 [consistency.md](consistency.md)。

---

## 依赖模型

只有两类边：

### 契约依赖
`slice.contractUsages` 声明。由 `validate-meta` 校验引用存在性和角色合法性。

### 显式非契约依赖
`cell.l0Dependencies` 声明 L0 导入关系。由 `validate-meta` 校验目标存在且在同 Assembly。

其他非契约耦合（共享进程状态、运行时配置注入等）当前不可建模。`select-targets` 因此是 **advisory** 级别。

---

## 验证保证

| 工具 | 保证级别 | 说明 |
|------|---------|------|
| `validate-meta` | **blocking** | 校验通过才允许合入。校验内容：引用存在性、拓扑合法性、格式合规 |
| `select-targets` | **advisory** | 输出是优化建议，不是完整性证明 |
| `verify-slice` | **blocking** | 执行 Slice 的 unit + contract 测试 |
| `verify-cell` | **blocking** | 执行 Cell 的 smoke 测试 |
| `run-journey` | **blocking** | 执行 Journey 的 auto passCriteria |
| `generate-assembly` | **derived-only** | 产出 boundary.yaml 和索引，自身不做校验 |

---

## 设计原则

1. **One-slice-one-cell** — 一个 Slice 只属于一个 Cell
2. **数据主权归 Cell** — Slice 不拥有数据
3. **跨 Cell 通信只通过契约** — 禁止直接 import 另一个 Cell 的 internal/
4. **L0 例外** — L0 Cell 可被同 Assembly 内兄弟 Cell 直接导入，但必须在 `l0Dependencies` 显式声明
5. **Metadata-first** — validate-meta 通过之前不生成代码
6. **Assembly 是生成的** — 不手写
7. **Journey 是验收边界** — 不是依赖边界
8. **投影可丢弃重建** — Projection 表永远不升格为 Authoritative
9. **动态状态只在 Status Board** — cell.yaml / slice.yaml / contract.yaml 不含交付状态

---

## 表分类

| 分类 | 真相源 | 写入方 | 读取方 | 可重建 |
|------|--------|--------|--------|--------|
| Authoritative | 是 | 仅 Owner Cell | 通过契约 | 不可 |
| Projection | 否 | Consumer Cell | 直接查询 | 可（从事件重放） |
| Cache | 否 | 任何 Cell | 直接查询 | 可（从源） |
| Coordination | 否 | Owner Cell | Owner Cell | 视情况（outbox 可，lease 不可） |

Coordination 包括：outbox、consumed markers、replay checkpoints、job leases。

---

## 目录结构

```
src/
├── kernel/                         # Cell/Slice 运行时 + 治理工具（底座灵魂）
│   ├── cell/                       # Cell/Slice/Assembly 接口 + BaseCell/BaseSlice
│   ├── assembly/                   # Assembly 运行时 + 代码生成
│   ├── metadata/                   # YAML 解析器 + Go 类型 + JSON Schema
│   ├── governance/                 # validate-meta + dependency checker + select-targets
│   ├── registry/                   # Contract/Cell 注册表
│   ├── journey/                    # Journey Catalog
│   ├── scaffold/                   # 脚手架（new-cell / new-slice / new-contract）
│   ├── slice/                      # verify-slice / verify-cell / run-journey
│   ├── outbox/                     # 事务性 Outbox 接口
│   ├── idempotency/                # 消费者幂等接口
│   └── contract/                   # 契约类型定义
├── cells/                          # 内置 Cell
│   ├── access-core/                # SSO/OIDC 认证
│   ├── audit-core/                 # 防篡改审计追踪
│   └── config-core/                # 配置热更新 + 功能开关
├── contracts/                      # 跨 Cell 边界契约
│   ├── http/{domain}/{version}/    # HTTP 契约
│   ├── event/{domain}/{version}/   # Event 契约
│   ├── command/                    # Command 契约
│   └── projection/                 # Projection 契约
├── journeys/                       # Journey 验收规格 + Status Board
├── assemblies/                     # 物理打包配置
│   └── {id}/generated/             # 工具生成产物（禁止手编）
├── runtime/                        # 通用运行时
│   ├── http/                       # 中间件 + 路由 + 健康检查
│   ├── auth/                       # JWT / RBAC / 服务间认证
│   ├── worker/                     # 后台任务框架
│   └── observability/              # 指标 / 追踪 / 日志
├── adapters/                       # 外部系统适配
│   ├── postgres/                   # 连接 + TxManager + Migrator
│   ├── redis/                      # 连接 + 分布式锁 + 幂等
│   ├── oidc/                       # OIDC Provider
│   ├── s3/                         # 对象存储
│   ├── rabbitmq/                   # 消息队列
│   └── websocket/                  # WebSocket
├── pkg/                            # 共享工具包
│   ├── errcode/                    # 错误码
│   └── ctxkeys/                    # Context Key
├── cmd/gocell/                     # CLI 入口
├── examples/                       # 示例项目
├── generated/                      # 工具生成产物（禁止手编）
├── fixtures/                       # 测试夹具
├── actors.yaml                     # 外部 Actor 注册
└── go.mod
```

---

## 工具链命令

| 命令 | 用途 |
|------|------|
| `gocell validate` | 校验全部元数据（blocking） |
| `gocell scaffold cell\|slice\|contract\|journey` | 生成新 Cell/Slice/Contract/Journey 骨架 |
| `gocell generate assembly\|indexes\|boundaries` | 生成 Go 代码和派生文件 |
| `gocell check contract-health\|slice-coverage\|...` | 针对性架构分析 |
| `gocell verify slice\|cell\|journey\|targets` | 执行测试（go test 智能包装） |
