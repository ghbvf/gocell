# GoCell 元数据模型 V2

## 设计目标

本文档定义了 GoCell 元数据模型的结构与拓扑规则。它是 V2 规范的一部分——运行时语义（如一致性等级的运行时行为）在配套文档中定义。本文档基于以下基本原则推导而来：

1. 每个事实恰好有一个**权威来源**。为了可读性，允许冗余锚点，前提是这些锚点可确定性地推导，且经工具验证保持一致（参见"derived-anchor"类别）。
2. 每个元数据字段恰好属于一个值类别：**canonical**、**derived-anchor**、**inherited**、**generated** 或 **delivery-only**（参见值解析模型）。
3. 动态交付状态不属于规范性元数据。
4. 契约结构属于版本化的 schema 文件，而非实现目录。
5. 该模型必须为以下六个工具提供充分的输入契约：
   - `validate-meta`
   - `generate-assembly`
   - `select-targets`
   - `verify-slice`
   - `verify-cell`
   - `run-journey`
6. 可推导的事实应由工具生成或验证，而非手动维护。

## 模型护栏

以下四条声明是模型的公理性约束。它们限定了所有后续规则和模板的解释边界。

### G1. 依赖图的权威性不是对称的

`slice.contractUsages` 是**实现层面的权威事实**，记录一个 slice 涉及了哪些契约。`journey.contracts` 是**验收层面的策展视图**——由人工挑选的、与 journey 通过标准相关的契约子集。两者并非并行的权威来源。任何需要完整依赖图的决策必须从 `slice.contractUsages` 聚合，绝不能从 `journey.contracts` 获取。

### G2. Journey 的语义角色有明确优先级

一个 journey 规范承载三种角色，具有不同的规范性强度：

| 角色 | 规范性强度 | 字段 |
|------|-----------|------|
| 验收规范 | **canonical** — 定义"完成"的含义 | `goal`、`passCriteria` |
| 路由锚点 | **best-effort** — 仅针对策展的契约进行验证（C13） | `cells` |
| 验收策展 | **curated view** — 人工挑选，非穷举 | `contracts` |

Journey 不是规范性的依赖来源。`cells` 是最佳的路由锚点，但并非经过证实的穷举集合。`contracts` 绝不能作为需要完整性的决策输入。

### G3. `consistencyLevel` 是粗粒度的能力标签，而非语义代理

`consistencyLevel`（L0-L4）表示 cell 或契约的**治理能力层级**——一种单轴全序，用于拓扑验证（C4、C5、C6、C7）。它不定义也不暗示：

- 交付语义（使用事件契约上的 `deliverySemantics`）
- 重放保证（使用事件/投影契约上的 `replayable`）
- 幂等性范围（使用事件契约上的 `idempotencyKey`）
- 运行时行为（定义在 `docs/architecture/consistency-levels.md` 中）

这些运行属性是契约或 slice 上的**独立字段**，不可从 `consistencyLevel` 推导。C6 的 kind 最低约束（如"event 要求 >= L2"）编码的是领域前置条件，而非语义定义。

### G4. `status-board` 是 delivery-only 制品——不构成架构门禁

`journeys/status-board.yaml` 是一个 **delivery-only** 制品。它追踪项目管理状态（进行中/阻塞/完成），而非架构正确性。

影响：

- `validate-meta` **不得**因 status-board 缺失或内容问题而在功能分支上阻断 CI。
- `validate-meta` **可以**在 journey 没有 status-board 条目时发出警告。
- 如需在发布分支上对 status-board 完整性进行门禁，这属于 **CI 流水线策略**，而非 `validate-meta` 的结构规则。

这意味着规则 D9 是一条 **CI 流水线建议**，而非模型层面的不变式。文档中应如此标注。

### G5. 在非契约依赖图补齐前，`select-targets` 为建议级别

`select-targets` 的输出是**建议性的（advisory）**——一种尽力而为的测试目标推荐。不得将其作为"未测试代码安全"的证明。当前模型仅追踪契约中介的依赖（`contractUsages`）和 L0 直接导入（`l0Dependencies`）。非契约耦合（共享进程内状态、运行时配置、基于约定的路由）对模型不可见。在 V3 引入显式非契约依赖图之前，`select-targets` 的结果应被视为优化指导，而非完整性保证。

## 规范性事实规则

### 规范字段名

使用以下名称作为规范字段集：

- `id`
- `owner`
- `schema.primary`
- `belongsToCell`
- `contractUsages`
- `endpoints`（kind 特定的子字段）
- `lifecycle`
- 结构化的 `verify`

### 禁用的遗留名称

不要在任何元数据中使用以下名称——包括手工编写和生成的文件：

- `cellId`
- `sliceId`
- `contractId`
- `assemblyId`
- `ownedSlices`
- `authoritativeData`
- `producer` / `consumers`（已被 kind 特定的 `endpoints` 替代）
- `callsContracts` / `publishes` / `consumes`（已被 `contractUsages` 替代）
- 契约上的 `status`（已被 `lifecycle` 替代）
- 契约上的 `version`（从 `id` 的最后一段推导）

### 动态状态与生命周期治理

元数据字段按可变性分为两类：

**交付动态字段**频繁变化（每个 sprint 或更频繁）。它们禁止出现在规范性元数据文件中，只属于 `journeys/status-board.yaml`：

- `readiness`
- `risk`
- `blocker`
- `done`
- `verified`
- `nextAction`
- `updatedAt`

**生命周期治理字段**以版本迁移为节奏变化（在整个契约生命周期中只变化少数几次）。它们允许出现在规范性元数据中：

- 契约上的 `lifecycle`（`draft` / `active` / `deprecated`）

### 规范性归属矩阵

每个事实恰好有一个规范性归属者。派生视图被显式标记。

| 事实 | 规范归属者 | 类别 | 备注 |
|------|-----------|------|------|
| 存在哪些 journey | `journeys/J-*.yaml` glob | canonical | catalog 是可选的生成索引 |
| Journey 的含义 | `journeys/*.yaml` | canonical | goal、owner、cells、contracts、passCriteria |
| Journey 焦点契约 | `journeys/*.yaml` 的 `contracts` | canonical | 策展子集，非穷举推导 |
| Slice 归属于哪个 cell | `slice.belongsToCell` | derived-anchor | 必须等于目录路径的 cell-id（B10） |
| Cell 一致性等级 | `cell.yaml` 的 `consistencyLevel` | canonical | slice 和契约受此约束 |
| Slice 一致性等级 | `cell.yaml`，可在 `slice.yaml` 中覆盖 | inherited | effective = 子级 ?? 父级 |
| Cell / slice 所有者 | `cell.yaml`，可在 `slice.yaml` 中覆盖 | inherited | effective = 子级 ?? 父级 |
| 契约 kind | `contract.kind` | derived-anchor | 必须等于 `id` 的第一段（D7） |
| 契约版本 | 从 `contract.id` 最后一段推导 | derived-anchor | 必须与目录路径一致（B7） |
| 契约边界 | `contract.yaml` kind 特定的 `endpoints` | canonical | Slice 通过 `contractUsages` 引用 |
| Slice 使用了哪些契约 | `slice.yaml` 的 `contractUsages` | canonical | 实现层面的映射 |
| Slice 文件归属 | `slice.allowedFiles` 或约定默认值 | canonical | 默认：`cells/{cell-id}/slices/{slice-id}/**`；始终具有权威性 |
| 哪些 cell 被打包在一起 | `assembly.yaml` 的 `cells` | canonical | 仅用于打包 |
| Assembly 边界契约 | 由 cells + contracts **生成** | generated | 允许手动策展作为过渡覆盖 |
| Assembly 冒烟测试目标 | 由 `cell.verify.smoke` **生成** | generated | 允许手动策展作为过渡覆盖 |
| 动态交付状态 | 仅限 `journeys/status-board.yaml` | delivery-only | 禁止出现在所有其他元数据文件中 |

