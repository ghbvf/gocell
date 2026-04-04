# GoCell 术语表

## 核心对象

### Cell

运行时、数据主权和部署的边界。拥有权威数据，通过契约发布/消费。

三种类型：
- **core** — 强一致性状态机（如 access-core、audit-core、config-core）
- **edge** — 边缘节点
- **support** — 辅助服务

L0 Cell 是特殊模型：
- 纯计算分区（无状态机、无契约）
- 同 Assembly 内直接导入
- 必须通过 `l0Dependencies` 显式声明

### Slice

最小的开发与验证边界。属于且仅属于一个 Cell。AI Agent 的默认工作单元。

Slice 不是数据边界 — 数据主权属于其所属 Cell。

Slice 是**依赖真相的唯一写入方**，通过 `contractUsages` 声明使用的契约及角色。

### Assembly

将 Cell 打包为可部署二进制的物理配置。从元数据生成。不是业务边界，仅是部署边界。

### Contract

Cell 之间的显式接口。四种类型：

| 类型 | 方向 | 提供方字段 | 消费方字段 | 最低一致性 |
|------|------|-----------|-----------|-----------|
| `http` | 同步请求/响应 | `server` | `clients` | L1 |
| `event` | 异步事实发布 | `publisher` | `subscribers` | L2 |
| `command` | 异步动作请求 | `handler` | `invokers` | L2 |
| `projection` | 读模型订阅 | `provider` | `readers` | L3 |

Schema 分离原则：`contract.yaml` 声明关系（YAML），`*.schema.json` 定义数据格式（JSON Schema）。Schema 文件属于契约版本目录，不属于 Cell 实现目录。

### Journey

跨越一个或多个 Cell 的用户级业务闭环。是**验收真相**，不是依赖真相。

每个 Journey 包含：
- `goal` — 达成目标
- `cells` — 路由锚点（best-effort，非穷举）
- `contracts` — 验收策展（仅列 passCriteria 直接断言的）
- `passCriteria` — 验收标准（auto/manual）

### Journey Catalog

产品级全量 Journey 注册表。蓝图事实。

### Status Board

唯一的动态状态快照。仅此处可包含 `state / risk / blocker / updatedAt`。

不参与架构合法性判定。`validate-meta` 对缺失条目仅发警告。

---

## 一致性等级

| 等级 | 名称 | 范围 | 机制 |
|------|------|------|------|
| L0 | LocalOnly | Slice 内 | 本地计算 |
| L1 | LocalTx | 单 Cell | 数据库事务 |
| L2 | OutboxFact | 单 Cell + 发布 | 事务 + Outbox |
| L3 | WorkflowEventual | 跨 Cell | 事件消费 + 投影 |
| L4 | DeviceLatent | 依赖设备 | 长延迟闭环 |

---

## 表分类

| 分类 | 真相源 | 写入方 | 读取方 | 可重建 |
|------|--------|--------|--------|--------|
| Authoritative | 是 | 仅 Owner Cell | 通过契约 | 不可 |
| Projection | 否 | Consumer Cell | 直接查询 | 可 |
| Cache | 否 | 任何 Cell | 直接查询 | 可 |
| Coordination | 否 | Owner Cell | Owner Cell | 视情况 |

Coordination 包括：outbox、consumed markers、replay checkpoints、job leases。

---

## 数据规则

1. 禁止跨 Cell 写入 Authoritative 表
2. 禁止跨 Cell 外键
3. 禁止跨 Cell UPDATE/DELETE
4. 禁止跨 Cell 共享 Authoritative 写模型
5. Projection 不得升格为 Authoritative
6. Cache 不得伪装为 Projection
7. Outbox 属于 Producer Cell
8. Consumed Marker 属于 Consumer Cell

---

## 治理对象（V3 字段）

### cell.yaml

稳定边界声明。

```yaml
id: access-core              # 必填，== 目录名
type: core                   # core | edge | support
consistencyLevel: L2         # L0-L4
owner:
  team: platform
  role: cell-owner
schema:
  primary: cell_access_core  # L0 可选
verify:
  smoke:
    - smoke.access-core.startup
l0Dependencies:              # 仅在导入 L0 Cell 时声明
  - cell: shared-crypto
    reason: 确定性哈希工具
```

**不包含**：readiness、risk、blocker、status、ownedSlices、authoritativeData、journeys、contracts 反向索引。

### slice.yaml

施工映射声明。依赖真相的唯一写入方。

```yaml
id: session-login            # 必填，== 目录名
belongsToCell: access-core   # derived-anchor，可省略
contractUsages:
  - contract: http.auth.login.v1
    role: serve
  - contract: event.session.created.v1
    role: publish
verify:
  unit:
    - unit.session-login.service
  contract:
    - contract.http.auth.login.v1.serve
  waivers:
    - contract: http.config.get.v1
      owner: platform-team
      reason: 只读配置调用，集成测试已覆盖
      expiresAt: 2026-06-01
```

**不包含**：done、verified、nextAction、status、callsContracts、publishes、consumes。

### contract.yaml

边界协议声明。

```yaml
id: http.auth.login.v1       # 必填
kind: http                   # http | event | command | projection
ownerCell: access-core       # 缺省 = 提供方，可省略
consistencyLevel: L1
lifecycle: active             # draft | active | deprecated
endpoints:
  server: access-core
  clients:
    - edge-bff
schemaRefs:
  request: request.schema.json
  response: response.schema.json
```

**不包含**：version（从 id 末段派生）、status（用 lifecycle 代替）、producer/consumers（用 kind-specific endpoints）。

### assembly.yaml

物理打包声明。手工编写只管 `id / cells / build`。

```yaml
id: core-bundle
cells:
  - access-core
  - audit-core
  - config-core
build:
  entrypoint: src/cmd/core-bundle/main.go
  binary: core-bundle
  deployTemplate: k8s
```

边界信息由工具生成到 `generated/boundary.yaml`。

**不包含**：exportedContracts、importedContracts、smokeTargets（这些在 generated/boundary.yaml 中）。

---

## 禁用字段名（V2 遗留）

以下字段名在 V3 中**禁止使用**，`validate-meta` 会报错：

`cellId` / `sliceId` / `contractId` / `assemblyId` / `ownedSlices` / `authoritativeData` / `producer` / `consumers` / `callsContracts` / `publishes` / `consumes` / `version`（契约上）/ `status`（契约上）

---

## 工具链命令

| 命令 | 用途 | 保证级别 |
|------|------|---------|
| `gocell validate` | 校验全部元数据 | blocking |
| `gocell scaffold cell\|slice\|contract\|journey` | 生成骨架 | — |
| `gocell generate assembly\|indexes\|boundaries` | 生成代码和派生文件 | derived-only |
| `gocell check contract-health\|slice-coverage\|...` | 针对性分析 | — |
| `gocell verify slice\|cell\|journey` | 执行测试 | blocking |
| `gocell verify targets` | 影响面分析 | advisory |
