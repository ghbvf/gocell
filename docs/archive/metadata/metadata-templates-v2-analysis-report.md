# Metadata Templates V2 第一性原理分析报告

## 范围

本报告只分析 [metadata-templates-v2.md](./metadata-templates-v2.md)。

不假设其他文档、代码或历史版本是正确的。

## 结论

当前 V2 文档存在结构性问题，不适合继续做局部修补，应该启动团队重新分析。

问题的根源不是字段命名，也不是个别规则缺失，而是以下几件事没有稳定下来：

1. 这份文档到底是在定义模板，还是在定义实例元数据模型、工具输入契约和治理规则。
2. 哪些事实是 canonical，哪些只是 default、effective 或 generated view。
3. `journey`、`contract`、`slice`、`assembly` 分别承担什么语义责任。
4. 工具链是否真的能从当前 canonical 数据中可靠推导出验证、路由和边界信息。

## 主要发现

### 1. 严重: 文档没有真正定义“template”

标题叫 Metadata Templates V2，但正文几乎全部在定义具体元数据对象、校验规则和工具输入，而不是模板机制本身。

缺失的模板核心概念包括：

- 参数
- 实例化
- 组合
- 覆盖规则
- 模板版本

这说明问题定义一开始就偏了。当前文档更像“元数据注册表规范”，不是“模板规范”。

参考：