### 值解析模型

每个元数据字段恰好属于五个类别之一。验证规则基于**有效值**运作。

| 类别 | 定义 | 解析规则 |
|------|------|---------|
| **canonical** | 手工编写的唯一事实来源，没有其他来源可以替代 | 直接使用声明值 |
| **derived-anchor** | 值可从另一个 canonical 字段确定性计算得出；可选——缺省时由工具推导，声明时由工具验证声明值 == 计算值 | 所有 derived-anchor 字段使用**可选带验证**模式：省略则由工具推导，声明则显式标注（工具检查一致性） |
| **inherited** | 值继承自父层的 canonical 字段；声明可覆盖但受约束 | effective = 子级声明值 ?? 父级值 |
| **generated** | 由工具生成，稳态下不手工编写 | 由工具推导；允许手动策展作为过渡覆盖 |
| **delivery-only** | 仅存在于 `journeys/status-board.yaml` | 不受架构拓扑验证或影响路由约束。枚举有效性规则（A9-A11）和格式规则（D8）仍然适用。根据护栏 G4，status-board 完整性是 CI 流水线策略，而非模型不变式。 |

**关于 `allowedFiles` 的说明**：`allowedFiles` 被分类为 **canonical**（而非 derived-anchor）。缺省时，约定默认值 `cells/{cell-id}/slices/{slice-id}/**` 作为初始 canonical 值。声明时，声明值即为 canonical 值。两种情况下，有效值均具有权威性——不存在"计算值 vs 声明值"的验证。这保持了"每个字段恰好属于一个值类别"的原则：`allowedFiles` 始终是 canonical，约定仅提供合理的默认值。

推论：

- `cell.yaml` 不应手动维护 `slices`、`journeys` 或 `contracts` 列表。
- 如果这些汇总信息有用，应将其生成到注册表或派生视图中。
- `journeys/catalog.yaml` 是可选的生成制品。Journey 的发现通过 `journeys/J-*.yaml` glob 实现。

## 三层模型

| 层 | 文件 | 规范职责 |
|----|------|---------|
| 1 | `journeys/*.yaml` | 单个 journey 的验收规范 |
| 2 | `cells/*/cell.yaml` | 治理分区：运行时边界（L1+）或计算分区（L0） |
| 3 | `cells/*/slices/*/slice.yaml` | 工作映射与影响路由 |

跨层资产（不作为编号层）：

- **契约**（`contracts/**/contract.yaml`）：跨 cell 的边界定义，被第 1-3 层引用。
- **Assembly**（`assemblies/*/assembly.yaml`）：cell 的物理打包，引用第 2 层实体。
- **Actor 注册表**（`actors.yaml`）：参与契约的非 cell 运行时角色。
- **Status Board**（`journeys/status-board.yaml`）：journey 交付状态投影（delivery-only）。不是结构分解层——它与三层层级正交。

所有跨 cell 交互都需要契约，有一个例外：L0 cell（计算分区）可以被同一 assembly 内的兄弟 cell 直接导入，无需契约（参见 L0 Cell 交互模型）。对于所有其他 cell（L1+），参见契约章节中的"何时建模轻量级契约"以获取降低开销的指导。

## 第 1 层：journeys/*.yaml

旅程规范是一个稳定的验收规格说明。它定义了端到端用户场景的"完成"标准。

```yaml
id: J-sso-login
goal: user completes SSO login and receives valid session
owner:
  team: platform
  role: journey-owner
primaryActor: end-user
cells:
  - access-core
  - audit-core
  - config-core
fixtures:
  - fixture-oidc-provider
  - fixture-user-basic
contracts:
  - http.auth.login.v1
  - event.session.created.v1
passCriteria:
  - text: OIDC redirect completes
    mode: auto
    checkRef: journey.J-sso-login.oidc-redirect
    assert: { type: httpStatus, expect: 302 }
  - text: callback token exchanged
    mode: auto
    checkRef: journey.J-sso-login.token-exchange
  - text: session created in DB
    mode: auto
    checkRef: journey.J-sso-login.session-db
    assert: { type: rowExists, table: sessions, key: session_id }
  - text: JWT cookie set
    mode: auto
    checkRef: journey.J-sso-login.jwt-cookie
  - text: user info accessible via /me
    mode: auto
    checkRef: journey.J-sso-login.auth-me
    assert: { type: httpStatus, expect: 200 }
```

`contracts` 是一个**精选的聚焦子集**——而非穷举推导。纳入标准：当且仅当某个 contract 被 `passCriteria` 条目**直接断言**时（即该 contract 被本旅程中至少一个 auto-check 所行使），才将其列入。路由完整性由 `journey.cells` 在尽力而为的基础上提供（规则 C13 将 cells 与精选 contracts 列表进行校验，而非与完整的 contract 全集进行校验）。任何需要完整 contract 列表的决策都必须使用 `slice.contractUsages` 聚合，而非 `journey.contracts`。

`journey.contracts` 与 `slice.contractUsages` 回答的是不同的问题，二者并非冗余：`contractUsages` 记录某个 slice 实现层面所涉及的 contract（实现级别）；`journey.contracts` 记录某个旅程验收标准所行使的 contract（验收级别）。两者互不派生。

**设计权衡——多角色对象**：旅程规范有意承担三种角色：验收规格说明（`goal` + `passCriteria`）、测试计划（`checkRef` + `fixtures`）以及路由锚点（`cells` 用于 `select-targets`）。这种耦合以语义纯粹性为代价，简化了治理（一个文件、一个负责人）。特别是，`journey.cells` 是当前最佳的路由锚点，但并非数学证明的穷举集——它仅针对精选 contracts 列表进行校验（C13），而契约推断模式是一种尽力而为的优化，并非精确的依赖图。

### passCriteria 字段说明

| 字段 | 条件 | 描述 |
|------|------|------|
| `text` | 必填 | 人类可读的验收标准 |
| `mode` | 必填 | `auto`（由 `run-journey` 校验）或 `manual`（人工签核） |
| `checkRef` | `mode: auto` 时必填 | 逻辑标识符，解析至可执行检查（参见 verify 命名约定） |
| `assert` | 可选 | 结构化断言提示：`{ type, expect, ... }` |

`run-journey` 通过 `checkRef` 目标执行所有 `auto` 标准，并将 `manual` 标准作为待办清单输出供人工审查。

### 必填字段

- `id`
- `goal`
- `owner`
- `cells`
- `contracts`
- `passCriteria`

### 推荐可选字段

- `primaryActor`
- `fixtures`

### Fixture 约定

Fixture 为 `run-journey` 准备测试环境。旅程规范中的每个 fixture id 必须对应一个 fixture 定义文件。

**文件位置**：`fixtures/{fixture-id}.yaml`

**结构**：

```yaml
id: fixture-oidc-provider
type: service-mock
description: mock OIDC provider for SSO login testing
setup:
  image: ghcr.io/gocell/mock-oidc:latest
  env:
    ISSUER_URL: http://localhost:9090
    CLIENT_ID: test-client
teardown: automatic
```

**字段说明**：

