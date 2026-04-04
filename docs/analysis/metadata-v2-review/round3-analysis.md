# Metadata Model V2 Round 3 Analysis

## Scope

本轮只分析 `docs/architecture/metadata-templates-v2.md`。

不假设其他文档、代码或历史结论是正确的。

## Conclusion

当前文档存在结构性问题，建议启动团队重新分析，而不是继续做局部修补。

问题的核心不在字段命名，而在以下四点没有稳定下来：

1. 这份文档到底是在定义模板，还是在定义元数据模型、工具输入契约和治理规则。
2. 哪些事实是 canonical，哪些是 default、effective、generated。
3. `journey`、`contract`、`slice`、`assembly` 各自承担什么责任。
4. 工具链是否真的能从当前 canonical 数据中可靠推导验证、路由和边界。

## Findings

### 1. Severe: 文档没有真正定义 “template”

标题是 Metadata Templates V2，但正文主要在定义：

- 具体元数据对象
- 校验规则
- 工具输入
- 治理约束

缺失模板系统最基本的概念：

- 参数
- 实例化
- 组合
- 覆盖规则
- 模板版本

从第一性原理看，问题定义已经偏了。当前文档更像“metadata model / registry spec”，不是“template spec”。

参考：

- `metadata-templates-v2.md:5`
- `metadata-templates-v2.md:112`
- `metadata-templates-v2.md:249`
- `metadata-templates-v2.md:398`

### 2. Severe: “single source of truth” 被文档自己打破

文档要求 every fact has exactly one canonical owner，但同时又存在：

- inherit
- override
- generated
- hand-curated override
- transition strategy

这说明当前最缺的不是更多规则，而是值层次定义。至少应明确区分：

- declared
- default
- effective
- generated

如果不先拆清这四层，后面所有 “single source of truth” 都会继续失真。

参考：

- `metadata-templates-v2.md:70`
- `metadata-templates-v2.md:79`
- `metadata-templates-v2.md:85`
- `metadata-templates-v2.md:342`
- `metadata-templates-v2.md:651`

### 3. Severe: contract 的治理 owner、运行时 provider、实现 slice 被混为一谈

当前规则把三个不同概念耦合在一起：

- `ownerCell`: 谁治理 contract 生命周期
- provider-side actor: 谁在运行时提供能力
- provider-role slice: 谁在实现层承接 contract

这三个概念不是同一个东西。

最直接的矛盾是：

- provider-side actor 允许是 external actor
- provider-role slice 要求 `slice.belongsToCell == provider actor`
- active contract 又要求至少存在一个 provider-role slice

三条同时成立时，external provider contract 无法表达。

参考：

- `metadata-templates-v2.md:408`
- `metadata-templates-v2.md:432`
- `metadata-templates-v2.md:436`
- `metadata-templates-v2.md:782`
- `metadata-templates-v2.md:794`

### 4. Severe: `journey` 的定义不足以支撑 precise impact routing

文档明确说 `journey.contracts` 是 curated focus subset，不是 exhaustive set。

但 precise mode 又希望通过：

`journey.cells -> slice.contractUsages -> contracts`

推导精确的 `slice -> journey` 关系。

这个推导不成立，因为：

- curated subset 不能证明完整影响面
- `journey.cells` 只是 cell 粒度，天然比 slice 粒度粗
- 从粗粒度参与集合反推细粒度依赖关系，只能得到启发式，不可能得到严格精确结果

所以这里不应该叫 precise mode，最多只能叫 heuristic mode。

参考：

- `metadata-templates-v2.md:152`
- `metadata-templates-v2.md:734`
- `metadata-templates-v2.md:746`
- `metadata-templates-v2.md:793`

### 5. High: `journey.cells` completeness 规则不自洽

文档试图用 C13 证明 `journey.cells` 是 exhaustive set，但前提并不成立：

- `journey.contracts` 不是全量
- contract client 允许 `["*"]`

对 open contract 来说，“所有 client-side actors” 本身就是未定义集合。对 curated subset 来说，也无法从子集证明全集完整。

因此 C13 不是 completeness proof，只是弱校验。

参考：

- `metadata-templates-v2.md:152`
- `metadata-templates-v2.md:433`
- `metadata-templates-v2.md:793`

### 6. High: verify 标识不是自描述的

`contract.{contract-id}.{role}` 无法仅凭字符串完成解析，文档也承认必须依赖“已加载的 contract ID 集”做最长匹配。

这说明 verify 标识不是封闭 grammar，而是 context-sensitive encoding。

此外，规则和示例还有冲突：

- 规则说去掉 category 后，其余 segments 构成 scope
- 示例却把 `contract.http.auth.login.v1.serve` 解析成 `TestContract_serve`

这会让 `verify-slice`、`run-journey` 和测试命名实现出现二义性。

参考：

