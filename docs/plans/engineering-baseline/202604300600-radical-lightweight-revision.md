# GoCell 激进减重修正决策（基于 3 摩擦 grep 实证 + 不向后兼容路径）

> 日期：2026-04-30
> 状态：**修正决策，与 `202604300500-engineering-priority-decision.md` 并存**（原 6 落点作为初版保留；本文不替换，补充实证修正）
> 触发：`202604300500` 决策中将 GoCell 误判为 "Spine 流派同阵营"；通过 3 个 grep 实证 agent（覆盖摩擦 1/2/3）证实 GoCell 实际是 **重框架（介于 go-zero 与 K8s operator SDK 之间）**，需修正
> 用户指令：(a) 不考虑向后兼容，彻底方案；(b) 拒绝 L7 渐进式演进路径（GoCell 必须保持强结构化）；(c) 找路径让重框架"重得有理由、感受度变轻"
> 关联：
> - `202604300430-engineering-research-cross-cut.md`（12 维度 × 3 SoT 数据底稿）
> - `202604300500-engineering-priority-decision.md`（原 6 落点决策，未删除）

---

## §1 三摩擦 grep 实证总结

> 实证来源：3 个并行 explorer agent，分别针对摩擦 1（框架 vs 库 API 形状）、摩擦 2（显式 vs IoC 装配）、摩擦 3（抽象过载 / 领域词汇）做 grep + Read 取证。每条结论嵌入了具体文件路径 + 行号 / 字段名作证据。

### 1.1 摩擦 1（框架 vs 库）实证

| 指标 | GoCell（最简 hello world） | net/http 等价 | 比值 |
|---|---|---|---|
| 文件数 | 7（cell.yaml + slice.yaml + contract.yaml + cell.go + handler.go + app.go + main.go）| 1 | **7×** |
| 行数 | ~220 | ~12 | **~18×** |
| 新概念 | 11+（Cell/Slice/Contract/Assembly/Bootstrap/ListenerRef/RouteGroup/auth.Mount/ContractSpec/L0-L4/yaml 字段约定）| 3（HandleFunc/ListenAndServe/ResponseWriter）| **~4×** |
| 上手时间 | 4-8 小时 | < 5 分钟 | — |
| 强制 yaml 字段 | 13（cell.yaml 7 + slice.yaml 6） | 0 | — |
| Framework-defined contributor interface | 5（RouteGroupContributor / EventRegistrar / HealthContributor / LifecycleContributor / ConfigReloader） | 0 | — |

**关键证据**：
- `bootstrap.go:L780-L878` 10-phase Run 流程
- `kernel/cell/interfaces.go:L84-L121` 12 方法 Cell interface
- `cells/accesscore/cell.go:L46-L51` 编译期断言 5 个 contributor interface 全部实现
- bootstrap phase5/6/3b 通过 `if rgc, ok := cell.(RouteGroupContributor); ok` **type assertion 自动发现**

**结论**：GoCell 实际是 **重 opinionated framework**，与 K8s operator SDK / Rails 同档（不是 Gin / chi）。

### 1.2 摩擦 2（显式 vs IoC）实证

| 维度 | 实证 |
|---|---|
| main.go 显式装配 | `examples/ssobff/app.go:L126-L213` 共 88 行手写显式装配（cell 构造 + adapter 注入 + JWT 密钥对 + assembly.Register）—— **70-75% 显式** |
| 反射使用 | **0 处**装配反射；仅 `kernel/cell/assembly.go:L257` 用 `reflect.ValueOf().IsNil()` 做 typed-nil 防御 |
| `init()` 注册 | **0 处** |
| `unsafe.` 使用 | **0 处** |
| `interface{} / any` 通用容器 | 仅 `Dependencies.Config` 和 `closers []any`，2 处核心使用 |
| 隐式自动装配点 | **4 处**（bootstrap phase3b/5/6 type assertion 自动发现 4 个 contributor + assembly 5 处可选 lifecycle hook 类型断言）—— **25-30% IoC** |

**关键证据**：
- `bootstrap.go:L848` `phase3bDiscoverLifecycleContributor`
- `bootstrap.go:L785` phase5 RouteGroupContributor 自动发现
- `bootstrap.go:L868` phase6 EventRegistrar 自动发现

**结论**：GoCell **70-75% 显式 + 25-30% IoC（type assertion 模式，不是反射）**。比 fx/dig 浅得多，但比 Spine "零隐式" 深。我之前说"不是 IoC 容器"是错的——type assertion 自动发现也是 IoC，只是更可读。