| 字段 | 是否必填 | 描述 |
|------|----------|------|
| `id` | 是 | 必须与旅程规范中引用的 fixture id 一致 |
| `type` | 是 | `service-mock`（容器）、`seed-data`（数据库种子数据）、`config-override`（运行时配置） |
| `description` | 否 | 人类可读的用途说明 |
| `setup` | 是 | 特定类型的配置 |
| `teardown` | 否 | `automatic`（默认）或 `manual` |

**加载顺序**：`run-journey` 在执行 `passCriteria` 之前按声明顺序加载 fixture。设置为 `teardown: automatic` 的 fixture 在所属旅程运行完成后（所有 `passCriteria` 评估完毕）清理，无论结果是通过还是失败。对于 `teardown: manual`，清理延迟至运维人员手动处理。

### Fixture 隔离模型

- **作用域**：每次旅程运行独立。每次旅程运行获得自己的 fixture 实例。跨旅程共享的 fixture id 会导致独立的 setup/teardown 周期。
- **seed-data 隔离**：`seed-data` fixture 必须使用唯一键前缀或专用测试 schema。约定：包含 `namespace` 字段，在运行时注入：

```yaml
id: fixture-user-basic
type: seed-data
setup:
  table: users
  data: [...]
  namespace: "{{runId}}"
teardown: automatic
```

| 类型 | namespace 行为 |
|------|---------------|
| `service-mock` | 容器名附加 namespace 后缀；端口隔离 |
| `seed-data` | 数据键添加 namespace 前缀 |
| `config-override` | 不需要（override 在进程内部生效） |

`namespace` 是可选的。缺省时不做隔离（适用于串行单旅程执行）。

**校验**：`validate-meta` 检查旅程规范中引用的每个 fixture 是否存在对应的 `fixtures/{fixture-id}.yaml` 文件（规则 B9）。

## journeys/status-board.yaml（仅用于交付）

```yaml
- journeyId: J-sso-login
  state: doing
  risk: low
  blocker: ""
  updatedAt: 2026-04-04
  targetDate: 2026-04-18
  evidenceRefs:
    - tests/journey/J-sso-login.log

- journeyId: J-audit-login-trail
  state: todo
  risk: medium
  blocker: waiting for audit-core scaffolding
  updatedAt: 2026-04-02
  evidenceRefs: []
```

### 必填字段

- `journeyId` — 必须引用已存在的旅程；在 status-board 中必须唯一（每个旅程一条记录）。根据护栏 G4，缺失条目仅触发警告（参见 D9）。
- `state` — `draft` | `todo` | `doing` | `blocked` | `done`
- `risk` — `low` | `medium` | `high`
- `blocker` — 必填字符串；无阻塞时为 `""`（null 或省略无效）
- `updatedAt` — ISO 日期，每次条目变更时更新
- `evidenceRefs`

### 推荐可选字段

- `targetDate`
- `updatedBy`

`updatedAt` 是 ISO 日期，每次条目变更时更新。工具可以自动写入。处于 `doing` 状态但 `updatedAt` 过期的条目是需要审查的信号。

## 第 2 层：cell.yaml

`cell.yaml` 拥有治理分区事实——L1+ Cell 的运行时边界和数据主权，L0 Cell 的计算分区元数据。它不维护 slice、journey 或 contract 的反向索引。

**目录约定**：Cell 目录名必须等于 `cell.id`。例如，`cell.id: access-core` 位于 `cells/access-core/cell.yaml`。此约定被 verify 解析和 `select-targets` 使用——无需额外的目录字段。

```yaml
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
noSplitReason:
  - session creation and identity verification share one transaction boundary
```

### 必填字段

- `id`
- `type` — `core` | `edge` | `support`
- `consistencyLevel`
- `owner` — `{ team, role }`
- `schema.primary` — L1+ Cell 必填（数据主权要求 schema 所有权）；L0 Cell 可选（计算分区不拥有数据）
- `verify.smoke`

### L0 Cell 交互模型

**设计权衡**：L0 是对 Cell 边界概念的有意放宽。L1+ 的 Cell 是具有数据主权和契约中介通信的运行时边界。L0 Cell 是**计算分区**——它们保留 Cell 级别的治理（所有权、冒烟测试、`select-targets` 路由），但不具备运行时隔离。这种务实的扩展以架构纯粹性为代价，降低了纯计算代码的契约开销。

L0 Cell（`consistencyLevel: L0`）是**纯计算库**，直接在 assembly 二进制中编译和链接。它们通过 Go 接口暴露功能，并由同一 assembly 中的兄弟 Cell 在进程内调用——无需契约。

- L0 Cell **可以**被同一 assembly 中的其他 Cell 直接导入（这是 L1+ Cell 的契约要求的例外）。
- L0 Cell **不得**持有可变状态、作为独立进程运行或出现在任何契约端点字段中（由 C7 强制执行）。
- L0 Cell 与 `pkg/` 的区别在于：它们拥有 Cell 级别的元数据（`cell.yaml`），参与所有权追踪，并受 `verify.smoke` 约束。

**显式依赖声明**：导入 L0 Cell 的 Cell 必须在其 `cell.yaml` 中声明依赖：

```yaml
# 导入方 cell.yaml
l0Dependencies:
  - cell: shared-crypto
    reason: 确定性哈希工具
```

`l0Dependencies` 在 Cell 导入任何 L0 Cell 时为必填。`validate-meta` 检查每个引用的 L0 Cell 是否存在、`consistencyLevel` 是否为 L0、且是否在同一 assembly 中。`select-targets` 使用 `l0Dependencies` 将 L0 Cell 的变更路由到依赖方 Cell。

### 推荐可选字段

- `noSplitReason`
- `schema.tables`

### 扩展示例

```yaml
allowedDependencies:
  - config-core
servedRoles:
  - end-user
stakeholders:
  - security
```

扩展不属于最小稳定模型的一部分。它们可能对特定团队或工具有用。

## 第 3 层：slice.yaml

`slice.yaml` 是工作映射和实现级别契约使用的权威来源。

**目录约定**：Slice 目录名必须等于 `slice.id`。例如，`slice.id: session-login` 位于 `cells/{cell-id}/slices/session-login/`。此约定被 verify 解析和 `select-targets` 路由使用。

```yaml
id: session-login
belongsToCell: access-core
owner:
  team: platform
  role: slice-owner
consistencyLevel: L2
contractUsages:
  - contract: http.auth.login.v1
    role: serve
  - contract: http.config.get.v1
    role: call
  - contract: event.session.created.v1
    role: publish
traceAttrs:
  extra:
    - session_id
    - user_id
verify:
  unit:
    - unit.session-login.service
  contract:
    - contract.http.auth.login.v1.serve
    - contract.event.session.created.v1.publish
allowedFiles:
  - cells/access-core/slices/session-login/**
```

### 必填字段

- `id`
- `contractUsages` — `{ contract, role }` 条目列表（对于没有跨 Cell 交互的 L0/L1 slice 可以为 `[]`）
- `verify.unit`
- `verify.contract` — 列表（对于没有契约使用的 L0/L1 slice 可以为 `[]`）
- `verify.waivers` — 可选列表，为 `contractUsages` 中未被 `verify.contract` 覆盖的条目提供显式豁免：

```yaml
verify:
  unit:
    - unit.session-login.service
  contract:
    - contract.http.auth.login.v1.serve
  waivers:
    - contract: http.config.get.v1
      owner: platform-team
      reason: 只读配置调用，已通过集成测试套件覆盖
      expiresAt: 2026-06-01
```

