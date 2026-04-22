# gocell 全量能力六角色归属裁决报告

> 日期: 2026-04-11
> 范围: `#1-#7、#9-#34`，`#8` 在源文档中无独立条目，故不单列
> 分析基准:
> - `/Users/shengming/Documents/winmdm/winmdm/docs/reviews/20260410-gocell-migration-analysis/10-gocell-enhancement-backlog.md`
> - `/Users/shengming/Documents/winmdm/winmdm/docs/reviews/20260410-gocell-migration-analysis/11-opensource-benchmark.md`
> - 当前仓库现状，重点参考 [errcode.go](/Users/shengming/Documents/code/gocell/pkg/errcode/errcode.go:1)、[builder.go](/Users/shengming/Documents/code/gocell/pkg/query/builder.go:1)、[outbox.go](/Users/shengming/Documents/code/gocell/kernel/outbox/outbox.go:1)、[router.go](/Users/shengming/Documents/code/gocell/runtime/http/router/router.go:1)、[body_limit.go](/Users/shengming/Documents/code/gocell/runtime/http/middleware/body_limit.go:1)、[watcher.go](/Users/shengming/Documents/code/gocell/runtime/config/watcher.go:1)、[keys.go](/Users/shengming/Documents/code/gocell/runtime/auth/keys.go:1)、[consumer_base.go](/Users/shengming/Documents/code/gocell/adapters/rabbitmq/consumer_base.go:1)
> 本次更新: 重新启动 6 个角色 agent，逐项补上“`gocell 应做 / MDM 应做 / 拆分做 / 不应框架做`”的明确归属与原因

## 1. Executive Summary

上一版报告的问题，不在“有没有结论”，而在“边界还不够一眼看懂”。这次修订后的核心变化是：

1. 每个能力都被明确判成 `gocell / mdm / split / none` 四类之一。
2. 每个能力都写清楚了：
   - 当前状态
   - 六角色逐项判断摘要
   - `gocell` 负责什么
   - `MDM` 负责什么
   - 为什么这么归属
3. 对已部分存在的能力，按“补齐/硬化”裁决，不再误写成全新能力，重点是 `#13`、`#19`、`#33`、`#34`。

### 1.1 归属判定标准

- `gocell`
  含义: 这是通用框架能力，应该由框架提供统一实现或统一契约。
- `mdm`
  含义: 这是明显的设备管理/证书/注册域能力，应该只由产品 Cell 实现。
- `split`
  含义: 框架只做扩展点、调度骨架、公共协议层；业务语义、策略和状态定义留给 MDM。
- `none`
  含义: 当前不应把它立成框架能力。要么交给基础设施原生能力，要么暂不做。

### 1.2 六角色缩写

- `P` = Product
- `A` = Architect
- `K` = Kernel Guardian
- `S` = Security
- `Q` = Correctness/QA
- `D` = DevOps

### 1.3 所有权统计

| 所有权 | 数量 | 条目 |
|---|---:|---|
| `gocell` | 18 | `#1 #5 #6 #7 #9 #10 #11 #12 #13 #17 #18 #19 #20 #30 #31 #32 #33 #34` |
| `mdm` | 3 | `#3 #24 #25` |
| `split` | 7 | `#2 #4 #14 #15 #16 #21 #22` |
| `none` | 5 | `#23 #26 #27 #28 #29` |

### 1.4 项目现在该做什么

**gocell 现在该做**

- `#1 CSRF`
- `#9/#10 errcode 内外分离 + trace_id`
- `#12 archtest`
- `#13 per-route Body size`：按“硬化已有能力”做
- `#19 Handler Middleware chain`：按“硬化已有能力”做
- `#20 TestPubSub`
- `#32 mTLS`
- `#34 OnConfigChange`

**gocell + MDM 拆分现在该做**

- `#2 密钥轮换调度器`
- `#14 Codec 注册表`
- `#22 Visibility Query API`

**MDM 现在该做**

- `#3 X.509 证书 adapter`
- `#24 Policy Engine`：至少做最小可用策略，不要求一开始就是“大而全引擎”