### 1.3 摩擦 3（抽象过载）实证

| 层 | 词数 | 内容 | 性质 |
|---|---|---|---|
| 一级（必须） | **6** | Cell / Slice / Contract / Journey / Actor / ConsistencyLevel(L0-L4) | **合理 domain language**（与 K8s Pod / DDD Bounded Context 同源） |
| 二级（L2+ 场景） | 6 | TxRunner / Emitter / Subscription / EntryHandler / **Disposition** / **HandleResult** | **混合**：Disposition + 4 词幂等模型透传 |
| 三级（framework 内部） | 6+ | **Claimer / Receipt / ClaimState** / SubscriptionMiddleware / DurabilityReporter / SubscriberIntakeStopper | 应该不暴露给业务开发者 |

**特定抽象过载点**（agent 3 实证）：
1. **5 词幂等 ACK 模型**：`Disposition + HandleResult + Claimer + Receipt + ClaimState` 同时出现才能描述一个 L2 consumer。Watermill 表达同等语义只用 `Ack/Nack` 2 个方法。这是分布式系统术语未经封装直接透传的认知税。
2. **TxRunner 三联命名**：`TxRunner / NoopTxRunner / RunnerOrNoop` 解决"测试 noop 事务"用 3 个名字。Go 标准库 `*sql.Tx` 本身就是 interface，多余。
3. **5 个 contributor interface**：framework-defined，每个 cell 强制实现。

**与 Clean Arch 在 Go 中水土不服的对比**：
- Clean Arch 的批评：UseCase/Repository/Entity 是 Java 层名移植，**空架子**
- GoCell 的问题：一级 6 词不是空架子（有真实业务对应物），二级 5 词幂等模型是**分布式术语未封装**——性质不同但摩擦同源

**结论**：一级词汇合理保留；二级 5 词幂等模型 + TxRunner 三联是真过载，应彻底简化。

---

## §2 GoCell 谱系定位（修正后）

```
轻库 ────────────────────────────────────────────────────── 重框架
  ▲ chi/Gin    ▲ Spine    ▲ Kratos   ▲ go-zero    ▲ GoCell    ▲ Rails / K8s operator SDK
                                                       ↑
                          5 contributor interface IoC（type assertion）
                          + 13 yaml 强制字段
                          + 70% 显式装配（main.go 可追踪）
                          + 5 词分布式术语透传
```

**核心论断**：GoCell 介于 go-zero 与 K8s operator 中间，**不是 Spine**。`docs/architecture/positioning.md` 必须明确这点（见 E5）。

---

## §3 减重数学

| 指标 | 当前 | E1+E2+E3 后 | + E4 marker codegen | + E8 bootstrap 显式化 | + E9 一键 scaffold | + E10 DTO 全自动 |
|---|---|---|---|---|---|---|
| hello world 手写文件数 | 7 | 4 | **3** | 3 | 3 | **2-3**（contract.yaml + service.go + 可选 cell.go） |
| 手写行数 | ~220 | ~120 | ~80 | ~95 | ~80 | **~50**（DTO/iface/errcode 都 codegen） |
| 新概念 | 11+ | 7 | 6-7 | 6-7 | 6-7 | **5-6** |
| 强制 import | 9 | 4 | 3-4 | 3-4 | 3-4 | 3 |
| 必填 yaml 字段总数 | 13 | 6 | 0 | 0 | 0 | 0 |
| 开发者写 yaml 文件类型 | 4 | 4 | **2** | 2 | 2 | 2 |
| 入场时间（5 步骤） | 4-8 小时 | 2-3 小时 | 1-2 小时 | 1-2 小时 | **5-10 分钟**（一键脚手架） | 5-10 分钟 |
| Framework-defined interface | 5 | 1 | 1 | 1 | 1 | 1 |
| IoC 装配比例 | 25-30% | 10-15% | 10-15% | **5-10%** | 5-10% | 5-10% |
| 比值 vs net/http | ~18× | ~8× | ~6× | ~6× | ~6× | **~5×** |

**关键减重节点**：
- **E2** Contributor 5→1：IoC 从 25-30% 砍到 10-15%（最大 IoC 杠杆）
- **E3+E4** marker codegen：hello world 从 7 文件砍到 3 文件（最大体感杠杆）
- **E8** bootstrap 显式化：IoC 残量从 10-15% 砍到 5-10%（最后清扫）
- **E9** 一键 scaffold：入场时间从 4-8 小时砍到 5-10 分钟（最大入场摩擦杠杆）
- **E10** DTO 全自动：service.go 行数减半，新概念 6-7 → 5-6（最大业务面摩擦杠杆）