**Verify/waiver 闭环**：对于每个提供方角色的 `contractUsages` 条目，slice 必须有匹配的 `verify.contract` 标识符**或**该契约的 `verify.waivers` 条目。此规则由 C19 强制执行（错误级别，非警告）。Waiver 必须包含 `owner`、`reason` 和 `expiresAt`。过期的 waiver（超过 `expiresAt`）视为缺失——C19 报错。

### 可选字段（派生锚点）

- `belongsToCell` — 缺省时从目录路径 `cells/{cell-id}/slices/...` 派生。声明时必须等于派生值（B10）。保留用于提高可读性。

### 可选字段（从 Cell 继承）

- `owner` — 缺省时继承 `cell.owner`
- `consistencyLevel` — 缺省时继承 `cell.consistencyLevel`；声明时不得超过 Cell 的值

### 可选字段（约定默认值）

- `allowedFiles`（带约定默认值的规范字段）— 缺省时根据目录约定默认为 `cells/{cell-id}/slices/{slice-id}/**`。仅当 slice 拥有其约定目录之外的文件时（例如共享 proto 文件、跨目录生成代码）才需显式声明。声明时，声明值替换（而非扩展）约定默认值。

### 可选字段（迁移）

- `migrations` — 该 slice 正在执行的活跃契约版本迁移列表：

```yaml
migrations:
  - contract: http.auth.login.v1
    target: http.auth.login.v2
    deadline: 2026-05-01
```

声明时，C17 允许引用已弃用的源契约。`validate-meta` 检查 `target` 是否存在且其 `lifecycle: active`。

### 推荐可选字段

- `traceAttrs.extra` — 超出平台信封的领域特定追踪属性

### contractUsages 角色到类型映射

契约端点字段使用**身份名词**（server、clients——谁参与），而 `contractUsages.role` 使用**行为动词**（serve、call——slice 做什么）。两套词汇从不同视角描述同一概念；映射是固定的、一对一的。

每个 `contractUsages` 条目包含一个 `contract`（引用现有契约的 id）和一个 `role`。有效组合：

| 类型 | 提供方角色 | 消费方角色 |
|------|-----------|-----------|
| `http` | `serve` | `call` |
| `event` | `publish` | `subscribe` |
| `command` | `handle` | `invoke` |
| `projection` | `provide` | `read` |

校验规则：

- 提供方角色 → `slice.belongsToCell` 必须等于契约提供方端点的 actor。
- 消费方角色 → `slice.belongsToCell` 必须出现在契约消费方端点列表中（或列表为 `["*"]`）。

### Verify 命名约定

Verify 标识符使用**前缀分发**格式。第一个以点分隔的段决定类别；其余段为类别特定内容。段数不固定。

| 类别 | 格式 | 示例 | 解析方式 |
|------|------|------|----------|
| `smoke` | `smoke.{cell-id}.{name}` | `smoke.access-core.startup` | `go test ./cells/access-core/... -run TestSmoke_access_core_startup` |
| `unit` | `unit.{slice-id}.{scope}` | `unit.session-login.service` | `go test ./cells/access-core/slices/session-login/... -run TestUnit_session_login_service` |
| `contract` | `contract.{contract-id}.{role}` | `contract.http.auth.login.v1.serve` | `go test ./contracts/http/auth/login/v1/... -run TestContract_http_auth_login_v1_serve` |
| `journey` | `journey.{journey-id}.{step}` | `journey.J-sso-login.oidc-redirect` | `go test ./tests/journey/... -run TestJourney_J_sso_login_oidc_redirect` |

**标准化规则**（用于生成 Go 测试函数名和路径）：

- 类别前缀被剥离；**所有剩余段**构成作用域——不做截断。
- 对于 `contract` 类别，`{contract-id}` 和 `{role}` 之间的边界由版本段决定：`v{N}` 段（匹配 `v\d+`）始终是 contract-id 的最后一段，紧随其后的段是 `{role}`。无需全局状态或契约注册表查找。
- 标识符中的连字符（`-`）在 Go 测试名中转换为下划线（`_`）。
- 标识符中的点（`.`）在 Go 测试名中转换为下划线（`_`）。
- 测试函数名为 `Test{Category}_{all_remaining_segments_normalized}`（例如 `contract.http.auth.login.v1.serve` → `TestContract_http_auth_login_v1_serve`）。

**Contract 类别解析**：对于 `contract.{contract-id}.{role}`，解析过程是确定性的，无需全局状态。按 `.` 分割，然后：最后一段必须是已知角色（`serve|call|publish|subscribe|handle|invoke|provide|read`）；倒数第二段必须匹配 `v\d+`（版本终止符）；`contract` 前缀和角色之间的所有内容构成 `{contract-id}`。示例：`contract.http.auth.login.v1.serve` → contract-id = `http.auth.login.v1`，role = `serve`。

**路径解析**：

- `smoke` 和 `unit`：Cell 目录为 `cells/{cell-id}/`，从 `slice.belongsToCell` 派生。
- `contract`：目录为 `contracts/{kind}/{domain-path}/{version}/`，从 contract id 解析（第一段 = kind，最后一段 = version，中间段 = 目录路径）。
- `journey`：目录始终为 `tests/journey/`。

平台追踪信封字段（`traceId`、`journeyId`、`callerCellId`、`calleeCellId`）是运行时标准，不属于 slice 级别的元数据。`traceAttrs` 应仅描述额外的领域属性。

## 契约模型

契约定义跨 Cell 边界的协议。L1+ Cell 之间的每一次跨 Cell 交互都需要一份契约。L0 Cell（计算分区）除外——它们在同一个 assembly 内通过直接 import 交互（参见 L0 Cell 交互模型）。

### 通用必填字段

所有契约类型共享以下必填字段：

- `id` — 格式：`{kind}.{domain-path}.v{N}`，其中 `{domain-path}` 是一个或多个以点分隔的片段（例如 `auth.login`、`device.enqueue`）。解析规则：第一个片段 = `kind`，最后一个片段 = `v{N}` 版本号，中间所有片段 = 领域路径。领域路径映射到 `contracts/{kind}/` 下的目录结构。版本号从 `id` 的最后一个片段派生——没有单独的 `version` 字段。
- `kind` — `http` | `event` | `command` | `projection`（派生锚点，可选——缺省时从 `id` 的第一个片段派生；声明时必须与之相等。由 D7 规则校验。保留此字段是为了可读性。）
- `ownerCell` — 负责契约生命周期的 Cell（治理所有权）。继承规则：缺省时默认为提供方端点的 actor（如果它是一个 Cell）；声明时必须引用一个 `cell.id`，而非外部 actor。仅在治理所有者与提供方不同时才需要声明。
- `consistencyLevel` — 取自全序集合 `L0 < L1 < L2 < L3 < L4` 的值。校验规则（C4、C5、C6、C7）中的所有比较运算符均使用此排序。每个级别的运行时语义（写入确认、幂等边界、重放保证）定义在 `docs/architecture/consistency-levels.md` 中；本文档仅定义结构和拓扑约束。
- `lifecycle` — `draft` | `active` | `deprecated`
- `schemaRefs` — 相对于契约目录的路径
- `endpoints` — 按类型定义（见下文）

### 通用推荐字段

- `summary`
- `semantics` — 每种类型的推荐键：

