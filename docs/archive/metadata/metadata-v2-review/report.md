# Metadata Templates V2 - 六角色团队分析报告

> 分析对象：`docs/architecture/metadata-templates-v2.md`
> 方法：六角色第一性原理审查

## 角色定义

| 角色 | 关注点 |
|------|--------|
| **架构师** | 结构一致性、分层完备性、设计目标是否自洽 |
| **DDD 专家** | 领域建模、聚合边界、职责分配 |
| **工具链开发者** | 规则可实现性、边界条件、自动化可行性 |
| **一线开发者** | 日常使用摩擦、认知负荷、易犯错误 |
| **QA/验证专家** | 验证规则完备性、遗漏场景、边界 case |
| **PM** | 治理开销、落地阻力、投产路径 |

---

## Round 1 分析（2026-04-04）

首轮分析发现 16 个问题。

### 问题汇总

| # | 严重度 | 类别 | 问题摘要 |
|---|--------|------|----------|
| 1 | Medium | 目标违反 | version 三重冗余，违反 "author once" |
| 2 | Medium | 目标违反 | assembly 生成字段的 truth source 不明 |
| 3 | **High** | 语义错误 | C6 消费者一致性约束方向可疑 |
| 4 | **High** | 语义错误 | C4 应约束 provider actor 而非 ownerCell |
| 5 | Medium | 缺失规则 | Cell 到 Assembly 唯一性未定义 |
| 6 | **High** | 缺失验证 | endpoint 字段名与 kind 无交叉验证 |
| 7 | Medium | 缺失定义 | Fixture 概念未定义、无验证 |
| 8 | Medium | 缺失验证 | contractUsages 与 verify.contract 无覆盖检查 |
| 9 | Low | 缺失验证 | checkRef 无实际解析验证 |
| 10 | Low | 缺失规则 | L0 cell 可以无意义地出现在 journey 中 |
| 11 | Low | 文档体验 | 层级呈现顺序违反直觉（1-4-2-3） |
| 12 | Medium | 缺失约束 | Contract ID 的段数/分隔解析规则未明确 |
| 13 | **High** | 治理冲突 | CLAUDE.md 与 V2 字段命名直接矛盾 |
| 14 | Medium | 缺失设计 | Lifecycle 状态转换规则未定义 |
| 15 | Medium | 缺失设计 | Contract 删除/版本升级策略缺失 |
| 16 | Low | 精度不足 | actors.yaml 变更路由缺少 diff 级精度 |

### Round 1 详细分析

<details>
<summary>展开查看 Round 1 完整分析</summary>

#### 第一轮：设计目标自洽性审查

##### 问题 1：版本三重冗余，违反目标 #1

**角色：架构师 + 工具链开发者**

> 目标 #1："A fact should be authored once and in exactly one place."

Contract version 在三个地方被书写：

1. `id` 的尾段：`http.auth.login.v1`
2. `version` 字段：`v1`
3. 目录路径：`contracts/http/auth/login/v1/`

B7 验证规则要求三者一致，说明设计者知道这是冗余的，但选择用验证而非消除来应对。按第一性原理，应该只有一个来源（比如从目录路径派生），其余由工具生成或验证。

**建议**：保留 `id` 和目录路径，去掉 `version` 字段，由工具从 `id` 派生。

##### 问题 2：Assembly 可选生成字段的暧昧地位，违反目标 #6

**角色：架构师 + PM**

> 目标 #6："Derivable facts should be generated or validated, not hand-maintained."

`assembly.yaml` 的 `exportedContracts`、`importedContracts`、`smokeTargets` 被标注为 "Optional Generated Fields"，但同时允许 "hand-curated override"。这导致：

- 不清楚谁是 source of truth——工具生成还是人工维护？
- C12 验证规则检查 hand-curated 的正确性和完备性，但如果字段不存在则跳过验证。这创造了一个验证盲区。

**建议**：明确声明这是一个过渡策略（初期手写，成熟后切换到生成），或始终生成并禁止手动覆盖。

#### 第二轮：领域建模问题

##### 问题 3：C6 消费者一致性约束方向可疑

**角色：DDD 专家 + 架构师**

规则 C6：