**不能降到 net/http 水平**，因为 GoCell 是 framework with structure。但能让"重框架感受度"从 18× 减到 ~5×（**减重 72%**）。

---

## §4 10 个彻底落点（E1-E10）

### E1 — 二级词汇彻底收口（breaking）

**删除（从导出 API 移除，藏入 kernel 内部）**：
- `Disposition`（枚举类型，开发者不再见到）
- `Claimer / Receipt / ClaimState`（藏入 `kernel/idempotency` 内部 unexported）
- `SubscriberIntakeStopper`（合并入 `Subscriber`）
- `NoopTxRunner / RunnerOrNoop`（命名层移除）
- `WrapLegacyHandler`（迁移期间产物，不再需要）

**保留**：
- `HandleResult`：业务开发者唯一返回类型
- `TxRunner`：单一名字
- `Subscriber`：单一名字（含原 Stopper 能力）

**新 API（业务侧暴露）**：
```go
// 业务 service.go 唯一接触：
func (s *Service) HandleSessionCreated(ctx context.Context, e Event) cell.HandleResult {
    if err := s.process(ctx, e); err != nil {
        if errcode.IsPermanent(err) {
            return cell.Reject(err)   // 等价旧 DispositionReject + PermanentError
        }
        return cell.Requeue(err)      // 等价旧 DispositionRequeue
    }
    return cell.Ack()                  // 等价旧 DispositionAck
}
```

**影响**：所有 L2 consumer 一次性切签名；`kernel/outbox` 公开 API 简化 ~30%。

---

### E2 — Contributor interface 5→1，IoC 降级（breaking，**最大杠杆**）

**当前问题**：5 个 framework-defined contributor interface + bootstrap phase3b/5/6 type assertion 自动发现，导致开发者：
- 看 `app.go` 无法知道 auditcore 订阅了哪 13 个 topic（要去 cell.go 翻 RegisterSubscriptions）
- 不知道 `accesscore.WithInitialAdminBootstrap()` 何时触发（要懂 phase3b 发现机制）

**改为**：单一显式注册 API
```go
// kernel/cell/cell.go
type Cell interface {
    Init(ctx context.Context, reg Registry) error  // 唯一注册点
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
}

type Registry interface {
    Routes(specs ...RouteSpec)                                       // 替代 RouteGroupContributor
    Subscribe(topic string, h EntryHandler, opts ...SubOpt)          // 替代 EventRegistrar
    Health(name string, fn HealthCheckFn)                            // 替代 HealthContributor
    OnStart(fn func(context.Context) error)                          // 替代 LifecycleContributor.OnStart
    OnStop(fn func(context.Context) error)                           // 替代 LifecycleContributor.OnStop
    OnConfigReload(fn func(context.Context, Config) error)           // 替代 ConfigReloader
}
```

**Cell 内部实现**：
```go
func (c *AccessCore) Init(ctx context.Context, reg cell.Registry) error {
    // 一眼可见所有注册：
    reg.Routes(c.routeSpecs()...)
    reg.Subscribe("session.created.v1", c.handleSessionCreated)
    reg.Subscribe("user.deleted.v1", c.handleUserDeleted)
    reg.Health("accesscore_ready", c.checkReady)
    reg.OnStart(c.bootstrapInitialAdmin)
    return nil
}
```

**删除**：
- `RouteGroupContributor / EventRegistrar / HealthContributor / LifecycleContributor / ConfigReloader` 5 个 interface
- `bootstrap.go` 中 phase3b、phase5、phase6 的 type assertion 自动发现循环
- `kernel/cell/registrar.go` 整文件（500+ 行）

**新增**：
- `kernel/cell/registry.go`（Registry interface + 实现，估计 ~200 行）
- bootstrap phase3 调用 `cell.Init(ctx, reg)` 替代分散 type assertion

**效果**：
- IoC 比例 25-30% → **10-15%**（仅保留 phase 编排本身，cell 内部装配完全显式）
- `app.go` + `cell.go Init()` 可一眼追踪所有 routes/subscriptions/health/lifecycle
- 移除 5 contributor 后，编译期断言段从 5 行降到 1 行（`var _ cell.Cell = (*X)(nil)`）

**ROI**：**最高** —— 从重框架向轻库感受度移动的核心动作。

---

### E3 — Codegen 全栈消化样板（breaking）

**当前**：`gocell generate` 仅生成 assembly boundary + metrics-schema