**现在不该作为框架能力立项**

- `#23 模块化单体 → 微服务`
- `#26 FanOut/FanIn`
- `#27 Hook 系统`
- `#28 服务发现 Registry`
- `#29 Saga 补偿`

## 2. 关键依赖

### 2.1 `mTLS / key rotation / cert policy`

- `#2` 是 `#3/#24` 的底层配套。
- `#24` 决定“哪些证书可以签”。
- `#3` 决定“如何签、如何吊销、如何发 CRL/SCEP”。
- `#32` 决定“签出来的证书如何在入口被验证并注入身份”。
- 推荐顺序:
  `#2 -> #3 + #24 -> #32`

### 2.2 `visibility / lifecycle / config change`

- `#22` 要想稳定，任务源注册和后台 worker 生命周期必须清楚。
- `#17` 若不做，`#22` 必须坚持最小面，只做“注册点 + 聚合查询”，不做复杂编排。
- `#34` 是 `#22` 任务源、连接池、轮换器热更新的运行时配套。

### 2.3 `pubsub testkit / handler middleware / delayed message`

- `#19` 是现有 consumer 行为的标准化入口。
- `#20` 是 `#19` 的质量门槛，必须锁定顺序、重试、DLQ、shutdown 语义。
- `#18` 只有在 `#19/#20` 已经稳定后才值得推进，否则会把消息系统复杂度再抬一层。

## 3. 全量能力归属矩阵

### 3.1 框架缺口 `#1-#7`

| ID | 能力 | 当前状态 | 六角色归属摘要 | 最终所有权 | gocell 负责 | MDM 负责 | 最终结论 | 原因 |
|---|---|---|---|---|---|---|---|---|
| `#1` | CSRF 中间件 | 未实现 | `P gocell-now / A gocell-now / K gocell-runtime / S gocell-conditional / Q gocell-now / D gocell-conditional` | `gocell` | `runtime/http/middleware` 提供统一中间件和 allowlist/config 接口 | `—` | `Now` | 它是纯 HTTP 运行时安全基建，不带领域语义；只有框架统一做，才能避免各 Cell 漏挂或分叉实现。 |
| `#2` | 密钥轮换调度器 | 未实现；[keys.go](/Users/shengming/Documents/code/gocell/runtime/auth/keys.go:1) 仅支持静态 env 读钥 | `P split-now / A split-now / K split-conditional / S split-now / Q split-now / D split-now` | `split` | `KeyManager`、调度器、共存窗口、事件/回调模型 | CA、DIGEST、LAPS、JWT 等具体轮换策略 | `Now` | 通用的是“轮换机制”，不是“轮换策略”；框架可以统一骨架，产品必须保留用途和策略控制权。 |
| `#3` | X.509 证书 adapter | 未实现 | `P mdm-now / A mdm-now / K mdm-now / S mdm-now / Q mdm-now / D mdm-now` | `mdm` | `—` | `cert-core` 的 CA、签发、CRL、SCEP | `Now` | 这是设备注册信任链本体，不是通用微服务底座。做进 gocell 会把框架直接拉成 MDM 半产品。 |
| `#4` | Webhook 出站 adapter | 未实现 | `P split-conditional / A split-later / K split-later / S split-conditional / Q split-later / D split-later` | `split` | 通用 HTTP 投递器、签名、重试、健康检查 | 事件目录、租户隔离、配置 CRUD、订阅策略 | `Conditional` | 传输机制可复用，但订阅模型和事件暴露明显是产品语义；当前没有必要把整套能力提前平台化。 |
| `#5` | 查询过滤语言 | 未实现；[builder.go](/Users/shengming/Documents/code/gocell/pkg/query/builder.go:1) 只有 SQL Builder | `P gocell-later / A gocell-conditional / K gocell-now / S gocell-later / Q gocell-now / D gocell-later` | `gocell` | `pkg/query` 的 DSL、白名单校验、SQL 转换 | 各 Cell 的可过滤字段声明 | `Later` | 它是通用查询能力，但不阻塞当前迁移主线；只有在多个列表 API 真正共享一套过滤语义时再收敛更合适。 |
| `#6` | 游标分页 | 未实现 | `P gocell-later / A gocell-conditional / K gocell-now / S gocell-later / Q gocell-now / D gocell-later` | `gocell` | `pkg/query` 的 cursor 编解码和 keyset 规则 | 各 Cell 的排序键选择 | `Later` | 这是典型通用查询基础设施，不是 MDM 专属；但当前收益仍低于协议、安全和消息主线。 |
| `#7` | 批量操作 helper | 未实现 | `P none-not-doing / A gocell-later / K gocell-conditional / S gocell-later / Q gocell-later / D gocell-later` | `gocell` | 若未来出现稳定模式，再提供统一结果模型和错误聚合 helper | 各具体批量业务语义 | `Later` | 如果以后做，它应该属于框架公共 helper；但当前尚无足够多稳定批量场景，不能先把策略和事务语义抽死。 |