| Kind | 推荐键 | 含义 |
|------|----------------|---------|
| `http` | `semantics.operation` | 此端点执行的操作 |
| `event` | `semantics.fact` | 此事件声明已发生的事实 |
| `command` | `semantics.action` | 此命令请求执行的动作 |
| `projection` | `semantics.view` | 此投影呈现的视图 |

### 按类型定义的端点

每种契约类型恰好定义两个端点字段，方向明确：

| Kind | 提供方字段 | 消费方字段 | 提供方含义 | 消费方含义 |
|------|--------------------|--------------------|------------------|----------------|
| `http` | `endpoints.server` | `endpoints.clients` | 提供该端点服务 | 调用该端点 |
| `event` | `endpoints.publisher` | `endpoints.subscribers` | 发出事件 | 接收事件 |
| `command` | `endpoints.handler` | `endpoints.invokers` | 处理命令 | 发送命令 |
| `projection` | `endpoints.provider` | `endpoints.readers` | 物化视图 | 查询视图 |

规则：

- 提供方字段始终是**单个 actor**（cell id 或 external actor id）。
- 消费方字段是**actor 列表**，或 `["*"]` 表示开放/公开契约。
- `["*"]` 表示任何已注册的 actor 均可消费。`validate-meta` 会跳过消费方成员资格检查，但对于任何引用该契约的 slice 仍然要求 actor 已注册。
- `["*"]` 不得与具名 actor 混合在同一个列表中。
- `ownerCell` 是治理所有权（谁拥有契约的生命周期）。它必须是一个 Cell，而非外部 actor。`ownerCell` 承担三项具体职责：**(1)** 版本演进决策（何时弃用、何时发布 v2），**(2)** Schema 兼容性审批，**(3)** 破坏性变更的迁移协调。`ownerCell` 不承担运行时职责——运行时保证属于提供方 actor 的职责范围。它通常与提供方 actor 相同，但并非总是如此（例如，一个 Cell 可能拥有某个契约的生命周期，而该契约的提供方是一个外部网关）。当 ownerCell 等于提供方时（大多数情况），这种重复是有意为之的：治理和运行时是不同的语义角色，即使被分配给同一个 actor。当 ownerCell 不等于提供方时，默认值为提供方（参见继承的 ownerCell）。

### 生命周期值

| 值 | 含义 |
|-------|---------|
| `draft` | 契约已定义但尚未承载流量。消费方端点列表可以为 `[]`。 |
| `active` | 契约已上线。至少需要一个消费方（或 `["*"]`）。 |
| `deprecated` | 契约计划移除。消费方应进行迁移。 |

**状态转换**是单向的：`draft → active → deprecated`。不允许从 `deprecated` 回退到 `active` 或从 `active` 回退到 `draft`——应创建新版本。`deprecated` 契约在删除前的保留期限是团队策略决定，不由 `validate-meta` 强制执行。

### 契约默认值

除非在单个契约中覆盖：

- `compatibilityPolicy`: `{ breaking: [remove_field, change_field_semantics], nonBreaking: [add_optional_field] }`
- `traceRequired`: `true`

仅在偏离默认值时才需要在契约中声明这些字段。

### HTTP 契约

```yaml
id: http.auth.login.v1
kind: http
ownerCell: access-core
consistencyLevel: L1
lifecycle: active
summary: authenticate user and create login session
endpoints:
  server: access-core
  clients:
    - edge-bff
schemaRefs:
  request: request.schema.json
  response: response.schema.json
```

### 事件契约

```yaml
id: event.session.created.v1
kind: event
ownerCell: access-core
consistencyLevel: L2
lifecycle: active
summary: session creation finalized and visible to downstream consumers
endpoints:
  publisher: access-core
  subscribers:
    - audit-core
    - config-core
schemaRefs:
  payload: payload.schema.json
  headers: headers.schema.json
semantics:
  fact: session creation completed
replayable: true
idempotencyKey: eventId
deliverySemantics: at-least-once
orderingSemantics: aggregateId+sequence
```

`event` 类型额外必填字段：

- `replayable`
- `idempotencyKey`
- `deliverySemantics` — `at-least-once` | `exactly-once` | `at-most-once`

`event` 类型额外推荐字段：

- `orderingSemantics`

### 命令契约

```yaml
id: command.device.enqueue.v1
kind: command
ownerCell: device-command-core
consistencyLevel: L2
lifecycle: active
summary: request command execution on target device
endpoints:
  handler: device-command-core
  invokers:
    - edge-bff
schemaRefs:
  request: request.schema.json
  ack: ack.schema.json
  result: result.schema.json
semantics:
  action: enqueue device command
```

注意：`endpoints.handler: device-command-core` 是处理命令的 Cell，而 `endpoints.invokers` 列出调用方。在此示例中 `ownerCell` 与 handler 相同。

### 投影契约

```yaml
id: projection.audit.timeline.v1
kind: projection
ownerCell: audit-core
consistencyLevel: L3
lifecycle: active
summary: read-only audit timeline view
endpoints:
  provider: audit-core
  readers:
    - edge-bff
schemaRefs:
  projection: projection.schema.json
replayable: true
```

`projection` 类型额外必填字段：

- `replayable`

### Schema 存放位置

跨边界 schema 属于契约版本目录，而非 Cell 实现目录。目录片段使用与 `contract.kind` 匹配的**单数形式**：

```
contracts/{kind}/{domain-path...}/{version}/
```

`{domain-path}` 片段与 `contract.id` 的中间片段（位于 `kind` 和 `v{N}` 之间）匹配，点替换为目录分隔符。

```text
contracts/http/auth/login/v1/
  contract.yaml
  request.schema.json
  response.schema.json
  examples/

contracts/event/session/created/v1/
  contract.yaml
  payload.schema.json
  headers.schema.json
  examples/

contracts/command/device/enqueue/v1/
  contract.yaml
  request.schema.json
  ack.schema.json
  result.schema.json

contracts/projection/audit/timeline/v1/
  contract.yaml
  projection.schema.json
```

### schemaRefs 路径解析

`schemaRefs` 的值**相对于 contract.yaml 所在目录**进行解析。仅允许裸文件名。禁止使用绝对路径或根相对路径。

示例：在 `contracts/http/auth/login/v1/contract.yaml` 中，`schemaRefs.request: request.schema.json` 指向 `contracts/http/auth/login/v1/request.schema.json`。

### 何时建模轻量级契约

所有跨 Cell 交互都需要契约。对于同一 assembly 内低一致性 Cell 之间简单、稳定的交互，使用轻量级契约以降低开销。

适合使用轻量级契约的指标：

- 两个 Cell 在同一个 assembly 中。
- 交互是具有稳定签名的简单同步调用。
- 不会有外部消费方需要此接口。

轻量级契约仍然是一个带有完整校验的 `contract.yaml`——只是 schema 最小化（例如，一个请求/响应 JSON schema），并且可以从 `lifecycle: draft` 开始。

需要详细 schema 和多个消费方的完整契约的指标：

- 交互跨越 assembly 边界。
- 任一 Cell 的 `consistencyLevel` > L1。
- 交互将有外部消费方。
- 交互被任何 journey 的 `passCriteria` 直接断言。

## assembly.yaml

`assembly.yaml` 管理物理打包和构建配置。

一个仓库可以包含**多个 assembly**。每个 assembly 位于 `assemblies/{assembly-id}/assembly.yaml`。规则 C12 确保没有 Cell 属于多个 assembly。

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
killSwitches:
  - kill.audit-consumer