> "if a slice references a contract via a client role, the effective consistencyLevel of slice.belongsToCell's cell must be >= the contract's consistencyLevel."

从领域语义看，这个约束的逻辑值得质疑：

- 一致性级别描述的是**这个 cell 自身写操作的保证强度**，而非它**接收数据的能力**。
- L1（LocalTx）的 cell 完全可以订阅 L2（OutboxFact）的事件——ConsumerBase 已经内置了幂等检查和 DLQ。消费者不需要自己也是 L2 才能处理 at-least-once 投递。
- 实际场景：`edge-bff`（外部 actor，maxConsistencyLevel: L1）想订阅 `event.session.created.v1`（L2），C6 会阻止它，因为 L1 < L2。

**核心矛盾**：consistencyLevel 定义的是"我提供的保证"还是"我参与的交互所需的保证"？如果是前者，C6 就是错的。C4（provider 约束）是合理的，但消费端不需要对称。

**建议**：C6 应改为仅约束 provider-side，或引入 `minConsumptionLevel` 来区分生产和消费能力。

##### 问题 4：ownerCell 与 provider-side actor 的职责模糊

**角色：DDD 专家 + 工具链开发者**

C4 使用 `ownerCell.consistencyLevel` 来约束 contract。如果 ownerCell 和 provider 不同：

- cell-A（L3 owner）+ cell-B（L1 provider）→ 合约可声称 L3 保证，但实际 provider 只能做到 L1。

**建议**：C4 应约束 **provider-side actor** 的 consistencyLevel，或同时约束两者（取较小值）。

##### 问题 5：Cell 能否属于多个 Assembly？

**角色：DDD 专家 + 一线开发者**

文档没有说明一个 cell 是否可以出现在多个 assembly 中。如果不允许，应增加 validation rule。

#### 第三轮：验证规则完备性

##### 问题 6：缺少 endpoint 字段名与 kind 的一致性验证

**角色：QA/验证专家**

没有验证规则确保 contract 使用了正确的 endpoint 字段名（如 `kind: http` 必须用 `endpoints.server`/`endpoints.clients`）。

**建议**：增加规则 D11。

##### 问题 7：Fixture 完全无验证

**角色：QA/验证专家 + 工具链开发者**

Journey spec 有 `fixtures` 字段，但没有定义 fixture 是什么、在哪声明、怎么加载。阻塞工具链实现。

##### 问题 8：contractUsages 与 verify.contract 之间无交叉验证

**角色：QA/验证专家 + 一线开发者**

D7 只检查 L2+ slice 的 `verify.contract` 非空，不检查覆盖度。

**建议**：要么加覆盖规则，要么明确声明 D7 是有意的最低门槛。

##### 问题 9：passCriteria checkRef 无解析验证

D8 只验证命名格式，不验证实际可解析性。应在文档中声明这是设计选择。

##### 问题 10：L0 Cell 在 Journey 中的无效引用

C8 禁止 L0 cell 参与 contract，但不阻止 L0 cell 出现在 journey.cells 中。

#### 第四轮：一线开发者体验

##### 问题 11：文档呈现顺序违反认知流

层级按 1-4-2-3 顺序呈现。

##### 问题 12：Contract ID 的域名歧义

`id` 格式为 4 段，但未限制 domain/action 为单段。5 段 id 解析会出错。

#### 第五轮：与 CLAUDE.md 的冲突

##### 问题 13：CLAUDE.md 与 V2 模型的直接冲突

CLAUDE.md 使用 `cellId`/`sliceId`/`ownedSlices`/`authoritativeData`/`contracts`/`journeys`，V2 全部禁止。

#### 第六轮：缺失的设计维度

##### 问题 14：缺少 Contract 生命周期状态机

lifecycle 无合法转换规则、副作用定义、时间约束。

##### 问题 15：缺少 Contract 删除/版本升级策略

deprecated 之后的删除流程、引用完整性维护、版本共存规则均缺失。

##### 问题 16：select-targets 的 actors.yaml 变更路由不充分

actors.yaml 变更路由缺少 diff 级精度。

</details>

---

## Round 2 分析（2026-04-04）

文档已更新，修复了 Round 1 中 16 项问题中的 8 项。本轮重新从第一性原理审查，聚焦残留问题和新引入的问题。