**目标**：从单一真相源 `cell.yaml + slices/<id>/contract.yaml` 自动生成：
- `cell_gen.go`：包含 Cell struct skeleton + `Init(ctx, reg)` 方法 + 所有 `reg.X()` 调用 + 编译期 interface 断言 + `_gen.go` build tag
- `slice_gen.go`（每 slice 一个）：handler 函数桩 + service interface 骨架 + 路由声明
- `slice.yaml`（每 slice 一个）：从 `contract.yaml usage` + service 文件自动推导，**不再手写**

**开发者只写**：
- `cells/<id>/cell.yaml`：4 字段元数据（见 E4）
- `slices/<id>/contract.yaml`：API 契约（contract 是真相源）
- `slices/<id>/service.go`：业务逻辑

**配套 verify-gate**：
```bash
$ make verify-codegen
# 1. git worktree add 隔离沙箱（吸收 K8s 模式）
# 2. 沙箱内重跑 gocell generate
# 3. git status --porcelain 计数，非零即 fail
```

**类比**：go-zero `goctl` + kubebuilder `controller-gen`。

**ROI**：高 —— E1/E2 的简化必须配 codegen，否则开发者反而要在 Init() 里写更多注册代码。

---

### E4 — yaml 字段彻底瘦身 + marker codegen 真相源迁移（breaking）

**修订（2026-04-30）**：本落点从原"yaml 字段瘦身"升级为"yaml 真相源迁移到 Go 代码 marker"，实现 cell.yaml + slice.yaml **完全 codegen**。

#### 4.1 真相源策略选择

候选方案对比（前置分析见同目录决策点对话历史）：

| 方案 | 真相源 | 编译期检查 | 可生成范围 | 缺点 |
|---|---|---|---|---|
| A. Marker comment（kubebuilder 范式） | cell.go 顶部 `// +gocell:cell:...` 注释 | 否（lint 守护） | cell.yaml + slice.yaml 全部 | marker 拼写错误依赖 lint |
| B. Type embedding | cell struct embed `cell.CoreCell / cell.LocalTxLevel` | 是 | type / consistencyLevel 等枚举（2-3 字段） | 表达能力弱，需混合方案 |
| C. 单一 IDL 文件（proto 风格） | 项目级 `gocell.idl.yaml` | 否 | 全部 yaml + Go skeleton | 失去 cell 内聚性，与 cells/ 严格隔离哲学冲突 |

**采纳方案：A 主 + B 辅（混合）**

#### 4.2 cell.yaml 完全 codegen

**真相源**：cell.go 顶部 marker 注释 + struct embed

```go
// Package accesscore implements the access core cell.
//
// +gocell:cell:id=accesscore
// +gocell:cell:verify.smoke=cmd/gocell/check/accesscore_smoke.sh
package accesscore

type AccessCore struct {
    cell.CoreCell      // type=core（embed 表达）
    cell.LocalTxLevel  // consistencyLevel=L1（embed 表达）
    // ...
}
```

**生成规则**：
- `gocell generate cell` 用 `go/ast` 解析 marker 注释 + struct embedded type
- 反向生成 `cell.yaml`（4 字段全部从代码推导）
- CI verify-gate：`make verify-codegen` → `git worktree add` 隔离沙箱重跑 → `git status --porcelain` 计数 → 非零 fail

**结果**：
| cell.yaml 字段 | 真相源 | 类型 |
|---|---|---|
| `id` | marker `// +gocell:cell:id=` | string |
| `type` | struct embed `cell.CoreCell / EdgeCell / SupportCell` | enum（编译期 type-safe） |
| `consistencyLevel` | struct embed `cell.LocalOnlyLevel / LocalTxLevel / OutboxFactLevel / WorkflowEventualLevel / DeviceLatentLevel` | enum（编译期 type-safe） |
| `verify.smoke` | marker `// +gocell:cell:verify.smoke=` | path string |

**优点**：核心枚举（type/consistencyLevel）由 embed 编译期检查；非枚举字段（id/verify.smoke）由 marker 表达；开发者只看 Go 代码。

#### 4.3 slice.yaml 完全 codegen

**真相源**：`slices/<id>/service.go` marker + `contract.yaml usage` 反推

```go
// Package sessionlogin implements the session login slice.
//
// +gocell:slice:id=sessionlogin
package sessionlogin
```

**生成规则**：
- `gocell generate slice` 扫描 service.go marker → 推导 id
- 扫 `slices/<id>/_test.go` → 推导 verify.unit
- 扫 `slices/<id>/contract_test/` → 推导 verify.contract
- 反查所有 contract.yaml 中 `usage.cellId == <parent-cell-id> && sliceId == <id>` 的条目 → 推导 contractUsages

