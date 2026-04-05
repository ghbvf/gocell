<!--
Sync Impact Report
===================
Version change: 0.0.0 (blank template) → 1.0.0
Modified sections: ALL (initial population from template)
Added principles:
  - I. Cell-Native 分层架构
  - II. Cell 治理与六条真相
  - III. Contract 边界纪律
  - IV. 测试先行（不可协商）
  - V. Cell 数据主权
  - VI. 事件驱动与一致性等级（L0-L4）
  - VII. Journey 验收驱动
  - VIII. 安全内建
  - IX. 简约与增量交付
Added sections:
  - 红线清单（17 条 RL-01 ~ RL-17）
  - 技术栈与约束
  - 开发工作流
  - 治理
Removed sections: none (template placeholders replaced)
Templates requiring updates:
  - .specify/templates/plan-template.md ✅ (Constitution Check section
    already present as generic gate; GoCell principles fill it at runtime)
  - .specify/templates/spec-template.md ✅ (no structural change needed;
    User Scenarios + Requirements + Success Criteria cover GoCell needs)
  - .specify/templates/tasks-template.md ✅ (no structural change needed;
    Phase structure accommodates GoCell's metadata-first + verify gates)
Follow-up TODOs: none
Reference: adapted from docs/archive/constitution-from-winmdm.md (v1.3.0),
           removing all WinMDM-specific knowledge
-->

# GoCell 项目宪法

## 核心原则

### I. Cell-Native 分层架构

所有代码 MUST 遵循以下分层结构，依赖方向单向向下：

| 层 | 目录 | 职责 | 可依赖 |
|---|------|------|--------|
| kernel | `kernel/` | Cell/Slice 运行时 + 治理工具（底座灵魂） | stdlib + `pkg/` + `gopkg.in/yaml.v3` |
| cells | `cells/` | Cell 实现（access-core / audit-core / config-core） | `kernel/` + `runtime/` |
| runtime | `runtime/` | 通用运行时（http / auth / worker / observability） | `kernel/` + `pkg/` |
| adapters | `adapters/` | 外部系统适配（postgres / redis / oidc / s3 等） | `kernel/` + `runtime/` 定义的接口 |
| pkg | `pkg/` | 共享工具包（errcode / ctxkeys） | stdlib |
| cmd | `cmd/` | CLI 入口 | 所有层 |
| examples | `examples/` | 示例项目 | 所有层 |

**不可违反的依赖禁令**：

- `kernel/` MUST NOT 依赖 `runtime/`、`adapters/`、`cells/`
- `cells/` MUST NOT 依赖 `adapters/`（通过接口解耦）
- `runtime/` MUST NOT 依赖 `cells/`、`adapters/`
- Cell 之间 MUST NOT 直接 import 另一个 Cell 的 `internal/`
  — **L0 例外**：L0 Cell（纯计算分区）可被同 Assembly 内兄弟
  Cell 直接导入，但 MUST 在 `cell.l0Dependencies` 显式声明

Cell 三种子类型：

- **core** — 强一致性状态机（如 access-core、audit-core、config-core）
- **edge** — 边缘节点 Cell
- **support** — 辅助 Cell

L0 是特殊计算分区：纯函数库，同 Assembly 内直接导入，不参与契约。

### II. Cell 治理与六条真相

GoCell 是元数据驱动的架构。以下六条真相是系统的认知根基，
MUST NOT 被任何工具或流程违反：

1. **`slice.contractUsages` 是实现级依赖真相。**
   需要完整依赖图时，从 slice.contractUsages 聚合，不从
   journey.contracts 或 cell.yaml 推导。
2. **`contract.yaml` 是边界协议真相。**
   契约关系只在 contract.yaml 定义，slice 和 journey 不是 owner。
3. **`journey.yaml` 是验收真相，不是依赖真相。**
   journey.cells 是路由锚点（best-effort），journey.contracts
   是验收策展，二者均非完备集合。
4. **`status-board.yaml` 是运营层，不参与架构合法性判定。**
   `validate-meta` 对缺失条目仅发警告，不阻断 CI。
5. **L0 是例外模型，MUST 显式声明依赖。**
   通过 `cell.l0Dependencies` 声明，`validate-meta` 校验目标
   存在且在同 Assembly。
6. **每条声明都 MUST 对应 `verify` 或 `waiver`。**
   无覆盖、无豁免即为不合规。

**真相归属**：每个关键事实只有一个 owner。

| 事实 | Owner | 不是 owner |
|------|-------|-----------|
| Slice 用了哪些契约 | `slice.contractUsages` | journey.contracts |
| 契约边界协议 | `contract.yaml` | slice 或 journey |
| 验收标准 | `journey.passCriteria` | slice.verify |
| Cell 归属 | `slice.belongsToCell`（或目录路径） | cell.yaml 反向索引 |
| 交付状态 | `status-board.yaml` | 任何其他元数据文件 |
| Assembly 边界 | `generated/boundary.yaml` | assembly.yaml 内联 |

**验证保证**：

| 工具 | 级别 | 说明 |
|------|------|------|
| `validate-meta` | **blocking** | 通过才允许合入 |
| `select-targets` | **advisory** | 优化建议，非完整性证明 |
| `verify-slice` | **blocking** | slice 的 unit + contract 测试 |
| `verify-cell` | **blocking** | Cell 的 smoke 测试 |
| `run-journey` | **blocking** | Journey 的 auto passCriteria |

**Metadata-first**：新建 Cell/Slice/Contract/Journey MUST 先有
合法 YAML 元数据（validate-meta 通过），再写实现代码。

### III. Contract 边界纪律

L1+ Cell 之间的所有交互 MUST 通过 Contract。四种 Contract
kind 及其合法角色：

| Kind | 提供方字段 | 消费方字段 | 提供方角色 | 消费方角色 |
|------|-----------|-----------|-----------|-----------|
| `http` | `server` | `clients` | serve | call |
| `event` | `publisher` | `subscribers` | publish | subscribe |
| `command` | `handler` | `invokers` | handle | invoke |
| `projection` | `provider` | `readers` | provide | read |

**生命周期**：`draft → active → deprecated`，单向不可逆。
已 deprecated 的契约 MUST NOT 被新代码引用（除非附带
`migrations` 声明）。

**Verify/Waiver 闭环**：每个 `contractUsages` 条目 MUST 有：
- 匹配的 `verify.contract` 标识符，**或**
- 一条 `verify.waivers` 条目（含 owner / reason / expiresAt）

过期 waiver 等同缺失，`validate-meta` 报错。Waiver 是临时
豁免，不是常态配置。

**Event 额外必填**：`replayable`、`idempotencyKey`、
`deliverySemantics`。Projection 额外必填：`replayable`。

**Schema 分离**：`contract.yaml` 声明关系（谁提供/谁消费），
`*.schema.json` 定义数据格式。`schemaRefs` 引用的文件 MUST
存在。

### IV. 测试先行（不可协商）

TDD 是所有代码的强制要求：

1. 先写测试，确认测试 **FAIL**。
2. 实现代码直到测试 **PASS**。
3. 修改测试以强行通过是 **FORBIDDEN**。

覆盖率目标：

| 层 | 最低覆盖率 | 测试风格 |
|---|-----------|---------|
| `kernel/` | ≥ 90% | table-driven test |
| 其他层（新增/修改代码） | ≥ 80% | unit + contract / integration |

验证矩阵（按一致性等级）：

| 等级 | Unit | Contract | Smoke | Journey | Replay |
|------|------|----------|-------|---------|--------|
| L0 | 必须 | — | — | — | — |
| L1 | 必须 | — | 必须 | — | — |
| L2 | 必须 | 必须 | 必须 | 必须 | — |
| L3 | 必须 | 必须 | 必须 | 必须 | 必须 |
| L4 | 必须 | 必须 | 必须 | 必须 | 必须 |

测试工具链：`testify` 断言，mock 放 `internal/mock/`。

**红线 — t.Skip 滥用**：`t.Skip(...)` MUST NOT 被当作通过。
一致性关键测试（outbox 写入、consumer 幂等、读模型重建）
MUST NOT 被默认跳过。每个 skip 点 MUST 有文档理由和关联
tracking issue。无理由地对一致性测试添加 `t.Skip` 是 PR
拒绝的充分理由。

### V. Cell 数据主权

每个 Cell 拥有独占的数据库 schema（`cell_{cell_id}` 命名）。

**表分类**：

| 分类 | 真相源 | 写入方 | 可重建 |
|------|--------|--------|--------|
| Authoritative | 是 | 仅 Owner Cell | 不可 |
| Projection | 否 | Consumer Cell | 可（事件重放） |
| Cache | 否 | 任何 Cell | 可（从源） |
| Coordination | 否 | Owner Cell | 视情况 |

**不可违反的数据禁令**：

- 跨 Cell Schema JOIN FORBIDDEN
- 跨 Cell 外键 FORBIDDEN
- 跨 Cell UPDATE/DELETE FORBIDDEN
- 跨 Cell 数据需求 MUST 通过 EventBus 预物化到消费方
  schema 内的 Projection 表，或通过显式内部 API 调用获取
- Projection 表 MUST NOT 升格为 Authoritative
- Projection 表 MUST 可从事件重放完整重建

**迁移纪律**：

- 每次 schema 变更 MUST 有 up/down 迁移对
- 已有迁移文件 MUST NOT 修改
- 新增 NOT NULL 列 MUST 有默认值
- 大表索引使用 `CREATE INDEX CONCURRENTLY`

### VI. 事件驱动与一致性等级（L0-L4）

每个 Cell 和 Slice MUST 声明一致性等级。等级驱动事务边界、
Outbox 使用和测试策略。

| 等级 | 名称 | 范围 | Outbox |
|------|------|------|--------|
| L0 | LocalOnly | 单 Slice 内部，纯计算 | 不适用 |
| L1 | LocalTx | 单 Cell 本地事务 | 不需要 |
| L2 | OutboxFact | 本地事务 + Outbox 发布 | **必须** |
| L3 | WorkflowEventual | 跨 Cell 最终一致 | 消费方可用 |
| L4 | DeviceLatent | 设备长延迟闭环 | 可用于初始分发 |

**L2 强制规则**：L2 事件 MUST 通过 Outbox 发布。在 DB 提交
后直接 `eventbus.Publish()` 对 L2 事件是 FORBIDDEN。业务写入
和 Outbox 条目 MUST 在同一事务内。

**Consumer 规则**：

- 所有 consumer MUST 幂等（按 event_id 去重）
- L2 consumer MUST 有死信队列
- ACK 时机：业务逻辑 + 幂等标记写入完成后
- unmarshal 失败 → 死信路由，MUST NOT return nil

**实现前六问清单**：

涉及状态变更的功能，实现前 MUST 回答：

1. **真相源**：该变更的权威写模型是什么？
2. **L1 边界**：同 Cell 内哪些状态 MUST 强一致（单事务）？
3. **L2 传播**：哪些状态通过事件传播到其他 Cell 或读模型？
4. **Outbox 判定**：丢失事件是否产生不正确/不可恢复状态？
   是 → Outbox 必须；否 → best-effort 可用。
5. **Consumer 契约**：每个消费方的幂等键、ACK 策略、重试策略？
6. **读模型重建**：每个下游读模型能否从真相源完整重建？
   不能 → MUST NOT 依赖 best-effort 事件。

**L4 专项规则**：L4 MUST NOT 被当作普通异步处理。需要显式的
超时处理、重试预算和迟到合并策略。

详见 `docs/architecture/consistency.md`。

### VII. Journey 验收驱动

Journey 是跨越一个或多个 Cell 的用户级业务闭环。

- Journey 是**验收真相**，不是依赖真相（第三条真相）
- `journey.cells` 是路由锚点（best-effort），不是完备参与方集合
- `journey.contracts` 是验收策展，不是完整依赖图
- 需要完整依赖图时，从 `slice.contractUsages` 聚合

**passCriteria 定义"完成"**：

- `mode: auto` — 可执行检查，`run-journey` 自动验证
- `mode: manual` — 人工签核

**Status Board 是运营层**（第四条真相）：

- 不参与架构合法性判定
- `validate-meta` 对缺失条目仅发警告
- Release 门禁是流水线策略，不是模型规则

`run-journey` 是 auto passCriteria 的 blocking 门禁。

### VIII. 安全内建

- 所有端点 MUST 有 JWT 中间件或被显式声明在认证白名单中
- 证书和密钥操作 MUST 产生审计日志
- 列表端点 MUST 强制分页，`page_size` 上限 ≤ 500
- 新增查询 MUST 有对应索引（通过 `EXPLAIN ANALYZE` 验证）
- 敏感字段（证书路径、密钥）MUST NOT 出现在明文日志中
- 服务间通信 MUST 有认证机制（service token 或 mTLS）

**红线 — Fail-Fast 基础设施**：生产配置 MUST NOT 回退到
localhost 默认值、内存或 noop publisher、no-op EventBus。
基础设施启动失败 MUST 以硬错误呈现（fail-fast），不是静默
降级。以 noop EventBus 成功启动的服务提供虚假信心，掩盖
关键数据丢失。

**红线 — 内部 API 信任**：内部 API（`/internal/v1/…`）
MUST NOT 仅因不公开路由就被隐式信任。MUST 有显式网络隔离
（私有子网或 service-mesh 策略），MUST NOT 允许未认证的
任意调用方访问。新增内部端点 MUST 在代码注释或架构文档中
声明调用方白名单。

**红线 — 安全敏感 Not-Found**：在安全敏感路径（认证、注册、
token 验证、授权检查）上，"资源未找到"或"用户未找到"
MUST 被视为安全事件：以 `slog` WARN 级别记录并附带请求
上下文，适当情况下进行限流。在这些路径上静默返回 404 而
不记日志是 FORBIDDEN。

### IX. 简约与增量交付

- 从最小可行实现开始。YAGNI 适用。
- 避免过度工程化：不添加未被明确要求的功能、抽象或可配置性。
- 三行相似代码优于一个过早抽象。
- `select-targets` 是 advisory 级别。在非契约依赖图补齐前，
  MUST NOT 将其作为唯一门禁。
- 待定决策（如 L0 目录位置、Journey 文件拆分、非契约依赖图
  最终形态）在工具实现中根据实际需要逐步确定，不预设。
  详见 `docs/architecture/v3-skeleton-todos.md`。
- kernel/ 只有一个外部依赖（`gopkg.in/yaml.v3`）。新增
  kernel 外部依赖 MUST 有充分理由并经架构审查。

## 红线清单

以下模式是 PR 立即拒绝的充分理由。

### 元数据治理红线

| ID | 红线 |
|----|------|
| RL-01 | 跨 Cell 直接 import `internal/`（L0 需显式 `l0Dependencies`） |
| RL-02 | `validate-meta` 不通过即合入 |
| RL-03 | `contractUsage` 缺失 `verify.contract` 或 `waiver` |
| RL-04 | 使用旧字段名（`cellId` / `sliceId` / `contractId` / `assemblyId` / `ownedSlices` / `authoritativeData` / `producer` / `consumers` / `callsContracts` / `publishes` / `consumes`） |
| RL-05 | 动态交付状态出现在 `cell.yaml` / `slice.yaml` / `contract.yaml` / `assembly.yaml`（只允许在 `status-board.yaml`） |

### 一致性与事件红线

| ID | 红线 |
|----|------|
| RL-06 | L2 事件通过 fire-and-forget 直接发布而非 Outbox |
| RL-07 | Consumer ACK 格式错误的关键消息而无补偿或死信路由 |
| RL-08 | 不可重建的读模型仅靠 best-effort 事件驱动 |
| RL-09 | 将执行状态事件误分类为 L3（当下游正确性依赖该事件时） |
| RL-10 | 复制已有 `eventbus.Publish` 模式到新事件链但未独立做一致性分类 |

### 数据主权红线

| ID | 红线 |
|----|------|
| RL-11 | 跨 Cell Schema JOIN / 跨 Cell 外键 / 跨 Cell UPDATE/DELETE |
| RL-12 | Projection 表升格为 Authoritative |

### 测试红线

| ID | 红线 |
|----|------|
| RL-13 | 对一致性关键测试添加 `t.Skip` 但无文档理由和 tracking issue |

### 安全红线

| ID | 红线 |
|----|------|
| RL-14 | 生产环境 localhost 降级 / noop publisher / no-op EventBus |
| RL-15 | 内部 API 无网络隔离且无调用方白名单 |
| RL-16 | 安全敏感路径返回 404 但不记日志 |

### 契约生命周期红线

| ID | 红线 |
|----|------|
| RL-17 | 已 deprecated 的契约被新代码引用且无 `migrations` 声明 |

## 技术栈与约束

- **语言**：Go（最新稳定版）
- **Web 框架**：stdlib `net/http` + `go-chi/chi/v5`
- **数据库**：PostgreSQL（schema-per-cell）
- **缓存 / EventBus**：Redis Streams（默认）或 RabbitMQ
- **日志**：`slog` only；`fmt.Println` / `fmt.Printf` / `log.Printf` FORBIDDEN
- **错误处理**：`pkg/errcode` 包；裸 `errors.New` 对外暴露 FORBIDDEN；
  所有错误 MUST 被处理（`_ = someFunc()` 忽略错误 FORBIDDEN）
- **Context**：所有函数首参 MUST 为 `context.Context`
- **命名**：DB 字段 `snake_case`，JSON/Query/Path `camelCase`
- **kernel 外部依赖**：仅 `gopkg.in/yaml.v3`
- **提交**：Conventional Commits（`feat/fix/refactor/docs`）
- **元数据**：`gopkg.in/yaml.v3` 解析；JSON Schema Draft 2020-12 校验
- **认知复杂度**：函数 ≤ 15
- **参考框架**：详见 `docs/references/framework-comparison.md`

## 开发工作流

1. **分支先行**：所有工作在 feature branch 上进行。
   工作类型编号：001-199 Feature / 200-399 Fix / 800-899 Docs /
   900-999 Experiment。分支命名 `{NNN}-{short-description}`。

2. **Metadata-first**：新建 Cell / Slice / Contract / Journey
   MUST 先产出合法 YAML 元数据（validate-meta 通过），再写
   实现代码。

3. **TDD 循环**：Red → Green → Refactor（详见原则 IV）。

4. **验证门禁链**：
   `validate-meta`（blocking）→ `verify-slice`（blocking）→
   `verify-cell`（blocking）→ `run-journey`（blocking）。
   `select-targets`（advisory）用于优化范围，非唯一门禁。

5. **迁移纪律**：up/down 对；不修改已有迁移文件；新 NOT NULL
   列有默认值；大表索引 `CREATE INDEX CONCURRENTLY`。

6. **安全门禁**：新端点上线前，验证原则 VIII 全部条目。

7. **一致性门禁**：涉及状态变更的功能，实现前 MUST 完成
   L0-L4 分类和六问清单（详见原则 VI）。

8. **Speckit 流水线**：feature 级工作遵循
   `/speckit.specify` → `/speckit.plan` → `/speckit.tasks` →
   `/speckit.implement`。

9. **框架对标**：新建或重构 kernel/、cells/、runtime/、adapters/
   模块时，MUST 先查阅 `docs/references/framework-comparison.md`
   并拉取对标源码，注明采纳/偏离理由。

10. **提交纪律**：Conventional Commits；涉及功能或行为变更时
    同步更新对应文档。

## 治理

- 本宪法是 GoCell 项目的最高权威文档。当与 `CLAUDE.md`、
  `.claude/rules/`、`docs/architecture/` 或其他实践指南冲突时，
  以本宪法为准。
- `CLAUDE.md` 是本宪法的实施细则（"如何做"）。
  `.claude/rules/gocell/` 提供战术编码标准。二者 MUST 与本宪法
  保持一致。
- `docs/architecture/` 下的参考文档（overview.md、glossary.md、
  consistency.md、metadata-model-v3.md）是权威架构参考，
  受本宪法约束。
- 所有代码审查和 PR MUST 验证是否符合本宪法原则。
- 修订本宪法 MUST：(a) 有书面理由；(b) 按语义版本号递增；
  (c) 传播检查至 `CLAUDE.md` 和 `.claude/rules/`。
- 版本策略：MAJOR = 原则删除或重定义；MINOR = 新增原则或章节；
  PATCH = 措辞澄清。
- 超出本宪法原则的复杂度 MUST 在 plan 的 Complexity Tracking
  表中显式论证。

**Version**: 1.0.0 | **Ratified**: 2026-04-05 | **Last Amended**: 2026-04-05