### 3.2 能力增强 `#9-#16`

| ID | 能力 | 当前状态 | 六角色归属摘要 | 最终所有权 | gocell 负责 | MDM 负责 | 最终结论 | 原因 |
|---|---|---|---|---|---|---|---|---|
| `#9` | errcode 内外错误分离 | 部分实现；[errcode.go](/Users/shengming/Documents/code/gocell/pkg/errcode/errcode.go:1) 只有 `Code/Message/Details/Cause` | `P gocell-now / A gocell-now / K gocell-now / S gocell-now / Q gocell-now / D gocell-now` | `gocell` | `pkg/errcode` 统一提供 `Message/InternalMessage` 语义 | `—` | `Now` | 这是一等横切能力。当前错误模型混用对外/对内语义，已经构成信息泄露和排障噪音。 |
| `#10` | errcode 错误追踪 UUID | 未实现 | `P gocell-now / A gocell-now / K gocell-now / S gocell-now / Q gocell-now / D gocell-now` | `gocell` | 在错误响应和日志间统一 trace_id 约定 | `—` | `Now` | 这是 #9 的配套能力，属于框架层统一约定，不应该由每个 Cell 各自决定错误关联方式。 |
| `#11` | errcode 4xx/5xx OTel 分类 | 未实现；[tracing.go](/Users/shengming/Documents/code/gocell/runtime/http/middleware/tracing.go:1) 只记录 `http.status_code` | `P gocell-later / A gocell-now / K gocell-now / S gocell-later / Q gocell-now / D gocell-now` | `gocell` | tracing/metrics 中间件的统一错误分类语义 | `—` | `Later` | 它显然归框架，不归业务；但优先级仍晚于错误模型本身，因为它改善的是观测口径，不是基础行为。 |
| `#12` | archtest 代码级边界守护 | 未实现 | `P gocell-now / A gocell-now / K gocell-now / S gocell-later / Q gocell-now / D gocell-conditional` | `gocell` | `pkg/archtest` 与 CI 规则集 | `—` | `Now` | 这是 Cell 架构治理应有的代码级门禁，属于 gocell 自身质量体系，而不是 MDM 域逻辑。 |
| `#13` | per-route Body size | 已部分可做；[router.go](/Users/shengming/Documents/code/gocell/runtime/http/router/router.go:1) 可挂 [body_limit.go](/Users/shengming/Documents/code/gocell/runtime/http/middleware/body_limit.go:1) | `P gocell-conditional / A gocell-now / K gocell-now / S gocell-not-doing-as-new / Q gocell-not-doing-as-new / D gocell-now` | `gocell` | 把已有路由级能力产品化、补文档和回归测试 | `—` | `Now` | 这不是新能力归属争议，而是“已有能力要不要正式承诺”。答案是要，而且归 gocell，不归 MDM。 |
| `#14` | Codec 注册表 + 自定义序列化 | 未实现 | `P split-now / A split-now / K split-now / S split-conditional / Q split-now / D split-conditional` | `split` | `pkg/codec`、registry、JSON/XML、内容协商 | SOAP/SyncML codec 的具体实现与安全加固 | `Now` | 注册表是框架扩展点，协议 codec 是业务域实现；这项必须拆开，否则会把协议语义固化进框架。 |
| `#15` | 队列状态机标准化 | 未实现 | `P split-conditional / A split-conditional / K split-conditional / S split-later / Q split-conditional / D split-now` | `split` | 最小状态机骨架、超时/重试预算工具 | 命令状态枚举、迟到 ACK、补偿规则 | `Conditional` | 如果以后做，框架只应做“状态机工具”，不能做“命令域状态图”。当前应等至少一个生产级队列跑通后再抽象。 |
| `#16` | 投影按需重算模式 | 未实现 | `P split-later / A split-later / K split-later / S split-later / Q split-later / D split-later` | `split` | 触发器、去抖、checkpoint 骨架 | 智能组/投影重算规则与领域算法 | `Later` | 框架最多提供调度模式，真正的重算语义仍然是产品域逻辑，当前不该提到前排。 |