**结果**：slice.yaml **完全自动生成**，开发者不写。

#### 4.4 不参与 codegen 的 yaml 文件

| 文件 | 状态 | 理由 |
|---|---|---|
| `contract.yaml` | **保留为真相源** | API 契约本身就是 source of truth，无法从代码反推 |
| `assembly.yaml` | **保留为真相源** | 部署时配置（cells 列表 + entrypoint + binary + deployTemplate），不是代码而是部署声明 |
| `journey.yaml` | **保留为真相源** | E2E 验收规格，业务叙述无法反推 |
| `actors.yaml` | **保留为真相源** | 外部系统注册（与 cells 平级） |
| `boundary.yaml`（生成产物） | **codegen** | 已是 generated/ 产物 |
| `metrics-schema.yaml`（生成产物） | **codegen** | 已是 generated/ 产物 |

#### 4.5 总效果

| 指标 | E4 修订前 | E4 修订后 |
|---|---|---|
| 开发者写的 yaml 类型 | 4（cell + slice + contract + assembly） | **2**（contract + assembly） |
| cell 元数据真相源 | yaml | **Go 代码**（marker + struct embed） |
| 必填 yaml 字段总数 | 13 | **0**（cell.yaml + slice.yaml 完全 codegen；contract.yaml + assembly.yaml 必填字段不变） |
| Hello world 文件类型 | 4（cell.yaml + contract.yaml + service.go + cell.go） | **3**（contract.yaml + service.go + cell.go） |
| 比值 vs net/http | ~7× | **~6×** |

**ROI**：高 —— 配 E3 codegen 才能成立；与 E2 Registry API 协同（`cell_gen.go` 自动包含 `Init(reg)` 骨架，开发者只填业务逻辑）。

---

### E5 — Positioning 文档（零代码，立即做）

**新建**：`docs/architecture/positioning.md`（300-500 字）

内容大纲：
1. **GoCell 是什么**：重框架 + 强 codegen + 多形态部署，同档 go-zero / K8s operator SDK
2. **GoCell 不是什么**：不是 IoC 反射容器（如 fx/dig）；不是注解魔法（如 farseer-go）；不是轻库（如 Gin/chi/Spine）
3. **核心价值**：
   - **代码不变，部署形态由 assembly 决定**（单进程 / 微服务 / 设备端，吸收 Service Weaver 理念）
   - **强结构化**：cells/ 严格隔离，archtest LAYER 静态守卫
   - **codegen 消化样板**：业务开发者只写 service.go + cell.yaml + contract.yaml
4. **对标关系澄清**：CLAUDE.md「Cell 运行时 → fx」是吸收设计语义（Lifecycle 对称清理 / Module 隔离），**不是引入 fx 包**
5. **重框架的代价 + 回报**：承认入场成本（4-8 小时）；E3 codegen 把 hello world 从 7 文件降到 3 文件作为补偿

**README 顶部加 100 字 elevator pitch**：
> GoCell 是 Go 的 Cell-native 工程底座。代码以 Cell/Slice 组织，部署形态由 assembly 决定（单进程 / 微服务 / 设备端）。配套 codegen 消化样板，业务开发者只写 service.go + 3 段 yaml。重框架，强结构化，不引入 IoC 反射或注解魔法。

**ROI**：极高 —— 0 代码改动，立即缓解外部按"轻框架"刻板印象误读。

---

### E6 — `gocell visualize` 工具

**输入**：
- `assemblies/<name>/generated/boundary.yaml`
- `cells/<id>/cell_gen.go`（含完整注册信息，E3 副产物）

**输出**：DOT / SVG / mermaid 三种格式

**子命令**：
- `gocell visualize assembly <name>` → cell × contract 拓扑图
- `gocell visualize cell <id>` → 该 cell 的所有 routes/subscriptions/health/lifecycle 关系图
- `gocell visualize contracts` → 全仓 contract 依赖图

**实现**：stdlib `text/template`，不引第三方图库

**ROI**：中 —— 重框架靠工具消化"看不见"摩擦，让 IoC 自动发现的依赖一键可视化。

---

### E7 — 装备期（合并旧 L1/L2/L4/L5/L6）

