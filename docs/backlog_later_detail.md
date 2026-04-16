# GoCell V1.1+ 长期规划详解

> 本文档通过抽取 `docs/reviews/archive/` 中的历史记录（包含 `202604111438-backlog-full-history.md`、`20260414-backlog-pre-wave-restructure.md`、`202604121800-backlog-pre-restructure.md` 等），详细还原了 `docs/backlog.md` 中简略带过的 v1.1 及更长远的规划任务详情与**原始详细分析决策过程**。

---

## 1. Metadata 校验规则补全 (G-1 ~ G-6)
V3 元数据模型的架构护栏，以确保配置约束能够下沉到自动化工具链中。

| 任务 ID | 等级 | 详细分析与决策依据 |
|---------|------|--------------------|
| **G-1 (FMT-11)** | HIGH | 动态状态字段（如 readiness/risk/blocker）禁入非 `status-board` 文件，严格隔离项目治理数据与源码框架事实。之前的评审发现大量的元数据污染了核心描述文件。 |
| **G-2 (TOPO-07)** | MEDIUM | 校验 `actor.maxConsistencyLevel` 约束，当前代码中（`kernel/metadata/parser.go`）已经完成了解析，但实际校验阶段并未阻断非合规一致性级别的配置，存在旁路安全隐患。 |
| **G-4** | MEDIUM | 针对被标记为 `deprecated` 状态 contract 的依赖引用阻断。当前的 CI 仅发出 warning 警告，未能发挥强制生命周期管理作用。v1.1 需升级为主动 break 构建逻辑。 |
| **G-6** | LOW | 校验派生文件 `assembly boundary.yaml` 的存在性与一致性。该文件属于生成系文件，只作为辅助验证，故降低至 LOW。 |

## 2. Kernel 子模块补全
原规划于 Phase 4 实施但被界定为 v1.1 范围的框架核心扩展机制：

| 子模块 | 等级 | 详细分析与决策依据 |
|--------|------|--------------------|
| **kernel/wrapper** | P1 | 契约级可观测代理（Traced wrapper），在应用边界处理自动追踪链路等切面逻辑。此前在 `URLParam` 剥离重构时暂时延后，现作为提高可溯源性必须补全。 |
| **kernel/command** | P1 | 提供命令队列接口的支持，补充框架级别的 L4 操作基建（当前 `device-cell` 等 L4 下发仅依赖适配器透传，缺统一底座）。 |
| **kernel/webhook** | P2 | Webhook 出站请求（Event Delivery）标准的 Receiver 与 Dispatcher 抽象模型（需在 Relay 重写稳定后执行，同时需防御 SSRF 等内部白名单攻击）。 |
| **kernel/reconcile** | P2 | 针对 L3 弱规范下最终状态的收敛控制循环（Reconciler 模式）。目前仅无实际业务紧迫需求，放于 P2 备赛。 |
| **runtime/scheduler** | P2 | 提供 Cron 表达式和完整定时任务支持（当前系统内的 PeriodicWorker 仅支持固定间隔，缺乏分布式防重及并发或 Cron 调度能力）。 |
| **kernel/replay** | P3 | 事件溯源所需的投影重算（Projection rebuild）依赖机制，保证 CQRS 下的强一致数据订正能力（依赖 Consumer 模型非常稳定）。 |
| **kernel/rollback** | P3 | Rollback 元数据模型与跨事件的撤回原语。 |

## 3. Adapters 分层重整 (AL-01 ~ AL-04)
将混合了“领域调度”与“SDK 胶水代码”的 Adapter 切分为核心 Runtime 抽象与纯粹存储访问层（隔离框架基础设施核心逻辑）：

* **AL-01 (Outbox Relay):** `adapters/postgres/outbox_relay.go` 的轮询调度逻辑属于框架通用 runtime 循环，需拆出至 `runtime/outbox/relay.go`。Adapter 侧仅应当聚焦于具体的 SQL 存取实现（Store API）。
* **AL-02 (DistLock):** `adapters/redis/distlock.go` 的续期 goroutine 控制和 TTL 刷新策略应抽象出通用的 DistLock 接口放入 runtime 包中，Redis 层只留存 NX/Eval 基础原语。
* **AL-04 (Auth Module):** `runtime/auth` 目前直接强制依赖了 `golang-jwt` 第三方库，违反了包层级的核心依赖抽象隔离约定。这部分作为技术债需评估是否值得抽象过深（JWT已是事实标准）。
* **RMQ-STATUS-01 (P2):** 当前 Dashboard RabbitMQ 集成使用的 `ConnectionStatus()` 直接返回裸的 enum 类型（未带任何上下文）；必须重构为结构化的 `ConnectionState`（携带包括 state, message, lastError 的详细监控追踪排查字段），以便上游可视化能够体现诊断级数据。

## 4. 架构风险与设计缺口 (Architectural Risks)