### 3.3 开源补入 `#17-#31`

| ID | 能力 | 当前状态 | 六角色归属摘要 | 最终所有权 | gocell 负责 | MDM 负责 | 最终结论 | 原因 |
|---|---|---|---|---|---|---|---|---|
| `#17` | 生命周期钩子细化 | 未实现；[interfaces.go](/Users/shengming/Documents/code/gocell/kernel/cell/interfaces.go:1) 只有 `Init/Start/Stop` | `P gocell-now / A gocell-now / K gocell-conditional / S gocell-later / Q gocell-conditional / D gocell-now` | `gocell` | 可选生命周期扩展接口，不能硬破坏所有 Cell | `—` | `Conditional` | 这类能力只能是框架做；但它会扩大 Cell 接口面，因此只有在真实启动/关闭痛点明确后才值得定型。 |
| `#18` | 延迟消息原语 | 未实现；[outbox.go](/Users/shengming/Documents/code/gocell/kernel/outbox/outbox.go:1) 无 `Delay/NotBefore` 语义 | `P gocell-later / A gocell-conditional / K gocell-conditional / S gocell-later / Q gocell-conditional / D gocell-later` | `gocell` | 若未来做，只提供最小消息语义与存储扩展点 | `—` | `Conditional` | 如果未来出现第二类协议或统一消息语义，这仍然是 gocell 的 transport/runtime 能力；但当前没有足够业务故事，不该先做。 |
| `#19` | Handler Middleware chain | 已部分实现；[outbox.go](/Users/shengming/Documents/code/gocell/kernel/outbox/outbox.go:1) 有 `TopicHandlerMiddleware`，[consumer_base.go](/Users/shengming/Documents/code/gocell/adapters/rabbitmq/consumer_base.go:1) 有 `AsMiddleware()` | `P gocell-now / A gocell-now / K gocell-now / S gocell-not-doing-as-new / Q gocell-now / D gocell-now` | `gocell` | 把现有 middleware 形态收敛成标准 adapter 契约 | `—` | `Now` | 边界很清楚，它属于 adapter 横切能力；当前真正要做的是“把已有雏形产品化”，而不是再造新概念。 |
| `#20` | TestPubSub 认证测试套件 | 未实现 | `P gocell-now / A gocell-now / K gocell-now / S gocell-later / Q gocell-now / D gocell-now` | `gocell` | adapter conformance testkit | `—` | `Now` | 这是框架质量基建，不是 MDM 功能。只要 gocell 对外提供 Pub/Sub adapter，这项就应是平台责任。 |
| `#21` | Mixin 跨 Cell 共享逻辑 | 未实现 | `P split-later / A split-later / K split-later / S split-later / Q split-later / D split-later` | `split` | 如果未来真要做，只提供组合机制 | 具体 mixin 内容与生命周期 | `Later` | 它不是纯框架也不是纯 MDM；但现在还没稳定复用模式，先让产品层沉淀真实重复再抽象。 |
| `#22` | Visibility Query API | 未实现；当前只有 `/healthz`、`/readyz`、`/metrics` | `P split-now / A split-now / K split-conditional / S split-conditional / Q split-now / D split-now` | `split` | 统一任务源注册点和聚合查询路由 | 各 Cell 的任务状态、进度、失败语义 | `Now` | 统一入口是框架责任，任务语义是产品责任。这项不拆就会出现“框架懂太多业务”或“各 Cell 各查各的”两种坏结果。 |
| `#23` | 模块化单体 → 微服务 | 部分实现；Assembly 已支持多 Cell 单进程组合 | `P gocell-conditional / A none-not-doing / K gocell-conditional / S gocell-later / Q gocell-not-doing / D gocell-conditional` | `none` | `—` | `—` | `Not Doing` | 它更像架构属性或验证清单，不是一个应单独开发的框架能力。当前应保留为部署等价性验证，而不是 backlog feature。 |
| `#24` | Policy Engine（证书签发策略） | 未实现 | `P mdm-conditional / A mdm-conditional / K mdm-now / S mdm-now / Q mdm-now / D mdm-conditional` | `mdm` | `—` | `cert-core` 的 allow/deny 规则、最小默认拒绝策略 | `Now` | 安全上它必须存在，但不需要一开始就是复杂“引擎”。最小可用规则系统现在就该由 MDM 做。 |
| `#25` | 短期证书 + 被动撤销 | 未实现 | `P mdm-later / A mdm-later / K mdm-later / S mdm-conditional / Q mdm-later / D mdm-later` | `mdm` | `—` | 短证书、续期、吊销替代策略 | `Later` | 它是 cert-core 的运营策略优化，不是框架能力；必须排在基础证书链和策略控制之后。 |
| `#26` | FanOut/FanIn 拓扑 | 无需实现；broker 原生已支持 | `P none-not-doing / A none-not-doing / K none-not-doing / S none-not-doing / Q none-not-doing / D none-not-doing` | `none` | `—` | `—` | `Not Doing` | 底层 broker 已有原生能力，框架再抽象只会复制消息中间件语义。 |
| `#27` | Hook 系统（mutation/query） | 未实现；且当前无统一 ORM/数据访问层 | `P none-not-doing / A none-not-doing / K none-not-doing / S none-not-doing / Q none-not-doing / D none-not-doing` | `none` | `—` | `—` | `Not Doing` | 这是典型 ORM 或数据访问层概念，不属于当前 gocell 的分层边界。 |
| `#28` | 服务发现 Registry | 未实现；当前以单 Assembly + K8s 为前提 | `P gocell-conditional / A none-conditional / K none-later / S none-not-doing / Q none-not-doing / D none-not-doing` | `none` | `—` | `—` | `Not Doing` | 当前应优先依赖 Kubernetes/平台层服务发现，而不是在框架里重复造一个 Registry。 |
| `#29` | Saga 补偿模式 | 未实现；当前主要靠 outbox/eventual consistency | `P none-not-doing / A split-later / K none-later / S none-not-doing / Q none-not-doing / D none-not-doing` | `none` | `—` | `—` | `Not Doing` | 如果未来真出现复杂补偿，才可能考虑单独模式；当前它不是应该承诺的框架能力。 |
| `#30` | 编译期 Contract 验证 | 未实现 | `P none-not-doing / A gocell-later / K gocell-later / S gocell-later / Q none-not-doing / D gocell-later` | `gocell` | 若未来需要，应归入治理/代码生成工具链 | `—` | `Not Doing` | 归属上它属于框架工具链，不属于 MDM；但现阶段 metadata/YAML 校验已够，当前不做。 |
| `#31` | 跨协议元数据同步 | 未实现；当前仍是 HTTP 主语境 | `P none-not-doing / A gocell-conditional / K gocell-later / S none-not-doing / Q none-not-doing / D gocell-conditional` | `gocell` | 若未来多协议落地，应由 runtime/transport 统一处理 | `—` | `Not Doing` | 如果以后需要，它也应该归 gocell，而不是 MDM；但当前纯 HTTP 阶段不值得提前设计。 |