按原 6 落点决策原样保留：
- **E7-1** 供应链安全武装（govulncheck + Semgrep + CodeQL + race 独立 job）—— 来自原 L1
- **E7-2** lint Tier 1+2 升级 —— 来自 ci-governance Batch 1+2
- **E7-3** 错误库 PII 增强（WithSafeDetails + AssertionFailedf）—— 来自原 L4
- **E7-4** API 治理升级（storageVersion + 弃用窗口）—— 来自原 L5
- **E7-5** 文档自动化（gen-crd-api-reference-docs + KEP frontmatter）—— 来自原 L6

注：原 L2（codegen 隔离沙箱）已并入 E3 配套 verify-gate，原 L3（Lifecycle 对称清理 + visualize）拆分到 E2（Lifecycle 部分自然消失因 contributor 收编）+ E6（visualize）。

---

### E8 — Bootstrap 显式化（延伸落点，breaking）

**当前**：`bootstrap.Run(ctx)` 黑盒跑 10 phase（phase0 validate → phase1 config → ... → phase10 LIFO teardown），开发者在 main.go 写：
```go
func main() {
    app := NewSSOBFFApp()
    ctx, cancel := shutdown.NotifyContext(...)
    defer cancel()
    app.Run(ctx)  // 一行调用，10 phase 黑盒
}
```

**改为**：phase 拆成可独立调用的函数，开发者可裁剪/重排
```go
func main() {
    ctx, cancel := shutdown.NotifyContext(...)
    defer cancel()

    // 一眼可见每个 phase：
    cfg := must(bootstrap.LoadConfig(ctx, "config.yaml"))
    asm := buildAssembly(cfg)               // 用户代码
    must(bootstrap.ValidateAssembly(ctx, asm))
    eb := bootstrap.NewEventBus(cfg.PubSub)
    must(bootstrap.StartCells(ctx, asm, bootstrap.WithEventBus(eb)))
    listeners := bootstrap.MountRoutes(asm, cfg.Listeners)
    workers := bootstrap.StartWorkers(ctx, asm)

    // 显式 wait + LIFO teardown：
    err := bootstrap.WaitShutdown(ctx, listeners, workers)
    bootstrap.TeardownLIFO(ctx, asm, listeners, workers)
    if err != nil { os.Exit(1) }
}
```

**变化**：
- **删**：`bootstrap.Run()` 单一入口
- **改**：`bootstrap` 包暴露 8 个独立函数（LoadConfig / ValidateAssembly / NewEventBus / StartCells / MountRoutes / StartWorkers / WaitShutdown / TeardownLIFO）
- **保留**：每个函数内部仍封装"phase 内部逻辑"（如 StartCells 仍按依赖顺序启动）
- **效果**：开发者可裁剪某个 phase（demo 不需要 EventBus 就跳过）；每个 phase 可独立测试；启动错误能精确定位到哪个 phase

**收益**：
- 把"10-phase 黑盒"变成"8 个显式调用"，开发者能直接读 main.go 理解启动流程
- 进一步从重框架向轻库感受度移动（从 IoC 25-30% → 10-15%（E2 已做）→ 5-10%）

**代价**：
- LIFO teardown 语义变弱（用户要自己保证调用顺序）—— 缓解：`bootstrap.NewLifecycle()` helper 仍提供自动 LIFO，是可选项不是强制
- main.go 行数从 ~3 行（`app.Run(ctx)`）增加到 ~15 行
- 启动失败时 rollback 责任转移到用户

**ROI**：中 —— 是 E2 完成后的延伸优化；不是必做但有助于消除剩余 IoC 痕迹。

**风险**：与 E5 positioning 文档的"重框架"定位有微妙张力——重框架通常包办 main.go，E8 把控制权还给用户。建议在 E5 文档明确：「GoCell 提供 `bootstrap.Run()` 一键启动，但所有 phase 函数也可独立调用」，给两种风格的开发者留口子。

---

### E9 — 一键 scaffold + 项目模板（高 ROI，对冲入场摩擦）

**当前**：`gocell scaffold cell <id>` 只生成 cell.yaml 骨架，开发者还要手写 cell.go / handler.go / service.go / 测试 / contract.yaml，新人 4-8 小时上手。

**改为**：
```bash
$ gocell new-cell accesscore --type=core --level=L2 --with-http --with-events
✓ cells/accesscore/cell.go (with marker comments)
✓ cells/accesscore/slices/example/service.go
✓ cells/accesscore/slices/example/service_test.go
✓ contracts/http/accesscore/example/v1/contract.yaml
✓ generated cell_gen.go + slice_gen.go (auto via E3)
✓ go test ./cells/accesscore/... PASS
```

一键产出**可编译可运行的最小 cell**，跑 `go test` 就过。新人 5 分钟出第一个能跑的 cell。

