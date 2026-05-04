# GoCell 系统工程逐层审查 — contracts/ 层

| 项 | 值 |
|---|---|
| 日期 | 2026-05-04 |
| 基线 commit | 11600a4f |
| 范围 | `/Users/shengming/Documents/code/gocell/contracts/`（49 条平台契约） |
| 审查维度 | ① 边界、② 职责与内聚、⑨ 可演进性、⑩ 第一性原理 |
| 形态层 | 框架契约（schema 设计 + 演进策略 + 引用闭合） |

## 0. 摘要

contracts/ 层目前最像"成年人"：strict request / loose response 二元模型已通过 ADR `docs/architecture/202605031600-adr-v1-schema-evolution.md` 明确，metadata-only 事件用 `unevaluatedProperties: false` 的白名单（见 `contracts/event/config/entry-upserted/v1/payload.schema.json:26`）落地，孤岛事件 `event.flag.changed.v1` 已转 `lifecycle: deprecated`（`contracts/event/flag/changed/v1/contract.yaml:6`），ADV-05 治理规则给"未连线"的事件留了 draft/deprecated 出口。

但仍存在三个系统工程层级的张力：(a) **kind 4 分法在平台层只用了 2 类**（http 33 + event 16，command/projection 在 platform 为空），分类的"必然性"不是从一阶事实推导出来的，而是从参考实现（Watermill / DDD CQRS）借来的；(b) **ContractMeta 的 SRP 嫌疑**——Kind 是分类标签，Endpoints 是拓扑事实，被合并在同一结构体（`kernel/metadata/types.go:152-178`）并通过 `ProviderEndpoint()` switch-case 解多态，这把"该 kind 用哪几个字段"的知识扩散到了 `EndpointsMeta` 的可选字段森林（`types.go:199-213`，10 个字段中只有 2-3 个会同时被使用）；(c) **v1 → v2 升级路径** 在 ADR 与规则文档中表述清晰（"删除/重命名/类型变更需 v2"），但仓库里没有任何 v2 contract、没有 deprecation 文件夹模板、没有"v1 与 v2 共存期"的注册示例——可演进性是文档承诺，不是验证过的能力。

## 1. 评级表

| 维度 | 评级 | 关键证据 |
|------|------|---------|
| ① 边界（schema strict/loose） | ✅ | ADR §1-§5 全部落地；request strict、response/event loose、metadata-only 用 `unevaluatedProperties` 白名单、shared/errors 例外保留 strict |
| ② 职责与内聚（SRP） | ⚠️ | `ContractMeta` 合并 Kind+Endpoints；`EndpointsMeta` 用 10 个 omitempty 字段表达 4 类拓扑，kind→字段的隐式依赖未在类型层固化 |
| ⑨ 可演进性 | ⚠️ | ADR + api-versioning.md 写了规则，但 0 个 v2 contract、0 个 deprecation 模板、`Lifecycle: deprecated` 事件没有"何时彻底删除"的操作流程 |
| ⑩ 第一性原理 | ⚠️ | 4 类 kind 中 command/projection 在平台层 0 实例，分类来自参考框架而非一阶事实；同步/异步切法可能更小 |

## 2. 问题清单

#### [P1] kind 4 分法在平台层未被两类实例验证
- **维度**：⑩ 第一性原理推导
- **位置**：`/Users/shengming/Documents/code/gocell/kernel/metadata/types.go:154`、`/Users/shengming/Documents/code/gocell/contracts/command/`、`/Users/shengming/Documents/code/gocell/contracts/projection/`
- **复杂度**：Cx3
- **现象**：49 条平台契约里 33 条 http + 16 条 event，command/projection 在 platform 为空（仅 examples/iotdevice 用 command）。`ContractMeta.Kind` 注释列了 4 个值，但平台只验证了 2 个。"必然 4 类"的论证缺位——见第 3 节。这意味着 `EndpointsMeta` 里 `Handler/Invokers/Provider/Readers` 这 4 个字段在平台层从未参与过 ADV-06 校验闭环。
- **建议方向**：要么写一份 ADR 承认"4 类是为 examples 与未来扩展预留"并把 command/projection 的 schema 校验/治理规则降级为可选；要么在平台层造一条 command 契约（如 accesscore 的 token 撤销）来把这条路径走通。