```

```yaml
# assemblies/core-bundle/generated/boundary.yaml（工具生成，禁止手动编辑）
generatedAt: "2026-04-04T10:30:00Z"
sourceFingerprint: "sha256:b5e6f7..."
exportedContracts:
  - http.auth.login.v1
  - http.auth.me.v1
  - http.config.get.v1
importedContracts: []
smokeTargets:
  - smoke.access-core.startup
  - smoke.audit-core.startup
  - smoke.config-core.startup
```

### 必填字段

- `id`
- `cells`
- `build` — `{ entrypoint, binary, deployTemplate }`

### 可选生成字段

这些字段为**仅生成**。由 `generate-assembly` 从 `assembly.cells` + 契约端点声明中派生，写入 `assemblies/{assembly-id}/generated/boundary.yaml`（不内联在 `assembly.yaml` 中）。不再允许手动维护——生成内容存放在专用的生成文件中，绝不与手工编写的元数据混合。

`validate-meta` 读取 `boundary.yaml` 进行 C11 完整性检查。如果 `boundary.yaml` 缺失或过期（`sourceFingerprint` 不匹配），`validate-meta` 在 CI 中发出警告，在 release 分支上报错。

- `exportedContracts` — 提供方在 assembly 内部且至少一个消费方在外部的契约
- `importedContracts` — 提供方在 assembly 外部且至少一个消费方在内部的契约
- `smokeTargets` — 聚合所有成员 Cell 的 `verify.smoke`

### 推荐可选字段

- `killSwitches`
- `flags`

工具特定的生成器配置和生成产物元数据：

```yaml
generated:
  outputDir: assemblies/core-bundle/generated
  generatedAt: "2026-04-04T10:30:00Z"
  sourceFingerprint: "sha256:b5e6f7..."
```

当 `exportedContracts`、`importedContracts` 或 `smokeTargets` 由工具生成（而非手动维护）时，其新鲜度元数据（`generatedAt` + `sourceFingerprint`）存储在 `generated` 块中。`validate-meta` 根据当前源文件检查 `sourceFingerprint`（规则 D10）。

## Actor 注册表：actors.yaml

契约端点字段引用运行时 actor。一个 actor 是以下两者之一：

1. 通过 `cells/*/cell.yaml` 声明的 Cell。
2. 在仓库根目录的 `actors.yaml` 中声明的外部 actor。

外部 actor 是参与契约的 Cell 模型之外的系统（例如 BFF 网关、第三方服务）。

```yaml
- id: edge-bff
  type: external
  maxConsistencyLevel: L1
  description: API gateway / BFF layer, not managed as a cell
```

### 每个 Actor 的必填字段

- `id` — 不得与任何 `cell.id` 冲突
- `type` — `external`
- `maxConsistencyLevel` — 此 actor 作为**提供方**（即提供方端点）所能支持的最高一致性级别。这不限制消费方的参与——一个 L1 actor 可以在任何级别消费/读取契约，因为一致性级别描述的是写入保证，而非消费能力。

### 推荐可选字段

- `description`

当**提供方 actor** 是外部 actor 时，校验规则 C4 使用 `actor.maxConsistencyLevel`。`ownerCell` 始终是一个 Cell，因此其 `consistencyLevel` 始终直接可用。

## journeys/catalog.yaml（可选生成文件）

`catalog.yaml` 不是规范数据源。Journey 的发现通过 `journeys/J-*.yaml` glob 模式进行。

如果团队需要一个可浏览的索引，工具（例如 `gocell journey list`）可以通过聚合 journey 规约和状态看板中的 `id / goal / owner / state / risk` 来生成。生成的文件不应手动编辑。

## verify-cell

`verify-cell` 执行 Cell 级别的冒烟验证。

- **输入**：`cell.yaml` 中 `verify.smoke` 标识符。
- **行为**：通过 verify 命名约定解析每个冒烟标识符，并执行对应的 `go test` 命令。
- **输出**：Cell 级别的冒烟通过/失败结果。
- **与其他工具的关系**：`verify-cell` 运行冒烟测试（Cell 边界健康检查）；`verify-slice` 运行单元测试 + 契约测试（slice 实现正确性）。`generate-assembly` 从成员 Cell 聚合冒烟目标以进行 assembly 级别的验证。

## select-targets 影响路由

**保证级别：建议性（advisory）。** `select-targets` 确定哪些 slice、Cell 和 journey *可能*受一组变更文件的影响。其输出是一种尽力而为的推荐，而非经过证明的完整目标集。在显式非契约依赖图存在之前（参见 V3 路线图），`select-targets` 的结果不得作为"可安全跳过测试"的唯一门禁。团队应将其输出视为优化指导，而非无影响的证明。

### 路由矩阵

| 变更文件模式 | 路由逻辑 | 影响范围 |
|---------------------|---------------|--------------|
| `cells/{cell}/slices/{slice}/**` | 匹配 `slice.allowedFiles`（或约定默认值 `cells/{cell-id}/slices/{slice-id}/**`）→ slice → cell → journeys | slice + cell + journey |
| `cells/{cell}/cell.yaml` | Cell → 其所有 slice → journeys | cell 全量 |
| `cells/{cell}/**`（不匹配上述 slice 或 cell.yaml 模式） | Cell 级共享代码 → cell → 其所有 slice → journeys | cell 全量 |
| `contracts/**/contract.yaml` | 查找所有 `contractUsages` 引用此契约的 slice → 标准链路 | slice + cell + journey |
| `contracts/**/*.schema.json` | 父契约 → 与 `contract.yaml` 变更相同 | slice + cell + journey |
| `journeys/J-*.yaml` | journey 本身 + 其 `cells` 列表 → 所有成员 Cell | journey + cells |
| `journeys/status-board.yaml` | 无路由（仅交付状态） | 无 |
| `assemblies/*/assembly.yaml`（`cells` 变更） | 所有成员 Cell → 其 slice → journeys | assembly 全量 |
| `assemblies/*/assembly.yaml`（`build` 变更） | 仅 assembly 构建 + smokeTargets | assembly 冒烟 |
| `actors.yaml` | 查找所有引用已变更 actor 的契约 → 契约路由链路 | 按契约 |
| `fixtures/{fixture-id}.yaml` | 查找所有 `fixtures` 列表包含此 fixture id 的 journey → journey + 其 `cells` | journey + cells |
| 其他任意路径 | 与所有 slice 的有效 `allowedFiles`（声明的或约定默认值）进行匹配。若匹配，按 slice 文件变更路由。若无匹配，则不路由（文件在元数据治理范围之外）。 | 取决于匹配结果 |

对于非 slice 文件，粗粒度模式和契约推断模式使用**相同的路由规则**。它们仅在 slice 到 journey 的解析粒度上有所不同。

### 粗粒度模式（始终可用）

路由：`changedFile → slice（allowedFiles 或约定默认值）→ cell（belongsToCell）→ journey（spec.cells）`。

这是 Cell 级粒度。如果一个 Cell 有多个 slice，任何一个 slice 中的文件变更都会触发涉及该 Cell 的所有 journey。当 Cell 较小或 journey 较少时可以接受。

### 契约推断模式（生成索引）

对于拥有大量 slice 和 journey 的 Cell，`select-targets` 可以消费一个生成的索引，该索引基于契约使用的重叠关系将每个 slice 映射到它可能影响的 journey。

索引的派生方式为：journey 规约 `cells` → 每个 Cell 的 slice → 每个 slice 的 `contractUsages` → 将契约匹配回 journey。此派生使用 `journey.cells` 作为锚点，而非 `journey.contracts`（精选子集），以减少漏报。规则 C13 校验 `journey.cells` 与精选契约列表的一致性（尽力完整性）。根据护栏 G1，`journey.contracts` 是一个精选视图——精选与穷举之间的差距是一个设计选择，而非需要通过警告规则来修补的缺陷。

**局限性**：此模式通过契约使用推断 slice-journey 关联，无法捕获非契约耦合（共享进程内状态、基于约定的路由、运行时配置）。通过非契约路径影响 journey 的 slice 将被遗漏。结果是对粗粒度模式的尽力细化，而非精确的目标集。

索引文件：`generated/indexes/journey-slice-map.yaml`，由 `validate-meta` 或 `generate-assembly` 生成。结构：

```yaml
# generated — do not edit
generatedAt: "2026-04-04T10:30:00Z"
sourceFingerprint: "sha256:a1b2c3d4..."
entries:
  - id: session-login
    cell: access-core
    journeys:
      - J-sso-login