**子命令**：
- `gocell new-cell <id> --type=<core|edge|support> --level=<L0-L4> [--with-http] [--with-events]`
- `gocell new-slice <cell-id> <slice-id> --contract=<path>`
- `gocell new-contract <kind>/<domain>/<name> --version=v1 --schema=<openapi-path>`

**实现要点**：
- 复用 E3 codegen 引擎产出 cell_gen.go / slice_gen.go
- 业务部分用 stdlib `text/template` 模板（最小有效骨架，含 1 个 example slice + 1 个能过的测试）
- 类比：`rails generate scaffold` / `kubebuilder create api`

**ROI**：**高** —— 直接对冲 4-8 小时入场摩擦数据点（来自摩擦 1 实证）。新人体感：从「读 4 份文档 + 看 3 个示例 + 学 11 个概念」降到「跑一行命令出可编译骨架」。

---

### E10 — contract.yaml → DTO + service interface 全自动生成（高 ROI）

**当前**：contract.yaml 的 schema 字段只用于 `gocell validate` lint，开发者手写：
- `cells/<id>/slices/<slice>/dto.go`（DTO struct + JSON tag）
- `cells/<id>/slices/<slice>/service.go` 中的 service interface
- DTO 与 contract.yaml 之间无机器守护，漂移就 review 抓

**改为**：contract.yaml 加 OpenAPI-style 完整 schema：
```yaml
# contracts/http/accesscore/sessionlogin/v1/contract.yaml
kind: http
endpoints:
  POST /api/v1/sessions:
    request:
      schema:
        type: object
        required: [username, password]
        properties:
          username: { type: string, minLength: 1, maxLength: 64 }
          password: { type: string, minLength: 8 }
    response:
      200:
        schema:
          type: object
          properties:
            sessionId: { type: string, format: uuid }
            expiresAt: { type: string, format: date-time }
errors:
  - code: ERR_AUTH_INVALID_CREDENTIALS
  - code: ERR_AUTH_RATE_LIMITED
```

`gocell generate contracts` 自动产出：
- `generated/contracts/<id>/types.go`：DTO struct（含 JSON tag、validation tag）+ 编解码 helper
- `generated/contracts/<id>/iface.go`：service interface（`type SessionLoginService interface { Login(ctx, *LoginRequest) (*LoginResponse, error) }`）
- `generated/contracts/<id>/errors.go`：errcode 常量（从 errors 字段生成）

**开发者只填 service.go 业务逻辑**，DTO + interface + errcode 都不写：
```go
// cells/accesscore/slices/sessionlogin/service.go
type Service struct { /* deps */ }
var _ contracts.SessionLoginService = (*Service)(nil)  // 编译期断言

func (s *Service) Login(ctx context.Context, req *contracts.LoginRequest) (*contracts.LoginResponse, error) {
    // 只写业务逻辑
}
```

**类比**：buf generate / OpenAPI-Codegen / oapi-codegen

**配套**：
- E3 codegen verify-gate 同样守护 contracts/ generated 产物（git worktree 沙箱）
- contract.yaml 字段变化 → 自动重新生成 → CI diff fail → 显式更新

**ROI**：**高** —— service.go 行数从 ~50 砍到 ~25；DTO 漂移问题彻底消失；contract 真正成为 API 契约真相源。**这是从重框架向轻库感受度移动的另一杠杆点**：开发者面对的不再是「framework 强加的 11 个概念」，而是「写一个 service.go 实现 generated interface」——这是任何 Go 程序员都熟悉的模式。

---

## §5 路线图

```
Batch 1（核心简化，3-4 周）          Batch 2（codegen 消化，5-6 周）          Batch 3（装备期，4-6 周）
──────────────────────────         ──────────────────────────────         ──────────────────────────
PR 1: E5 positioning + README        PR 4: E3 codegen cell_gen.go            PR 10: E7-1 供应链安全
PR 2: E2 Contributor 5→1                  + slice_gen.go                     PR 11: E7-2 lint Tier 1+2
PR 3: E1 二级词汇收口                  PR 5: E4 marker codegen               PR 12: E7-3 错误库 PII
                                          （cell.yaml + slice.yaml          PR 13: E7-4 API 治理
                                           真相源迁移到 Go 代码）            PR 14: E7-5 文档自动化
                                     PR 6: E10 contract → DTO/iface
                                          全自动 codegen
                                     PR 7: E9 一键 scaffold + 模板
                                     PR 8: E6 gocell visualize
                                     PR 9: E8 bootstrap 显式化（延伸）
```