- [metadata-templates-v2.md:5](./metadata-templates-v2.md#L5)
- [metadata-templates-v2.md:112](./metadata-templates-v2.md#L112)
- [metadata-templates-v2.md:249](./metadata-templates-v2.md#L249)
- [metadata-templates-v2.md:398](./metadata-templates-v2.md#L398)

### 2. 严重: “single source of truth” 被文档自己打破

文档强调 every fact has exactly one canonical owner，但同时又大量使用：

- inherit
- override
- generated
- optional hand-curated override
- transition strategy

这说明当前真正缺失的不是更多规则，而是值层次定义。至少应区分：

- declared value
- default value
- effective value
- generated view

如果不先拆清这四层，后面所有“唯一事实源”的表述都会继续失真。

参考：

- [metadata-templates-v2.md:70](./metadata-templates-v2.md#L70)
- [metadata-templates-v2.md:79](./metadata-templates-v2.md#L79)
- [metadata-templates-v2.md:85](./metadata-templates-v2.md#L85)
- [metadata-templates-v2.md:342](./metadata-templates-v2.md#L342)
- [metadata-templates-v2.md:651](./metadata-templates-v2.md#L651)

### 3. 严重: contract 的治理 owner、运行时 provider、实现 slice 被混为一谈

当前规则中至少混了三个不同概念：

- `ownerCell`: 谁治理 contract 生命周期
- provider-side actor: 谁在运行时提供边界能力
- provider-role slice: 谁在实现层承接这个 contract

这三个概念不是同一个东西，但文档把它们耦合起来了。

最直接的矛盾是：

- provider-side actor 允许是 external actor
- provider-role slice 要求 `belongsToCell == provider actor`
- active contract 又要求至少存在一个 provider-role slice

三条同时成立时，external provider contract 无法表达。

参考：

- [metadata-templates-v2.md:408](./metadata-templates-v2.md#L408)
- [metadata-templates-v2.md:432](./metadata-templates-v2.md#L432)
- [metadata-templates-v2.md:436](./metadata-templates-v2.md#L436)
- [metadata-templates-v2.md:782](./metadata-templates-v2.md#L782)
- [metadata-templates-v2.md:794](./metadata-templates-v2.md#L794)

### 4. 严重: `journey` 的定义不足以支撑“precise” impact routing

文档明确说 `journey.contracts` 是 curated focus subset，不是 exhaustive set。

但后面又希望通过 `journey.cells -> slice.contractUsages -> contracts` 推导精确的 `slice -> journey` 关系。

这个推导从第一性原理上站不住，因为：

- curated subset 不能证明完整影响面
- `journey.cells` 只是 cell 粒度，天然比 slice 粒度更粗
- 从粗粒度参与集合反推细粒度依赖关系，只能得到启发式，不可能得到严格精确结果

因此这里不应该叫 precise mode，最多只能叫 refined heuristic mode。

参考：

- [metadata-templates-v2.md:152](./metadata-templates-v2.md#L152)
- [metadata-templates-v2.md:734](./metadata-templates-v2.md#L734)
- [metadata-templates-v2.md:746](./metadata-templates-v2.md#L746)
- [metadata-templates-v2.md:793](./metadata-templates-v2.md#L793)

### 5. 高: `journey.cells` completeness 规则不自洽

文档想用 C13 证明 `journey.cells` 是 exhaustive set，但前提并不成立：

- `journey.contracts` 不是全量
- contract client 允许 `["*"]`

对 open contract 来说，“所有 client-side actors” 本身就是未定义集合；对 curated subset 来说，“从子集证明全集完整”也不成立。

所以 C13 不是 completeness proof，只是一个有限条件下的弱校验。

参考：

- [metadata-templates-v2.md:152](./metadata-templates-v2.md#L152)
- [metadata-templates-v2.md:433](./metadata-templates-v2.md#L433)
- [metadata-templates-v2.md:793](./metadata-templates-v2.md#L793)

### 6. 高: verify 标识不是自描述的，工具解析依赖外部上下文

`contract.{contract-id}.{role}` 的解析无法仅靠字符串本身完成，文档也明确承认要依赖“已加载的 contract ID 集”做最长匹配。

这说明 verify 标识不是封闭 grammar，而是 context-sensitive encoding。

此外，示例与命名规则还有冲突：

- 规则说去掉 category 后，其余 segments 构成 scope
- 示例却把 `contract.http.auth.login.v1.serve` 解析到 `TestContract_serve`

这会让 `verify-slice`、`run-journey`、测试命名约定在实现时产生二义性。

参考：

- [metadata-templates-v2.md:371](./metadata-templates-v2.md#L371)
- [metadata-templates-v2.md:377](./metadata-templates-v2.md#L377)
- [metadata-templates-v2.md:386](./metadata-templates-v2.md#L386)
- [metadata-templates-v2.md:388](./metadata-templates-v2.md#L388)

### 7. 高: generated artifact 被当作依赖输入，但没有一致性模型

文档允许或依赖多种 generated artifact：

- assembly boundary views
- smokeTargets
- `generated/indexes/journey-slice-map.yaml`

但没有定义：

- 来源集合
- 指纹
- 失效条件
- 原子更新语义
- 工具读取时如何确认视图是新鲜的

只要 generated artifact 会被工具消费，它就已经不只是“展示层产物”，而是系统状态的一部分。当前这里没有闭环。

参考：

- [metadata-templates-v2.md:85](./metadata-templates-v2.md#L85)
- [metadata-templates-v2.md:86](./metadata-templates-v2.md#L86)
- [metadata-templates-v2.md:651](./metadata-templates-v2.md#L651)
- [metadata-templates-v2.md:653](./metadata-templates-v2.md#L653)
- [metadata-templates-v2.md:736](./metadata-templates-v2.md#L736)
- [metadata-templates-v2.md:746](./metadata-templates-v2.md#L746)

### 8. 高: 模型边界是按工具切的，不是按事实类型切的

当前文档把下面几类东西放进了一个统一模型：

- architecture facts
- execution bindings
- delivery tracking
- build and packaging

它们的 owner、变化频率、验证方式都不同。把它们混在一起，会让“测试和交付变化”伪装成“架构变化”。

这是模型边界不稳，而不是文档章节排版问题。

参考：

- [metadata-templates-v2.md:11](./metadata-templates-v2.md#L11)
- [metadata-templates-v2.md:95](./metadata-templates-v2.md#L95)
- [metadata-templates-v2.md:179](./metadata-templates-v2.md#L179)
- [metadata-templates-v2.md:369](./metadata-templates-v2.md#L369)
- [metadata-templates-v2.md:614](./metadata-templates-v2.md#L614)

### 9. 中高: `slice.contractUsages` 语义过载

`contractUsages` 现在至少承担了三种角色：

- 描述 slice 会接触哪些 contract
- 证明某个 slice 在 provider 或 client 侧参与该 contract
- 为 impact routing 提供依赖边

但这三件事并不等价。

如果不区分：

- usage edge
- ownership edge
- verification coverage edge

那后续规则就只能不断依赖隐含推理，越补越复杂。

参考：

- [metadata-templates-v2.md:300](./metadata-templates-v2.md#L300)
- [metadata-templates-v2.md:366](./metadata-templates-v2.md#L366)
- [metadata-templates-v2.md:794](./metadata-templates-v2.md#L794)

### 10. 中高: `consistencyLevel` 目前更像标签，不是可执行约束

文档反复使用 `consistencyLevel` 做约束，但没有给出足够操作性定义。例如：

- 写入确认语义
- 读己之写保证
- 重试与幂等边界
- 顺序要求
- 补偿语义
- 验证证据要求

如果这些没有落到执行或验证层，`L1/L2/L3` 就只是宣称，不是可证明能力。

参考：

- [metadata-templates-v2.md:78](./metadata-templates-v2.md#L78)
- [metadata-templates-v2.md:79](./metadata-templates-v2.md#L79)
- [metadata-templates-v2.md:409](./metadata-templates-v2.md#L409)
- [metadata-templates-v2.md:504](./metadata-templates-v2.md#L504)
- [metadata-templates-v2.md:806](./metadata-templates-v2.md#L806)

### 11. 中: fixture 已经进入运行模型，但没有进入完整 routing / failure model

文档已经把 fixture 作为 `run-journey` 的真实输入，但仍缺失：

- fixture 文件变化如何触发相关 journey
- setup 失败如何回滚
- teardown 失败如何处理
- 并发执行时如何隔离端口、库、topic 等资源

这说明 journey 执行模型只定义了静态引用，没有定义运行时语义。

参考：

- [metadata-templates-v2.md:181](./metadata-templates-v2.md#L181)
- [metadata-templates-v2.md:209](./metadata-templates-v2.md#L209)
- [metadata-templates-v2.md:711](./metadata-templates-v2.md#L711)

### 12. 中: status-board 也没有完全闭环

文档在动态字段命名上仍有漂移：

- 前面列的是 `lastUpdated`
- 实际 schema 用的是 `updatedAt`

同时没有看到对 `journeyId` 唯一性、是否允许缺项、是否要求一旅程一条状态记录的完整定义。

这说明 delivery-only 数据虽然被隔离出来了，但对象边界还没完全稳定。

参考：

- [metadata-templates-v2.md:56](./metadata-templates-v2.md#L56)
- [metadata-templates-v2.md:216](./metadata-templates-v2.md#L216)
- [metadata-templates-v2.md:805](./metadata-templates-v2.md#L805)

## 建议重开的核心议题

### 1. 先重定义问题边界

先回答一句话问题：

这是要做“模板系统”，还是“元数据注册表 / 架构索引 / 测试编排模型”？

如果答案不是模板系统，建议直接改名，避免继续围绕错误问题定义演化。

### 2. 拆分模型层次

至少拆成四层：

- architecture core
- execution binding
- delivery tracking
- build and packaging

不要继续把目标态模型和过渡期迁移策略混在一个规范里。

### 3. 定义值层次

为每个字段明确它属于哪一层：

- declared
- default
- effective
- generated

没有这层定义，就不要再宣称 every fact has exactly one canonical owner。

### 4. 重定义 `journey`

明确它到底是：

- acceptance spec
- runnable test plan
- impact graph anchor

如果三者都需要，建议拆对象，而不是让一个对象承担三套推理责任。

### 5. 重定义 `contract` 参与关系

至少区分：

- governance owner
- runtime provider / consumer actor
- implementation slice
- verification coverage

否则 external actor、provider slice、ownerCell 的矛盾还会反复出现。

### 6. 重新设计 precise routing

如果要真正精确，就必须引入可验证的显式 canonical 关系。

不要再试图从 curated subset 反推 exhaustive topology。

### 7. 给 generated artifact 建一致性模型

至少明确：

- 由哪些源文件生成
- 如何计算指纹
- 何时失效
- 如何原子更新
- 工具如何拒绝读取过期结果

### 8. 给 `consistencyLevel` 下操作性定义

否则它只是标签，不是约束，也无法成为可靠的验证输入。

## 优先级建议

如果团队只先处理三件事，建议顺序如下：

1. 重新定义问题边界与模型分层。
2. 拆清 `journey`、`contract`、`slice` 的语义责任。
3. 停止把 curated data 当作 precise routing 的输入。
