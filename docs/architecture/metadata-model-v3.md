# GoCell 元数据模型 V3

> 本文档从 V2.1 最小重建。只保留不可逆的真相层和边界定义，不写满规则。
> 细节在工具实现中优化，不在文档中预设。

当前仓库约定：元数据根目录为 项目根目录。下文中的路径示例均以仓库根相对路径表示。

## 六条真相

1. `slice.contractUsages` 是实现级依赖真相。
2. `contract.yaml` 是边界协议真相。
3. `journey.yaml` 是验收真相，不是依赖真相。
4. `status-board.yaml` 是运营层，不参与架构合法性判定。
5. L0 是例外模型，必须显式声明依赖（`l0Dependencies`）。
6. 每条声明都必须对应 `verify` 或 `waiver`。

凡是不完备、不可验证、只是运营状态的东西，都不要当成架构真相。

---

## 一、核心模型

### Cell

治理分区。两种子类型：

- **L1+**：运行时边界，拥有数据主权，通过契约与外部通信。
- **L0**：计算分区，纯函数库，同 assembly 内直接导入，不参与契约。

```yaml
# cells/access-core/cell.yaml
id: access-core
type: core
consistencyLevel: L2
owner:
  team: platform
  role: cell-owner
schema:
  primary: cell_access_core
verify:
  smoke:
    - smoke.access-core.startup
l0Dependencies:           # 仅在导入 L0 Cell 时声明
  - cell: shared-crypto
    reason: 确定性哈希工具
```

目录约定：`cells/{cell-id}/cell.yaml`，且 `cell.id` == 目录名。`schema.primary` 对 L0 可选。

### Slice

实现映射。slice 是**依赖真相的唯一写入方**。

```yaml
# cells/access-core/slices/session-login/slice.yaml
id: session-login
belongsToCell: access-core       # derived-anchor，可省略
contractUsages:
  - contract: http.auth.login.v1
    role: serve
  - contract: http.config.get.v1
    role: call
  - contract: event.session.created.v1
    role: publish
verify:
  unit:
    - unit.session-login.service
  contract:
    - contract.http.auth.login.v1.serve
    - contract.event.session.created.v1.publish
  waivers:
    - contract: http.config.get.v1
      owner: platform-team
      reason: 只读配置调用，集成测试已覆盖
      expiresAt: 2026-06-01
```

目录约定：`cells/{cell-id}/slices/{slice-id}/slice.yaml`，且 `slice.id` == 目录名。`owner` / `consistencyLevel` 继承自 Cell。`allowedFiles` 必填（FMT-14 治理规则强制，`gocell scaffold` 生成初始值）。

### Contract

跨 Cell 边界协议。L1+ Cell 之间的所有交互都需要契约。

```yaml
# contracts/http/auth/login/v1/contract.yaml
id: http.auth.login.v1
kind: http                        # derived-anchor，可省略
ownerCell: access-core            # 缺省 = provider，可省略
consistencyLevel: L1
lifecycle: active
endpoints:
  server: access-core
  clients:
    - edge-bff
schemaRefs:
  request: request.schema.json
  response: response.schema.json
```

目录约定：`contracts/{kind}/{domain...}/{version}/contract.yaml`。`schemaRefs` 相对 `contract.yaml` 所在目录解析。

四种 kind：`http` / `event` / `command` / `projection`。端点字段按 kind 不同：

| Kind | 提供方 | 消费方 |
|------|--------|--------|
| `http` | `server` | `clients` |
| `event` | `publisher` | `subscribers` |
| `command` | `handler` | `invokers` |
| `projection` | `provider` | `readers` |

生命周期：`draft → active → deprecated`，单向不可逆。

Event 额外必填：`replayable`、`idempotencyKey`、`deliverySemantics`。
Projection 额外必填：`replayable`。

### Journey

验收真相。定义"完成"的含义。**不是依赖真相。**

```yaml
# journeys/J-sso-login.yaml
id: J-sso-login
goal: 用户完成 SSO 登录并获得有效 session
owner:
  team: platform
  role: journey-owner
cells:                            # 路由锚点（best-effort，非穷举）
  - access-core
  - audit-core
  - config-core
contracts:                        # 验收策展（仅列被 passCriteria 直接断言的）
  - http.auth.login.v1
  - event.session.created.v1
passCriteria:
  - text: OIDC 重定向完成
    mode: auto
    checkRef: journey.J-sso-login.oidc-redirect
  - text: Session 写入数据库
    mode: auto
    checkRef: journey.J-sso-login.session-db
  - text: 安全审查签核
    mode: manual
```

`cells` 是路由锚点，不是完备参与方集合。`contracts` 是验收策展，不是完整依赖图。需要完整依赖图时，从 `slice.contractUsages` 聚合。

### Status Board

运营层。不参与架构合法性判定。

```yaml
# journeys/status-board.yaml
- journeyId: J-sso-login
  state: doing
  risk: low
  blocker: ""
  updatedAt: 2026-04-04
```

`validate-meta` 对缺失条目仅发警告，不阻断 CI。Release 门禁是流水线策略，不是模型规则。

### Assembly

物理打包。手工编写只管 `id` / `cells` / `build`。边界信息由工具生成到独立文件。

```yaml
# assemblies/core-bundle/assembly.yaml（手工编写）
id: core-bundle
cells:
  - access-core
  - audit-core
  - config-core
build:
  entrypoint: cmd/core-bundle/main.go
  binary: core-bundle
  deployTemplate: k8s
```