| 风险点 | 诊断依据与解决方案规划 |
|--------|------------------------|
| **Cell 接口拆分** | 现有的基础 Cell 接口变得极度膨胀（已包含 12 个独立方法），混合了 metadata accessor 与 lifecycle 控制。后期需要结合 ISP 原则重构，切分为 `Cell`、`CellLifecycle` 以及 `CellMetadata` 三个精简模块。 |
| **Adapter 集成覆盖** | 检测到当前 6 个主干 Adapter 中留存了多达 15 个 `t.Skip` 的黑盒测试盲区，这些缺乏用例保护的逃逸边界在真实流量下可能引起预期外 Panic，需补齐 Testcontainer。 |
| **ER-ARCH-01 (C4架构风险)**| Router 的启动探测逻辑目前使用了写死的 `time.After(500ms)` 的硬编码时序推断（Heuristics）来探测 RabbitMQ Subscribe 的 Topology Setup (Qos/Declare/Bind/Consume)。如果跨可用区网络稍慢，会导致框架判断启动完成但实则未准备好的竞态故障。根因：必须从架构协议级别将 Subscriber 的接口抽象拆分出同步等待的 `Setup()` 及后续异步消费的 `Run()` 双阶段设计。 |
| **L3 (WorkflowEventual) 示例代码缺口** | 当前系统缺少完全使用 L3 一致性级别（例如“纯读模型投影”或“独立异步消费汇聚流”模型）的官方 Reference Cell。产生该缺口的核心原因是框架层面决议驳回了 `GAP-8 (框架级 CQRS)` 及 `WM-29 (Saga 补偿)`（避免引入过重的中心协调器）；这导致绝大部分跨服务一致性流转都被强行收敛在了 L2 级别的 Outbox Handler 中去依附完成。后续需要在 `examples/` 目录下官方补齐标准且优雅的 L3 Projection 样板代码，防止业务开发时因缺少参照，而将超复杂流转错误地塞入单体 L2 事务。 |

## 5. 契约增强体系 (Contracts Management)
为了使契约模型能够完全与现代化的大型声明式治理生态打通，演进为真正的 API 资产管理库：
* **CONTRACT-BREAKING-01:** 新增命令 `gocell check contract-breaking`。借鉴 buf.build 策略引入 40+ 条 API Schema 历史破坏性变更比对规则（例如字段删除、必填放宽等进行强制阻断）。
* **CONTRACT-CODEGEN-01:** 实现 API 结构体的原生自动双向推断；支持将 Go DTO 的 Struct Tags 实时一键导出 / 双写更新到 JSON Schema（解决代码与契约 YAML 分裂的窘境，对齐 oapi-codegen 体验）。
* **CONTRACT-STUB-01:** 引入 Consumer-Driven Contract 开发范式。提供针对消费方的 Contract Stub 桩代码校验套件（模拟 Spring Cloud Contract WireMock/Pact），以真实测试约束微服务调用端的模拟一致性。

## 6. 技术规格债务 (Spec Tech-Debt)
* **CONTRACT-META-01 (P1):** API 传输层的语义被大幅削弱；当前的 `contract.yaml` 只能支持描绘 Body JSON 的格式限制。必须增强为一等公民的独立描述，补充对：`Method / Path / PathParams / QueryParams / SuccessStatus / NoContent` 等隐式传输逻辑的静态界定（对标 Kratos Method Binding / goa）。
* **C-AC7 (P2):** 当前授权体系生成的 JWT 中缺乏 `jti`（JWT ID）声明。这导致业务一旦遭受单 Token 攻击无法被单独列入黑名单撤销（Revoke），运维只能采用清空全局会话这种毁灭打击的重构动作。
* **C-L6 (P2):** 核心约束名称断裂；代码 Scaffold 脚手架工具与自动化的元数据 Generator 因为开发者上下文脱节互相打架（CLI 使用点分全名如 `http.auth`，Generator 内部退化成斜杠分割），需要全局检索并统一内部的 Contract ID 解析标准。
* **C-DC9 (P2):** `audit-core` 服务下的 `auditarchive` 处理接口目前是占据位置的死代码靶子（返回固定的 `ErrNotImplemented`），真实的 AWS S3 对象存储 Adapter 基础设施其实已经跑通，中间业务层漏接了导出的链路，属于烂尾代码需打通。
* **DURABLE-TYPE-01:** 由于类型抹除在 `assembly` 打包阶段才会校验，当前 L2/L3的持久化级别检测只能退化为程序启动瞬间的 动态强行 Panic（Fail-fast）。应当深入探索如何在类型系统层面提供强有力的静态编译保护（仓储级能力推断）。

## 7. WinMDM 跨团队需求 (Defer / V2+)

经过架构师、安全、测试、运维、DX (开发者体验)、产品等**六席位联合会审**，针对 WinMDM 团队提出的 34 项附加改造需求逐一评估，形成了明确的留放机制（拒绝为不符合框架核心属性的特性买单）。