#### [P1] ContractMeta SRP——Kind 与 Endpoints 合并导致可选字段森林
- **维度**：② 职责与内聚
- **位置**：`/Users/shengming/Documents/code/gocell/kernel/metadata/types.go:152-213`
- **复杂度**：Cx3
- **现象**：`EndpointsMeta` 用 10 个 omitempty 字段（Server/Clients/HTTP + Publisher/Subscribers + Handler/Invokers + Provider/Readers）表达 4 类拓扑，kind→字段的合法组合是隐式约束（靠 `ProviderEndpoint()` switch-case 与 governance 规则散布表达）。后果：(1) 新增 kind 必须改 5 处（types/ProviderEndpoint/governance/validate/contractgen）；(2) 错误组合（如 http contract 写了 publisher）要靠运行时规则拦截。
- **建议方向**：把 EndpointsMeta 拆为 `HTTPEndpoints / EventEndpoints / CommandEndpoints / ProjectionEndpoints` 四个独立类型，ContractMeta 用 `oneof`/sealed-style 接口（Go 里用单字段非 nil + 校验函数）承载——把"kind 决定字段"从注释/约定升级为类型约束。

#### [P1] v1 → v2 升级路径无实例、无目录骨架
- **维度**：⑨ 可演进性
- **位置**：`/Users/shengming/Documents/code/gocell/contracts/`（无任何 `/v2/` 目录）；`docs/architecture/202605031600-adr-v1-schema-evolution.md` 全文未提 v2
- **复杂度**：Cx2
- **现象**：api-versioning.md 写"删除/重命名/类型变更 → v2"，但仓库 0 实例。一旦真要升 v2，至少 5 个未答问题：(a) `contracts/http/auth/login/v1/` 与 `/v2/` 同时存在时 ContractMeta.id 是否变成 `http.auth.login.v2`？(b) ownerCell 同时挂两个 contract 是否造成 router 重复挂载？(c) v1 lifecycle 该为 `deprecated` 还是新增 `superseded`？(d) outbox triggers 字段是否要写 `event.session.created.v2`？(e) journeys 里 checkRef `contract.http.auth.login.v1.*` 怎么平滑迁移到 v2？规则承诺与可执行能力之间有缺口。
- **建议方向**：选一条最小成本的 contract（如 audit list）走一遍 v1 → v2 演练，把 ADR、目录约定、validate 规则、journey 引用语法一次性补齐；或写一份 ADR 明确"在 1.0 之前不做 v2 升级，所有变更走 v1 lenient + 字段标注"的策略并删除 api-versioning.md 中"v2"相关段落。

#### [P2] api-versioning.md 与 contract 实际命名不一致（pageSize vs limit）
- **维度**：① 边界
- **位置**：`/Users/shengming/Documents/code/gocell/contracts/http/audit/list/v1/contract.yaml:35-39`、`/Users/shengming/Documents/code/gocell/contracts/http/config/list/v1/contract.yaml:19-23`、`.claude/rules/gocell/go-standards.md`
- **复杂度**：Cx1
- **现象**：所有列表合约都用 `limit` 作为分页大小参数，但规则文档说"`pageSize` 上限 500"。规则文字与代码现状不一致；按 MEMORY "规则不超前于代码库现状"，应改规则文字。
- **建议方向**：把规则文档里 `pageSize` 全部改为 `limit`；或者派 architect 仲裁哪个名字是权威。

#### [P2] Event headers 字段名 snake_case 与 cell-patterns "camelCase" 冲突
- **维度**：① 边界 / ⑨ 可演进性
- **位置**：`/Users/shengming/Documents/code/gocell/contracts/event/session/created/v1/headers.schema.json:7`（`event_id`）
- **复杂度**：Cx1
- **现象**：`.claude/rules/gocell/cell-patterns.md` 明确"HTTP DTO 和事件 payload 统一 camelCase"，但 headers schema 里是 `event_id`（snake_case）。这是 v1 历史遗留——但因为 v1 lenient 演进策略允许"加 optional 字段不破坏"，这种命名差异既不能简单改也没有明确的 v2 演练承担它。
- **建议方向**：作为前面"v1→v2 演练"的搭车项处理，一次性把 envelope 字段统一到 camelCase；或者写明"event envelope headers 不属于 payload，沿用 outbox transport 字段命名（snake_case）"。

## 3. 第 10 维度专项推导

**问题**：从最小不可约事实集，contract 是否必然分为 http / event / command / projection 4 类？