```

`sourceFingerprint` 是用于计算此索引的所有输入文件（journey 规约、cell.yaml、slice.yaml、contract.yaml）的哈希值。`validate-meta` 从当前源文件重新计算指纹并进行比较——不匹配意味着索引已过期。

**新鲜度**：`validate-meta` 每次运行都会重新计算索引并与现有文件做差异比较。如果重新计算的结果与文件不同，`validate-meta` 发出警告（CI 环境）或错误（release 分支）。在 CI 流水线中，`select-targets` 必须在 `validate-meta` **之后**执行，以确保消费的是最新索引。

当索引存在且是最新的时，`select-targets` 使用它进行契约推断的 slice 级粒度路由。当索引缺失或过期时，回退到粗粒度模式。

## V3 路线图（不在当前版本范围内）

以下变更计划在下一个主要版本中实施。在此记录作为设计方向，而非当前承诺。

1. **拆分 Journey 角色**：将验收规格（`J-*.spec.yaml`）、路由声明（`J-*.routing.yaml`）和测试计划（`J-*.plan.yaml`）分离。这消除了 G2 中记录的多角色耦合。
2. **引入显式非契约依赖图**：在每个 assembly 引入 `dependencies.yaml`，声明非契约耦合（共享状态、配置注入、基于约定的路由）。一旦可用，`select-targets` 可以从建议级（G5）升级为可靠级。
3. **将 L0 从 Cell 重命名为 module 或 library-partition**：当 `l0Dependencies` 在生产环境中使用两个发布周期后，评估是否应将 L0 正式从 Cell 概念中分离。

## 校验预期

`validate-meta` 执行以下规则，分为四组。各组按顺序执行：**A → B → C → D**。B 组依赖 A 组（ID 唯一性/存在性必须通过后才能进行引用完整性检查）。C 组依赖 B 组（引用必须有效后才能进行拓扑检查）。D 组独立于 C 组（所有 D 规则检查字段值和格式，而非拓扑）。在每组内部，规则可以并行执行；某条规则失败不会阻止同组中的兄弟规则。

**前提——consistencyLevel 排序**：校验规则中的所有比较运算符（`<=`、`>=`、`<`、`>`）使用全序 `L0 < L1 < L2 < L3 < L4`。此排序在本文档中是公理性的。每个级别的运行时语义定义在 `docs/architecture/consistency-levels.md` 中。

### A 组：身份标识 + 枚举有效性

| # | 规则 |
|---|------|
| A1 | `cell.id` 全局唯一。格式：`kebab-case`（小写字母、数字、连字符）。 |
| A2 | `slice.id` 在其父 Cell 内唯一。格式：`kebab-case`。全局限定名：`{cell.id}/{slice.id}`。 |
| A3 | `contract.id` 全局唯一。格式：`{kind}.{domain-path}.v{N}`。第一个片段 = kind，最后一个片段 = 版本号，中间片段 = 领域路径（一个或多个）。 |
| A4 | `journey.id` 全局唯一。格式：`J-{kebab-case}`。 |
| A5 | `assembly.id` 全局唯一。格式：`kebab-case`。 |
| A6 | `actors.yaml` 中的 `actor.id` 全局唯一。格式：`kebab-case`。不得与任何 `cell.id` 冲突。 |
| A7 | `contract.lifecycle` 的值必须是 `draft`、`active` 或 `deprecated`。 |
| A8 | `cell.type` 的值必须是 `core`、`edge` 或 `support`。 |
| A9 | `status-board.state` 的值必须是 `draft`、`todo`、`doing`、`blocked` 或 `done`。 |
| A10 | `status-board.risk` 的值必须是 `low`、`medium` 或 `high`。 |
| A11 | `status-board.blocker` 是必填字符串。无阻塞项时值为 `""`。不允许为 null 或省略该字段。 |

### B 组：引用完整性

| # | 规则 |
|---|------|
| B1 | `slice.belongsToCell` 指向一个已存在的 Cell。 |
| B2 | `journeys/*.yaml` 中的每个 `cells` 条目指向一个已存在的 Cell。每个 `contracts` 条目指向一个已存在的契约。 |
| B3 | slice 中每个 `contractUsages[].contract` 指向一个已存在的契约。 |
| B4 | 每个契约端点 actor（提供方和消费方条目）引用一个 cell id 或 `actors.yaml` 中的 actor id。 |
| B5 | `assembly.cells` 的条目指向已存在的 Cell。 |
| B6 | 如果 `assembly.exportedContracts` / `importedContracts` 存在，每个条目指向一个已存在的契约。 |
| B7 | 从 `contract.id` 解析出的版本片段（最后一个点分隔片段）必须与文件路径中的版本目录片段匹配。 |
| B8 | `contract.ownerCell` 必须引用一个 `cell.id`，而非外部 actor。 |
| B9 | `journeys/*.yaml` 中 `fixtures` 列表引用的每个 fixture 必须有对应的 `fixtures/{fixture-id}.yaml` 文件。 |
| B10 | `slice.belongsToCell` 必须等于从 slice 目录路径 `cells/{cell-id}/slices/{slice-id}/` 解析出的 `{cell-id}` 片段。 |
| B11 | 契约中每个 `schemaRefs` 的值必须解析为契约版本目录中的已存在文件。 |
| B12 | `cell.id` 必须等于包含 `cell.yaml` 的目录名（例如，`cells/access-core/cell.yaml` 要求 `id: access-core`）。 |
| B13 | `slice.id` 必须等于包含 `slice.yaml` 的目录名（例如，`cells/access-core/slices/session-login/slice.yaml` 要求 `id: session-login`）。 |

### C 组：拓扑

| # | 规则 |
|---|------|
| C1 | `contractUsages[].role` 必须是所引用契约 `kind` 在角色-类型表中的有效值。 |
| C2 | 提供方角色：`slice.belongsToCell` 必须等于契约提供方端点的 actor。 |
| C3 | 消费方角色：`slice.belongsToCell` 必须出现在契约消费方端点列表中（或列表为 `["*"]`）。 |
| C4 | `contract.consistencyLevel` 不得超过提供方 actor 的一致性级别（Cell 使用 `cell.consistencyLevel`，外部 actor 使用 `actor.maxConsistencyLevel`）——这是一个硬约束（错误）。此外，如果 `contract.consistencyLevel` 超过 `ownerCell.consistencyLevel`，`validate-meta` 发出警告（治理团队可能缺乏该级别的运维经验），但这不是阻断性错误。 |
| C5 | `slice.consistencyLevel`（存在时）不得超过 `cell.consistencyLevel`。 |
| C6 | **领域约束**（非拓扑派生）：契约 `kind` + `consistencyLevel` 最低要求——`http` 要求 `>= L1`（请求处理需要本地事务），`event` 要求 `>= L2`（可靠投递需要 outbox），`command` 要求 `>= L2`（同上），`projection` 要求 `>= L3`（物化需要跨 Cell 最终一致性）。 |
| C7 | L0 隔离：L0 Cell 不得出现在任何契约端点字段中。L0 slice 必须有空的 `contractUsages`。L0 Cell 可以被同一 assembly 中的兄弟 Cell 直接 import（参见 L0 Cell 交互模型）。 |
| C8 | 任何两个 slice 的有效 `allowedFiles` 模式（声明的或约定默认值）不得重叠。重叠意味着存在某个文件系统路径同时匹配两个 glob。工具使用交叉匹配测试。 |
| C9 | 一个 Cell 内的所有 slice 必须通过其有效 `allowedFiles`（声明的或约定默认值）共同覆盖 `cells/{cell-id}/slices/` 下的实现文件。 |
| C10 | `journeys/*.yaml` 的 `contracts` 列表中的每个契约，其提供方 actor 或至少一个消费方 actor 必须出现在该 journey 的 `cells` 列表中，或者是 `actors.yaml` 中已注册的外部 actor。 |
| C11 | 如果 `assembly.exportedContracts` / `importedContracts` 是手动维护的：每个列出的契约必须确实跨越了 assembly 边界。所有跨越边界的契约必须被列出（完整性）。 |
| C12 | 一个 Cell 最多只能属于一个 assembly。任何 `cell.id` 不得出现在多个 `assembly.cells` 列表中。 |
| C13 | `journey.cells` 尽力完整性：对于 `journey.contracts` 中的每个契约，提供方 actor（如果是 Cell）必须出现在 `journey.cells` 中。对于消费方 actor：如果消费方列表为 `["*"]`，跳过该契约的消费方检查；否则，所有作为 Cell 的消费方 actor 必须出现在 `journey.cells` 中。外部 actor 不在检查范围内。注意：此规则仅针对精选契约列表校验 Cell——无法证明相对于完整契约全集的完整性。 |
| C14 | 活跃契约使用：每个 `lifecycle: active` 且提供方 actor 为 Cell 的契约，必须有至少一个 slice 为其声明提供方角色的 `contractUsages` 条目。提供方为外部 actor 的契约豁免——外部 actor 没有 slice。 |
| C16 | 角色-verify 方向一致性：对于每个 `slice.contractUsages` 条目 `{contract, role}`，如果 `slice.verify.contract` 包含与该 contract-id 匹配的标识符，则 verify 条目的角色后缀必须等于声明的角色。示例：`contractUsages role: serve` 要求 verify 条目以 `.serve` 结尾，而非 `.call`。范围：仅检查同时出现在两个列表中的条目（非覆盖规则）。 |
| C17 | 已弃用契约引用限制：`slice.contractUsages` 不得引用 `lifecycle: deprecated` 的契约，除非该 slice 为该契约声明了 `migrations` 条目（参见迁移）。警告级别。 |
| C18 | 跨 assembly 契约依赖：对于被多个 assembly 中的 Cell 引用的每个契约，`validate-meta` 将依赖边（provider-assembly → client-assembly）输出到 `generated/indexes/assembly-dependency-graph.yaml`。在 CI 中为信息性提示；在 release 分支上，当 `exportedContracts`/`importedContracts` 存在但依赖图缺失时为错误。 |
| C19 | Verify/waiver 闭环：对于 slice 中每个提供方角色的 `contractUsages` 条目，slice 必须有匹配的 `verify.contract` 标识符（按 contract-id + 角色后缀匹配）**或**具有有效 `owner`、`reason` 和未过期 `expiresAt` 的 `verify.waivers` 条目。缺失覆盖且无 waiver 为错误。过期的 waiver 视为缺失。 |
| C20 | L0 依赖声明：每个导入 L0 Cell 的 Cell 必须在 `cell.yaml` 的 `l0Dependencies` 中声明该依赖。每个引用的 Cell 必须存在、`consistencyLevel` 为 L0、且在同一 assembly 中。 |

### D 组：执行

| # | 规则 |
|---|------|
| D1 | 交付动态字段（`readiness`、`risk`、`blocker`、`done`、`verified`、`nextAction`、`updatedAt`）不得出现在 `cell.yaml`、`slice.yaml`、`contract.yaml` 或 `assembly.yaml` 中。`lifecycle` 是治理字段，不受此限制。 |
| D2 | `slice.verify` 必须满足其有效 `consistencyLevel` 的最低验证要求：L0-L1 要求非空的 `verify.unit`；L2+ 要求非空的 `verify.unit` 和非空的 `verify.contract`。所有 Cell 要求非空的 `verify.smoke`。D2 是有意设定的最低门槛——它不要求 `contractUsages` 条目与 `verify.contract` 条目一一对应。团队可以通过扩展规则施加更严格的标准。 |
| D3 | 所有 verify 标识符——`passCriteria.checkRef`、`cell.verify.smoke`、`slice.verify.unit` 和 `slice.verify.contract` 中的——必须遵循 verify 命名约定（前缀分发格式）。对于 `contract` 类别，倒数第二个片段必须匹配 `v\d+`，最后一个片段必须是已知角色。`validate-meta` 检查格式有效性；`verify-slice` 和 `run-journey` 检查运行时可解析性。 |
| D4 | Schema 目录的 kind 片段必须等于 `contract.kind`（单数形式）。 |
| D5 | 生成的索引和产物必须使用规范字段名（例如 `id`，而非 `sliceId`）。 |
| D6 | 契约 `endpoints` 字段名必须匹配按类型定义的端点表：`http` 必须使用 `server`/`clients`，`event` 必须使用 `publisher`/`subscribers`，`command` 必须使用 `handler`/`invokers`，`projection` 必须使用 `provider`/`readers`。`endpoints` 下的其他任何字段名均无效。 |
| D7 | `contract.kind` 必须等于 `contract.id` 的第一个点分隔片段。例如，`kind: http` 要求 `id` 以 `http.` 开头。 |
| D8 | `status-board.evidenceRefs` 的条目必须是有效的相对路径格式（不允许绝对路径，不允许 `..` 遍历）。 |
| D9 | `status-board.journeyId` 在 `status-board.yaml` 内必须唯一。根据护栏 G4，这是一个 **CI 流水线建议**，而非模型级不变量：当一个 journey 没有 status-board 条目时，`validate-meta` 发出警告，而非错误。Release 分支上的门控是流水线策略决定，不在本规约范围内。 |
| D10 | 生成产物（`journey-slice-map.yaml`、assembly 可选生成字段）必须包含 `generatedAt`（ISO 时间戳）和 `sourceFingerprint`（用于计算产物的所有输入文件的哈希值）。`validate-meta` 每次运行都从当前源文件重新计算指纹；不匹配意味着产物已过期——在 CI 中为警告，在 release 分支上为错误。只有 `validate-meta` 计算 `sourceFingerprint`；`generate-assembly` 写入字段值，但将指纹计算推迟给 `validate-meta`。 |
| D-W1 | （警告）对于每个有非空 `contractUsages` 的 slice，如果任何条目在 `verify.contract` 标识符中没有对应项（按 contract-id 匹配），发出警告列出未覆盖的契约。团队可通过 `cell.yaml` 扩展将其提升为错误：`verifyPolicy: { contractCoverage: strict }`。 |