### Round 1 问题处置状态

| R1# | 状态 | 处理方式 |
|-----|------|----------|
| #3 C6消费者约束 | **已修复** | 删除了消费者一致性约束规则，整体重编号 |
| #4 ownerCell vs provider | **已修复** | C4 现在同时约束 ownerCell 和 provider-side actor |
| #5 Cell-Assembly唯一性 | **已修复** | 新增 C12 |
| #6 endpoint字段名验证 | **已修复** | 新增 D11 |
| #8 verify覆盖度 | **已修复** | D7 明确声明是最低门槛 |
| #9 checkRef解析 | **已修复** | D8 明确分层：validate-meta 查格式，verify-slice/run-journey 查运行时 |
| #12 Contract ID歧义 | **已修复** | A3 和 id 格式描述已明确多段解析规则 |
| #14 Lifecycle状态机 | **已修复** | 明确单向转换 draft→active→deprecated |
| #2 Assembly生成字段 | **已修复** | 明确声明 "transition strategy"，缺失时发 warning |
| #1 version冗余 | 残留 | 风险已知，低优先级 |
| #7 Fixture未定义 | 残留 | 无变化 |
| #10 L0 cell在journey | 残留 | 无变化 |
| #11 文档顺序 | 残留 | 仍为 1-4-2-3 |
| #13 CLAUDE.md冲突 | **残留** | CLAUDE.md 仍使用禁止的字段名 |
| #15 删除/升级策略 | 部分解决 | 状态转换已明确，删除策略仍缺 |
| #16 actors.yaml精度 | 残留 | 无变化 |

### Round 2 新发现问题

#### 问题 R2-1（新，Medium）：Slice 目录约定未声明

**角色：架构师 + 工具链开发者**

文档新增了 cell 目录约定（"The cell directory name must equal `cell.id`"），但 slice 没有等价约定。从示例 `allowedFiles: cells/access-core/slices/session-login/**` 可推断 slice 目录名等于 `slice.id`，但未显式声明。

`unit` verify ref 的路径解析依赖 `cells/{cell-id}/slices/{slice-id}/...`，如果 slice 目录名不等于 `slice.id`，解析会失败。

**建议**：在 Layer 3 section 补充 slice 目录约定。

#### 问题 R2-2（新，High）：journey.cells 被称 "exhaustive" 但无完备性验证

**角色：架构师 + DDD 专家**

Precise mode 推导逻辑更新为：

> "This derivation uses `journey.cells` (exhaustive) as the anchor, not `journey.contracts` (curated subset), to avoid false negatives."

但 **没有验证规则** 确保 `journey.cells` 包含所有相关 cell。B2 只验证列出的 cell 存在，不验证完备性。

**场景**：journey J-sso-login 的 contracts 中有 `event.session.created.v1`（subscribers 包含 `audit-core`），但 `journey.cells` 漏写 `audit-core`。precise mode 不会索引 audit-core 的 slices，导致其文件变更时不触发该 journey。

**设计张力**：`journey.contracts` 被声明为 "curated subset"（允许不完备），但 `journey.cells` 被当作 "exhaustive"（要求完备），两者的完备性承诺不一致。

**建议**：要么增加完备性验证（从 journey.contracts 推导必需 cells），要么删除 "exhaustive" 声明。

#### 问题 R2-3（新，Medium）：旧 B8（catalog 验证）被删除后无替代

**角色：QA/验证专家**

上一版 B8 验证 catalog.yaml 一致性。新版替换为 ownerCell 验证，catalog 验证消失。但 `catalog.yaml` 仍被描述为 optional generated artifact。如果 catalog 存在但过时/错误，无工具捕获。

**建议**：恢复 catalog 验证规则，或在 catalog 文件头部加 `# generated — do not edit` 标记并由 validate-meta 检查。

#### 问题 R2-4（新，Medium）：Active contract 无实际使用验证

**角色：QA/验证专家 + 架构师**

`lifecycle: active` 语义为 "at least one client expected"，但无规则验证是否真有 slice 通过 `contractUsages` 引用了 active contract。

- 一个 active contract 可能 endpoints 声明了三个 client cell，但无 slice 实际使用——声明与实现脱节。
- 至少 provider-side 应有一个 slice 声明 provider role 使用。