### 延后至 V1.1/P2 的待定需求 (Defer V1.1 依据六席位审议票型):
| # | 需求项 | 票数 | 详细拒接/延期分析 |
|---|--------|------|-------------------|
| **WM-32** | mTLS 中间件 | 4/6 | 安全席位提出这是 MDM 设备双向认证安全的硬约束，但架构及 DX 席位认为：大规模环境下的 mTLS 卸载通常前置放在 K8s 反向代理 / 服务网格（Service Mesh）解决，不应在应用 HTTP 中重做了。V1.1 取折中：可以加一套 TLS 构建器并在 HTTP 加证书提取钩子。 |
| **WM-4** | Webhook 出站 | 4/6 | 基础设施未稳：出站体系由于需附加 HMAC 的安全认证及严峻的 **SSRF 黑白名单防范机制**，其执行依赖当前未稳固的 L3 Outbox Relay 能力，因此放在后续专门的 P2 批次实施。 |
| **WM-18** | 延迟消息原语 | 3/6 | 架构与产品通过，但测试及运维反对。因支持特定延时的话（TTL）、RabbitMQ 就必须绑定 `x-delayed-message` 第三方环境插件方可；而内存级的 Pub/Sub 测试桩根本无法模拟分布式的计时器。此功能导致系统运维成本成倍拉升，延迟至底座 Outbox 彻底稳定后再探索。 |

### 永久封存 / 不予接入的重量级（Reject / V2+ 依据六席位审议票型）:

*判断通用框架标准的基线：**“如果将本项目平移到一个非 MDM 项目（如电商、SaaS 或 IoT 设备联动中），至少有除了设备管理外的2个领域场景具备完全同等诉求，才可界限为框架级公共能力”。***

| ID | 特性需求 | 票数 | 对标及框架拒绝理由与 WinMDM 的替代方案（Workarounds） |
|---|--------|------|-------------------------------------------------------|
| **WM-3** | X.509/CA | 1/6 | **MDM 专属。** CA 签名册发、CRL 发布、SCEP 协议都是设备管理重资产特有场景，整套 X.509 PKI 足以单立成为一套极其复杂的发行平台，强行揉入框架内核极其危险。**替代方案：** WinMDM 的 `cert-core` 自己建立领域代码包装，或接入外围组件（如 `step-ca`）。 |
| **WM-14**| Codec 注册表 | 1/6 | **YAGNI (You aren't gonna need it) 镀金。** GoCell 当前坚定的执行了 JSON 首发原则；引入 Content-Type（多维的 XML/SOAP 解析框架）会波及改写 20 个测试 Handler。而要求这点的起因只是 MDM 所用老旧的 `OMA-DM SyncML` 协议强行捆绑。**替代方案：** WinMDM 各自的 Handler 层独立手工维护非主流 Decode 逻辑，别污染主 Http。 |
| **WM-21**| 拦截器 Mixin | 2/6 | **非 Go 惯用法则（Non-idiomatic）。** Go 本质语言的共享手段依赖于强大的 Embedding + Interface。搞复杂的类似 Python Hook 层组合树反人类。如有横向逻辑应当用现有 HTTP 或 Consumer Middleware 层（比如 PR#68）解决掉。 |
| **WM-24**| Policy Engine | 1/6 | **CA 验证专属体系。** 通用全量策略引擎（OPA / Casbin / Cedar）作为独立基础产品介入，带 DSL 解析引擎、生命周期状态树。GoCell 最大边界是支持出一个简单的适配器接口钩子。**替代方案：** WinMDM 以一个特定内部服务的形式封装 Casbin 使用。 |
| **WM-26**| 拓扑广播 FanOut | 0/6 | 纯属于底层 MQ 代理服务器自带的网络分发（如 RMQ 的 Exchange 机制）。业务框架内部不需要重新抽象一套分发流池，徒增多一层代理损耗。 |
| **WM-28**| 服务发现 (GAP-2) | 0/6 | 违背基础云原生生态现状。现在的系统大多运行于 K8s 集群，自带了强大的 Service 分发能力以及网络路由劫持。内部自己建立重型的注册中心完全是脱库（过早的抽象）。|
| **WM-29**| Saga 分布式事务 | 0/6 | 框架已经引入并实现了非常稳固的 L2 Outbox 本地关联入库 + 后台 L3/L4 最终一致的轮询处理。为了一个低频场景引入超吃资源的微服务跨库全局 Saga 协调者与回放流程系统，直接拖垮代码复杂度。 |
| **GAP-1**| gRPC 双协议栈 | 0/6 | 未遇到性能或网络瓶颈的情况下暂无重写全部路由序列化传输层的动力（纯无明确价值的大重构镀金行为）。 |
| **GAP-8**| 框架级 CQRS | 0/6 | 基于底层事件网关现有的订阅派发流，业务已完全可以手工建立只读同步投影；系统层提供一个黑盒 CQRS 核心会极大捆绑使用方式，反而抹杀业务灵活运用库表的自由度。 |