```yaml
# assemblies/core-bundle/generated/boundary.yaml（工具生成，禁止手编）
generatedAt: "2026-04-04T10:30:00Z"
sourceFingerprint: "sha256:b5e6f7..."
exportedContracts:
  - http.auth.login.v1
importedContracts: []
smokeTargets:
  - smoke.access-core.startup
```

### Actor 注册表

非 Cell 的外部参与方。

```yaml
# actors.yaml
- id: edge-bff
  type: external
  maxConsistencyLevel: L1
```

`maxConsistencyLevel` 仅约束提供方角色，不限制消费方。

---

## 二、真相归属

每个关键事实只有一个 owner。

| 事实 | Owner | 不是 owner |
|------|-------|-----------|
| Slice 用了哪些契约 | `slice.contractUsages` | journey.contracts |
| 契约边界协议 | `contract.yaml` | slice 或 journey |
| 验收标准 | `journey.passCriteria` | slice.verify |
| Cell 归属 | `slice.belongsToCell`（或目录路径） | cell.yaml 反向索引 |
| 交付状态 | `journeys/status-board.yaml` | 任何其他元数据文件 |
| Assembly 边界 | `assemblies/*/generated/boundary.yaml` | assembly.yaml 内联 |

---

## 三、依赖模型

只有两类边：

### 契约依赖

`slice.contractUsages` 声明。由 `validate-meta` 校验引用存在性和角色合法性。

### 显式非契约依赖

`cell.l0Dependencies` 声明 L0 导入关系。由 `validate-meta` 校验目标存在且在同 assembly。

其他非契约耦合（共享进程状态、运行时配置注入等）当前不可建模。`select-targets` 因此是 **advisory** 级别。

---

## 四、验证保证

| 工具 | 保证级别 | 说明 |
|------|---------|------|
| `validate-meta` | **blocking** | 校验通过才允许合入。校验内容：引用存在性、拓扑合法性、格式合规。 |
| `select-targets` | **advisory** | 输出是优化建议，不是完整性证明。在非契约依赖图补齐前不可作为唯一门禁。 |
| `verify-slice` | **blocking** | 执行 slice 的 unit + contract 测试。 |
| `verify-cell` | **blocking** | 执行 Cell 的 smoke 测试。 |
| `run-journey` | **blocking** | 执行 journey 的 auto passCriteria。 |
| `generate-assembly` | **derived-only** | 产出 boundary.yaml 和索引，自身不做校验。 |

---

## 五、Waiver 模型

每个 `contractUsages` 条目必须有：
- 匹配的 `verify.contract` 标识符，**或**
- 一条 `verify.waivers` 条目

`waiver` 适用于提供方和消费方角色，但应优先使用可执行的 `verify.contract`。`waiver` 是临时豁免，不是常态配置。

```yaml
waivers:
  - contract: http.config.get.v1
    owner: platform-team         # 谁批准
    reason: 只读调用，集成测试已覆盖  # 为什么豁免
    expiresAt: 2026-06-01        # 什么时候过期
```

过期 waiver 等同于缺失。`validate-meta` 报错。

---

## 六、迁移附录（不污染核心模型）

### 从 V2.1 迁移

| V2.1 概念 | V3 处理 |
|-----------|---------|
| Assembly 内联 exported/imported/smoke | 迁移到 `assemblies/*/generated/boundary.yaml` |
| `producer` / `consumers` | 已替换为 kind-specific `endpoints` |
| `version` 字段 | 已删除，从 `id` 末段派生 |
| 隐式 L0 import | 迁移到显式 `l0Dependencies` |
| D-W1 warning 级覆盖率 | 替换为 C19 verify/waiver 闭环（error） |
| status-board 1:1 强制 | 降为 advisory warning |

### 现在不定死的

- Journey 是否拆成 spec / routing / plan 三文件
- L0 是否改名为 module / library-partition
- 非契约依赖图的最终形态
- 局部生成物命名与辅助索引文件约定
- 验证规则的精确编号体系

这些在工具实现过程中根据实际需要逐步确定。

---

## 附：从 V2.1 保留的校验规则（精简版）

> 完整编号体系在 `validate-meta` 实现时确定。此处仅列出不可删除的核心校验。

### 引用完整性

- `slice.belongsToCell` 指向已存在的 Cell
- `contractUsages[].contract` 指向已存在的契约
- `contract.ownerCell` 必须是 Cell，不是外部 actor
- `schemaRefs` 引用的文件必须存在
- `cell.id` / `slice.id` 必须等于目录名

### 拓扑合法性

- `contractUsages.role` 必须匹配契约 `kind` 的合法角色
- 提供方角色：`belongsToCell` == 契约提供方 actor
- 消费方角色：`belongsToCell` 在契约消费方列表中
- `contract.consistencyLevel` 不得超过提供方 actor 的一致性级别
- L0 Cell 不得出现在任何契约端点中
- 一个 Cell 最多属于一个 assembly

### Verify 闭环

- 每个 contractUsage 条目必须有 verify.contract 或 waiver（C19）
- verify 标识符必须遵循前缀分发格式
- L0 依赖必须在 `l0Dependencies` 中声明（C20）

### 格式合规

- `lifecycle` ∈ {draft, active, deprecated}
- `cell.type` ∈ {core, edge, support}
- 交付动态字段不得出现在非 status-board 文件中
- 已弃用契约不得被新引用（除非有 `migrations` 声明）