**建议**：新增规则——active contract 的 provider-side actor 对应 cell 中，至少一个 slice 声明了 provider role 使用。

#### 问题 R2-5（新，Medium）：Verify ref 中 contract-id 解析依赖全局状态

**角色：工具链开发者**

`contract.http.auth.login.v1.serve` 的解析：contract id 是 `http.auth.login.v1`（变长多段），role 是 `serve`（最后一段）。纯字符串解析无法确定 contract id 边界，必须查 contract 注册表。

文档应显式声明：verify ref 解析需要在 validate-meta 加载完所有 contract id 后才能进行。

#### 问题 R2-6（新，Low）：ownerCell != provider 时的治理协作模式未说明

**角色：PM + DDD 专家**

B8 规定 ownerCell 必须是 cell。文档补充了场景说明："a cell may own the lifecycle of a contract whose provider is an external gateway"。但当 ownerCell 团队和 provider 团队不同时，协作模式（谁决定 deprecation？谁执行 breaking change？）未定义。

#### 问题 R2-7（新，Medium）：C9 completeness check 的范围和 routing 盲区

**角色：工具链开发者 + 一线开发者**

C9："All slices within a cell must collectively cover `cells/{cell-id}/slices/` implementation files."

1. `cells/{cell-id}/` 下但不在 `slices/` 下的文件（如 `cells/access-core/internal/shared.go`）不受此规则管辖。Routing matrix 也未覆盖 `cells/{cell}/internal/**` 等中间层级。
2. "implementation files" 定义不明确——`.go`？`_test.go`？`testdata/`？

**建议**：明确 cell 目录结构规范 + 补充 routing 覆盖规则。

### Round 2 问题汇总

| # | 严重度 | 类别 | 状态 | 问题摘要 |
|---|--------|------|------|----------|
| R2-1 | Medium | 缺失约定 | **新** | Slice 目录约定未声明（影响 verify 解析） |
| R2-2 | **High** | 语义矛盾 | **新** | journey.cells 被称 "exhaustive" 但无完备性验证 |
| R2-3 | Medium | 缺失验证 | **新** | catalog.yaml 验证规则被删除后无替代 |
| R2-4 | Medium | 缺失验证 | **新** | active contract 无实际使用验证 |
| R2-5 | Medium | 实现约束 | **新** | verify ref 中 contract-id 解析依赖全局状态（应显式声明） |
| R2-6 | Low | 治理空白 | **新** | ownerCell != provider 时的协作模式未说明 |
| R2-7 | Medium | 边界模糊 | **新** | C9 completeness check 的范围不明 + routing 盲区 |
| R1-1 | Low | 目标违反 | 残留 | version 三重冗余 |
| R1-7 | Medium | 缺失定义 | 残留 | Fixture 概念仍未定义 |
| R1-10 | Low | 缺失规则 | 残留 | L0 cell 可无意义地出现在 journey 中 |
| R1-11 | Low | 文档体验 | 残留 | 层级呈现顺序仍为 1-4-2-3 |
| R1-13 | **High** | 治理冲突 | **残留** | CLAUDE.md 与 V2 字段命名直接矛盾 |
| R1-15 | Medium | 缺失设计 | 部分解决 | Contract 删除/版本升级策略仍缺 |
| R1-16 | Low | 精度不足 | 残留 | actors.yaml 变更路由缺少 diff 级精度 |

### Round 2 团队共识建议

#### P0（阻塞落地）

- **R1-13**：同步 CLAUDE.md 中的 cell/slice 字段名到 V2 规范
- **R2-2**：要么给 journey.cells 加完备性验证，要么删除 "exhaustive" 声明

#### P1（影响工具链实现）

- **R2-1**：补充 slice 目录约定
- **R2-7**：明确 cell 目录结构规范 + routing 覆盖
- **R2-5**：声明 verify ref 解析对 contract 注册表的依赖

#### P2（后续迭代）

- R2-4：active contract 使用验证
- R1-7：fixture 定义
- R2-3：catalog 验证恢复或替代方案
- R1-1、R2-6、R1-10、R1-11、R1-15、R1-16：低优先级优化