### 3.4 审查补入 `#32-#34`

| ID | 能力 | 当前状态 | 六角色归属摘要 | 最终所有权 | gocell 负责 | MDM 负责 | 最终结论 | 原因 |
|---|---|---|---|---|---|---|---|---|
| `#32` | mTLS 中间件 | 未实现；`runtime/tls` 仍停留在 backlog | `P gocell-now / A gocell-now / K gocell-now / S gocell-now / Q gocell-now / D gocell-now` | `gocell` | `runtime/http/middleware` 的证书链校验、身份注入、header trust 开关 | `—` | `Now` | 这就是典型通用接入层能力。无论是 MDM、IoT 还是零信任服务，它都应由框架统一提供。 |
| `#33` | 限流/熔断可选中间件 | 部分实现；[rate_limit.go](/Users/shengming/Documents/code/gocell/runtime/http/middleware/rate_limit.go:1) 已有限流 | `P gocell-conditional / A gocell-conditional / K gocell-conditional / S gocell-not-doing-as-security / Q gocell-later / D gocell-conditional` | `gocell` | 保持为可选 middleware/adapter；当前先以现有限流为主 | `—` | `Conditional` | 它的归属没有争议，属于 gocell；争议只在时机。当前不该把“已有 rate limit + 尚缺 breaker”误写成全新必做能力。 |
| `#34` | OnConfigChange | 部分实现；[watcher.go](/Users/shengming/Documents/code/gocell/runtime/config/watcher.go:1) 只有文件级 callback | `P gocell-now / A gocell-now / K split-conditional / S gocell-conditional / Q gocell-now / D gocell-now` | `gocell` | `runtime/config` 提供变更分发与失败隔离契约 | 各 Cell 自己定义如何重载和重绑 | `Now` | 分发契约是框架职责，具体重载动作是 Cell 职责；但整体所有权仍归 gocell，因为没有这个 runtime 契约，热更新链路就无法闭环。 |