**关键依赖**：
- E5 零代码立即做
- E2 + E1 是 Batch 1 主菜，必须先于 E3（codegen 要基于新 Registry API 生成代码）
- E3 + E4（marker codegen）必须协同切（cell.yaml/slice.yaml 删除依赖 codegen + AST 解析能力）
- E10 在 E3 之后（DTO codegen 复用 E3 引擎）
- E9 在 E10 之后（一键 scaffold 调 E3 + E10 codegen 引擎产出可编译骨架）
- E6 在 E3 + E10 之后（依赖完整 generated 信息）
- E8 在 E2 之后（contributor 收编完成后才能拆 phase）
- Batch 3 装备期与前两期解耦，可并行

**总周期**：12-16 周完成 14 个核心 PR。

---

## §6 与原 6 落点的对应关系

| 原落点（202604300500） | 新落点（本文） | 变化 |
|---|---|---|
| L1 供应链安全 | E7-1 | 不变，保留 |
| L2 codegen 隔离沙箱 | 并入 E3 配套 verify-gate | 不再独立 |
| L3 Lifecycle 对称清理 + visualize | E2（Lifecycle 自然解决）+ E6（visualize） | 拆分 |
| L4 错误库增强 | E1（HandleResult 部分）+ E7-3（PII 部分） | 拆分 |
| L5 API 治理 | E7-4 | 不变 |
| L6 文档自动化 | E5（positioning）+ E7-5（自动化） | 拆分 |
| — | E1 二级词汇收口（新） | 新增（来自摩擦 3 实证） |
| — | E2 Contributor 5→1（新） | 新增（来自摩擦 1+2 实证，最大 IoC 杠杆） |
| — | E3 Codegen 全栈（新） | 新增（来自摩擦 1 实证，go-zero 范本） |
| — | E4 marker codegen + yaml 真相源迁移（新） | 新增（修订自原 yaml 瘦身；cell.yaml + slice.yaml 完全 codegen） |
| — | E8 Bootstrap 显式化（新，延伸落点） | 新增（IoC 残量从 10-15% 降到 5-10%，配 E2 后做） |
| — | E9 一键 scaffold + 项目模板（新） | 新增（最大入场摩擦杠杆：4-8 小时 → 5-10 分钟） |
| — | E10 contract → DTO/iface 全自动 codegen（新） | 新增（最大业务面摩擦杠杆：service.go 行数减半 + 概念 6-7 → 5-6） |

**原 L7（轻起步路径）**：用户拒绝。GoCell 必须保持强结构化，不允许 cells/<x>/internal/ 普通包先存在的过渡形态。

**E11-E14 评估候选**：另有 4 个低-中 ROI 延伸落点（errcode 收敛 / Actor 合并 / assembly.yaml 极简 / pkg 库化承诺）已单独存到 `202604300700-extension-leverages.md`，作为 backlog 评估项不进核心 10 落点。

---

## §7 决策点（待确认）

- [ ] **本文是否作为新 baseline 替代 202604300500 决策？**（用户已说"不替换"，本文与原 6 落点决策并存；下一步骤待用户指令是直接采纳 E1-E10 还是合并到原决策）
- [ ] **E5 positioning 文档是否立即做？**（零成本，强烈建议）
- [ ] **E2 Registry API 形状是否同意？**（草案见 §4 E2，可能需要再迭代具体方法签名）
- [ ] **E3 + E4 + E10 是否一次性切（一个 release）还是按 Batch 1 + Batch 2 分两次切？**
- [ ] **E11-E14 延伸候选**（见 `202604300700-extension-leverages.md`）是否进入排期或留作 backlog？

---

## §8 数据来源

- 摩擦 1（框架 vs 库）实证：agent `a917888423ecc6710`，74 次 tool_use，覆盖 cell.yaml/slice.yaml/cell.go 字段统计 + bootstrap 10-phase 拆解 + hello world 文件数对比
- 摩擦 2（显式 vs IoC）实证：agent `af8ebe8f929a7e4f1`，61 次 tool_use，覆盖 main.go 装配链 + 反射使用扫描 + 隐式发现点定位
- 摩擦 3（抽象过载）实证：agent `adc5ed5850f25d22e`，56 次 tool_use，覆盖词汇全清单 + 学习曲线分层 + 与 Clean Arch 对比

每个 agent 结论嵌入了具体文件路径 + 行号作证据，详见各 transcript 文件路径（见 ../engineering-baseline/202604300430-engineering-research-cross-cut.md §6）。