**最小不可约事实集**：跨 cell 通信只有两个一阶事实：
- **F1**：发送方是否需要等待接收方返回结果（同步 vs 异步）
- **F2**：消息是否要被持久化以供回放（瞬时 vs 持久）

这两个维度二乘二可以得到 4 个象限：
| | 同步 | 异步 |
|---|---|---|
| 瞬时 | RPC 风格 | fire-and-forget |
| 持久 | (罕见，事务 RPC) | event sourcing |

**4 类 kind 与象限的对照**：
- **http**：同步 + 瞬时
- **event**：异步 + 持久（outbox 保证）
- **command**：同步 + 瞬时（与 http 同象限，但语义是"做"而非"取/改资源"）
- **projection**：同步 + 瞬时（与 http 同象限，但语义是"读视图"）

可见 **command 和 projection 与 http 落在同一象限**——它们的差异不是底层通信事实差异，而是 DDD CQRS 的语义切分差异（Command 不返回数据；Query/Projection 不修改状态）。这意味着：

1. **任意性切分**：4 类是来自 DDD/CQRS 与 Watermill 的概念借用，而非通信底层的不可约。如果按 F1×F2 切，2 类就够；按"语义意图"切，4 类是其中一种合理选择。
2. **冗余补丁**：`shared/errors/error-response-v1.schema.json` 是 strict/loose 二元拼接的应力痕迹——HTTP 错误信封需要 strict 防杂质，但又必须供 loose response schema 通过 `$ref` 引入；ADR §5 明确把它列为例外，这个例外恰好揭示了"strict/loose 不是 schema 属性而是字段属性"的更深规律。
3. **可推导性反证**：把 ContractMeta.Kind 拆掉、只保留 `Sync bool` + `Persistent bool` 两个布尔——`ProviderEndpoint()` 仍然能从 Endpoints 的 `oneof` 字段推导，治理规则也能用 `(Sync, Persistent) → 必填字段集` 表达。**核心公理（cell 间只通过 contract 通信）不会动摇**。命名上保留 http/event 作为常用别名即可。

**结论**：4 类 kind 不是必然，是历史选择 + 工程便利。当前未来不打算实现 command/projection 的平台契约时，4 分法的成本（types/governance/validate/codegen 5 处分支）大于收益。这一项进入 P1 而不是 P2，是因为它直接放大了问题 #2（SRP）的修复成本。

## 4. 跨层观察

- **contracts ↔ cells**：ADV-06 双向校验（`endpoints.subscribers ↔ contractUsages[role=subscribe]`）覆盖到位，CLAUDE.md 与 cell-patterns.md 都有显式说明。但 **contracts 不知道 cell 的物理位置**，验证依赖 cells 注册时回填——单看 contracts 目录无法判断"这个 publisher 真的会被部署"，假阴性风险靠 ADV-05（active event 必须有 subscriber）兜住，但反向"active http server 必须有 cell 注册"由 governance 别处规则覆盖，链路较长。
- **contracts ↔ journeys**：journeys 通过 `checkRef: contract.<id>.<aspect>` 引用，但 contracts 目录里没有可读的 "aspect 列表"——aspect 命名约定（subscribe/publish/serve/...）散落在 journey runner 实现里。Journey 写错 aspect 不会被 contract 端发现。
- **contracts/ vs pkg/contracts**：前者是权威 yaml + schema，后者放 `HTTPResponse / HTTPTransport / SchemaRefs` 等 Go 类型（见 `kernel/metadata/types.go:215-225` 的 type alias）。边界目前清晰：yaml 是 SoR，pkg/contracts 是为 Go 解析提供的镜像。但要警惕 pkg/contracts 演化出"contract 业务逻辑"（如 schema diff 计算）——一旦发生就会模糊"yaml 是唯一真理源"的承诺。
- **shared/ 当前只有 errors/**：ADR §5 解释为什么它只此一项。如果未来 metadata-only 事件演化出多个共享 envelope 模式（比如 audit envelope、tracing envelope），shared/ 子目录的演化策略需要明确——是按 "envelope/" 分目录还是按 "domain/" 分目录。

## 5. 一句话结论

contracts/ 层的 schema 设计与演进策略已属于框架层的**示范性工程实践**，但 4-kind 分类的必然性、ContractMeta 的 SRP、v1→v2 演练 这三块从"承诺"到"被验证的能力"之间还有缺口；建议把"是否保留 command/projection 平台契约位"作为下一次 architect 仲裁的输入项，并把 v1→v2 演练列入 029 roadmap 的 G 轨道。