## 4. 归属视图

### 4.1 `gocell` 应做的能力

- `Now`
  `#1 #9 #10 #12 #13 #19 #20 #32 #34`
- `Conditional`
  `#17 #18 #33`
- `Later`
  `#5 #6 #7 #11`
- `Not Doing Now`
  `#30 #31`

### 4.2 `MDM` 应做的能力

- `Now`
  `#3 #24`
- `Later`
  `#25`

### 4.3 `split` 的能力

- `Now`
  `#2 #14 #22`
- `Conditional`
  `#4 #15`
- `Later`
  `#16 #21`

### 4.4 `none` 的能力

- `#23`
  不是框架能力，是架构验证项。
- `#26`
  交给 broker 原生能力。
- `#27`
  边界不匹配。
- `#28`
  交给 Kubernetes/平台层。
- `#29`
  当前不成立。

## 5. 路线图

### 5.1 Now

- `gocell`
  `#1 #9 #10 #12 #13 #19 #20 #32 #34`
- `split`
  `#2 #14 #22`
- `mdm`
  `#3 #24`

### 5.2 Conditional

- `gocell`
  `#17 #18 #33`
- `split`
  `#4 #15`

### 5.3 Later

- `gocell`
  `#5 #6 #7 #11`
- `split`
  `#16 #21`
- `mdm`
  `#25`

### 5.4 Not Doing

- `none`
  `#23 #26 #27 #28 #29`
- `gocell, 但当前不做`
  `#30 #31`

## 6. 结论

这次 6 个角色逐项复核后，边界已经可以明确写成一句话：

- `gocell` 现在真正应该承诺的是：HTTP 安全底座、错误模型、代码级治理、消息链路统一行为、适配器质量门槛、异步可见性、配置热更新和 mTLS。
- `MDM` 现在真正应该承诺的是：证书链路本身和证书签发策略，而不是把这些语义反推给框架。
- `split` 项要坚持“框架做骨架，MDM 做语义”，尤其是 `#2 #14 #15 #22`。
- 有些能力不是“没人想做”，而是“现在不该做成框架能力”，典型就是 `#23 #26 #27 #28 #29`。