- `metadata-templates-v2.md:371`
- `metadata-templates-v2.md:377`
- `metadata-templates-v2.md:386`
- `metadata-templates-v2.md:388`

### 7. High: generated artifact 被当作依赖输入，但没有一致性模型

文档依赖或允许多种 generated artifact：

- assembly boundary views
- smokeTargets
- `generated/indexes/journey-slice-map.yaml`

但没有定义：

- 来源集合
- 指纹
- 失效条件
- 原子更新语义
- 工具如何拒绝读取过期结果

只要 generated artifact 会被工具消费，它就已经是系统状态的一部分，而不只是展示层产物。

参考：

- `metadata-templates-v2.md:85`
- `metadata-templates-v2.md:86`
- `metadata-templates-v2.md:651`
- `metadata-templates-v2.md:653`
- `metadata-templates-v2.md:736`
- `metadata-templates-v2.md:746`

### 8. High: 模型边界是按工具切的，不是按事实类型切的

当前文档把以下几类东西放进一个统一模型：

- architecture facts
- execution binding
- delivery tracking
- build and packaging

它们的 owner、变化频率、验证方式不同。混在一起会让“测试和交付变化”伪装成“架构变化”。

参考：

- `metadata-templates-v2.md:11`
- `metadata-templates-v2.md:95`
- `metadata-templates-v2.md:179`
- `metadata-templates-v2.md:369`
- `metadata-templates-v2.md:614`

### 9. Medium-High: `slice.contractUsages` 语义过载

`contractUsages` 现在至少承担了三种角色：

- 描述 slice 会接触哪些 contract
- 证明 slice 在 provider / client 侧参与某 contract
- 为 impact routing 提供依赖边

但这三件事并不等价。

如果不区分：

- usage edge
- ownership edge
- verification coverage edge

后续规则会越来越依赖隐含推理。

参考：

- `metadata-templates-v2.md:300`
- `metadata-templates-v2.md:366`
- `metadata-templates-v2.md:794`

### 10. Medium-High: `consistencyLevel` 更像标签，不是可执行约束

文档反复使用 `consistencyLevel` 做约束，但没有给出足够操作性定义，例如：

- 写入确认语义
- 读己之写保证
- 重试与幂等边界
- 顺序要求
- 补偿语义
- 验证证据要求

如果这些没有落到执行或验证层，`L1/L2/L3` 只是标签，不是可证明能力。

参考：

- `metadata-templates-v2.md:78`
- `metadata-templates-v2.md:79`
- `metadata-templates-v2.md:409`
- `metadata-templates-v2.md:504`
- `metadata-templates-v2.md:806`

### 11. Medium: fixture 已进入运行模型，但没有进入完整 routing / failure model

文档已经把 fixture 作为 `run-journey` 的真实输入，但仍缺失：

- fixture 文件变化如何触发相关 journey
- setup 失败如何回滚
- teardown 失败如何处理
- 并发执行时如何隔离端口、数据库、topic 等资源

这说明 journey 执行模型只定义了静态引用，没有定义运行时语义。

参考：

- `metadata-templates-v2.md:181`
- `metadata-templates-v2.md:209`
- `metadata-templates-v2.md:711`

### 12. Medium: status-board 没有完全闭环

动态字段命名仍有漂移：

- 前文列的是 `lastUpdated`
- 实际 schema 用的是 `updatedAt`

同时没有完整定义：

- `journeyId` 是否唯一
- 是否允许缺项
- 是否要求每个 journey 必须恰好对应一条状态

说明 delivery-only 数据虽然被隔离出来了，但对象边界还没稳定。

参考：

- `metadata-templates-v2.md:56`
- `metadata-templates-v2.md:216`
- `metadata-templates-v2.md:805`

## 建议团队重开的核心议题

1. 先重定义问题边界：这是模板系统，还是元数据注册表 / 架构索引 / 测试编排模型。
2. 拆分模型层次：至少分成 architecture core、execution binding、delivery tracking、build and packaging。
3. 定义值层次：declared、default、effective、generated。
4. 重定义 `journey`：它是 acceptance spec、runnable test plan，还是 impact graph anchor。
5. 重定义 `contract` 参与关系：区分 governance owner、runtime provider / consumer actor、implementation slice、verification coverage。
6. 重新设计 precise routing：如果要真正精确，就引入显式 canonical 关系，不要从 curated subset 反推 exhaustive topology。
7. 给 generated artifact 建一致性模型：来源、指纹、失效、原子更新、读取策略。
8. 给 `consistencyLevel` 下操作性定义，否则它不能成为可靠的验证输入。

## Priority

如果团队只先处理三件事，建议顺序如下：

1. 重新定义问题边界与模型分层。
2. 拆清 `journey`、`contract`、`slice` 的语义责任。
3. 停止把 curated data 当作 precise routing 的输入。
