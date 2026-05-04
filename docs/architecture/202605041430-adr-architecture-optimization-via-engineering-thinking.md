# GoCell 架构设计优化（思想推导 + 形态约束 + K8s 校准）

## 0. 方法论结构

```
        【目标层】Bridle 三跃迁
   状态可见 / 意图可表达 / 系统自收敛
              ▲
              │
       【思想层】（推导工具）
   系统工程 + 软件工程 + 第一性原理
              │
              ▼
       【形态层】GoCell 是编程框架（关键约束）
              │     时间维度：编译期/CI/启动期/运行时
              │     权限范围：仓库与元数据，不含宿主运行时
              ▼
       【应用层】GoCell 架构设计原则
              │
              ▼
       【校准层】K8s（同范式参照，集群运行时形态）
```

**核心认知**：Bridle 是**目标**（要达到什么），三种工程思想是**推导工具**，**形态层**是不可绕过的约束（GoCell 是编程框架，不是集群运行时），K8s 是同范式但不同形态的参照。

---

## 1. 目标层：Bridle 三跃迁陈述

| 跃迁 | 目标状态 |
|---|---|
| ① 状态可见 | 代码不再是被生产的工件，而是被观测的对象 |
| ② 意图可表达 | 命令式驱动转为声明式契约，每个意图绑定验证器 |
| ③ 系统自收敛 | 单次推理转为持续自治，desired vs observed 自动消减 |

仅此三句作为目标。后续推导用三种思想完成，不再引用 Bridle 话术。

---

## 2. 思想层：三种工程思想

### 2.1 系统工程视角
边界 / 状态 / 反馈回路 / 故障模式 / 可观测性 / 配置管理 / 需求追溯 / 熵增。

### 2.2 软件工程视角
抽象 / 内聚 / 耦合 / DIP / DRY / SRP / 类型 / 契约 / 错误处理 / ISP。

### 2.3 第一性原理视角
寻找最小不可约事实集 / 砍冗余与补丁 / 规则可推导 / 公理违例必修。

---

## 3. 形态层：GoCell 部署形态约束（关键）

### 3.1 GoCell 是编程框架，不是集群运行时

| 维度 | 集群运行时（K8s） | 编程框架（GoCell） |
|---|---|---|
| **形态** | 长期运行的服务（apiserver+etcd+controller）| 被嵌入到客户应用的库 + 编译期工具（codegen）|
| **持久层** | 拥有 etcd | **不拥有任何持久层** |
| **修改权限** | 运行时改集群（kubectl/patch）| 编译期改仓库；运行时只能注入接口给宿主 |
| **部署多样性** | N=1 | N=每个客户不同（k8s pod / lambda / cf worker / 裸金属）|
| **时间维度** | 持续（运行时为主）| 多重（编译期 / CI 期 / 启动期 / 运行时）|
| **错误传播** | 整个集群 | 单个宿主应用 |

### 3.2 时间维度模型

GoCell 的所有事件落在四个时间维度之一：

| 维度 | 触发 | GoCell 权限 | 典型工作 |
|---|---|---|---|
| **编译期** | `gocell generate` / `go build` | **完全控制** | YAML→Go codegen / archtest |
| **CI 期** | git push / PR | **完全控制** | validate-meta / verify-slice / harvest 巡检 |
| **启动期** | corebundle 二进制启动 | **fail-fast 权限** | bootstrap phase0-7 / 配置校验 |
| **运行时** | 处理请求 / 后台 worker | **只读 + 经接口注入** | 业务逻辑 / 接口暴露状态供宿主消费 |

### 3.3 权限模型（决定自收敛在哪闭环）

第一性原理推导：

- 收敛需要"对系统的修改权限"
- K8s 运行时**有权限**改集群 → reconcile 落运行时
- GoCell 运行时**没有权限**改宿主应用（它是被嵌入的库）
- GoCell 编译期/CI 期**有权限**改自己仓库（PR / commit / 元数据）
- 所以 **GoCell 自收敛必须落在它有权限的时间维度** = 编译期/CI 期

这不是设计偏好，是**权限模型的强约束**。Bridle Leap 3 在 GoCell 落点是 **harvest 反哺循环**（CI bot + AI Agent 操作仓库），不是运行时 controller manager。

### 3.4 双层 harvest 域

GoCell 的 harvest 反哺有两层独立域：

| 域 | 范围 | 修复对象 | 触发者 |
|---|---|---|---|
| **框架自演进** | `kernel/` `runtime/` `adapters/` `cells/` | 框架自身 | GoCell 维护团队 + Claude Code Agent |
| **客户应用演进** | 客户使用 GoCell 写的 cells/contracts/journeys | 客户代码 | 客户工程师 + 其 AI Agent |

两层机制同构（同一套 archtest 引擎、harvest 队列、决策梯度），但**范围与所有权分开**。GoCell 必须显式区分这两层，避免框架自演进规则误用到客户域。

### 3.5 推论：编译期前移优先

由 3.2-3.4 推出工程总则：

> **任何能在编译期完成的事，不要推到运行时。**

理由：
- 编译期错误 = 客户拿不到坏二进制（最早失败）
- CI 期错误 = 客户拿不到坏 PR
- 启动期错误 = 宿主 fail-fast
- 运行时错误 = 已经在客户生产环境

时间维度越早，blast radius 越小，反馈越快。这是 GoCell 形态独有的优势，K8s 因为是运行时服务无法享受。

---

## 4. 推导：每个目标在三思想 × 形态约束下的要求

### 4.1 目标①「状态可见」

**三思想要求**：
- 系统工程：状态可测量、可定位、可聚合
- 软件工程：状态读写分离、ISP、DRY
- 第一性原理：状态是数据，必须有 schema + 单一接口契约

**形态约束下的实例化**：
- **编译期**：状态 schema 从 YAML codegen 出 Go 类型（确保编译期类型安全）
- **运行时**：状态读写经统一接口（`HealthAggregator` 等），**持久化交宿主**通过 adapter 注入

⇒ **A1**：状态有 schema 与归属（编译期 schema + 运行时接口）
⇒ **A2**：状态有唯一**接口契约**（不是唯一文件路径）
⇒ **A3**：状态可聚合到层级（运行时内存聚合，输出经 adapter）

### 4.2 目标②「意图可表达」

**三思想要求**：
- 系统工程：双向 traceability + 配置管理
- 软件工程：契约即代码 + 类型安全 + DIP
- 第一性原理：约束可形式化 + 单 owner

**形态约束下的实例化**：
- **编译期**：YAML → Go 代码（契约即代码的字面落地）
- **CI 期**：archtest 引擎执行规则（双向追溯、覆盖检测）
- **运行时**：不参与（意图在编译期已凝固到 binary）

⇒ **B1**：声明 ↔ 实现双向追溯（编译期 + CI 期 archtest）
⇒ **B2**：声明 / 规则数据化（CI 期规则引擎，规则即 YAML）
⇒ **B3**：单 owner 不可妥协（编译期检测）

### 4.3 目标③「系统自收敛」

**三思想要求**：
- 系统工程：闭环控制（observed → diff → action → observed）
- 软件工程：幂等 + 结构化错误 + testable
- 第一性原理：收敛需要距离 metric

**形态约束下的实例化**（最关键）：
- 收敛对象 = **仓库代码 + 元数据**（不是运行时集群状态）
- 收敛触发器 = git push / 定时 CI / AI Agent 巡检
- 收敛动作 = 开 PR / 写 advice / 升级人工 alert
- 持久层 = git（GoCell 唯一拥有的"etcd"）
- 执行者 = CI bot + Claude Code Agent

⇒ **C1**：闭环必须完整（**在 CI 期/AI Agent 域闭环**，不假设运行时 controller）
⇒ **C2**：每条规则带结构化 next-action（autofix PR / suggest / advisory / block / escalate）
⇒ **C3**：每条规则带距离 metric（**仓库 metric**：deprecation 剩余天数 / 覆盖率 / 死契约数 / 未关闭 finding 数）

### 4.4 横切要求

⇒ **D1**：分层依赖纯粹（编译期 import 图）
⇒ **D2**：fail-fast 优先于优雅降级（**限定编译期 + 启动期**；运行时降级是宿主选择）
⇒ **D3**：边界类型自有（公开 API 不暴露第三方 SDK 类型）
⇒ **E1**：编译期前移优先（§3.5 推出的工程总则，作为决策准则的横切原则）

---

## 5. 合流：GoCell 架构设计原则（13 条）

| 类别 | 编号 | 原则 |
|---|---|---|
| 状态 | P-A1 | 状态有 schema 与归属（编译期 schema + 运行时接口）|
| 状态 | P-A2 | 状态有唯一**接口契约**（持久化交宿主）|
| 状态 | P-A3 | 状态可聚合到层级 |
| 意图 | P-B1 | 声明 ↔ 实现双向追溯（编译期/CI 期）|
| 意图 | P-B2 | 声明/规则数据化（CI 期规则引擎）|
| 意图 | P-B3 | 单 owner 不可妥协 |
| 收敛 | P-C1 | 闭环必须完整（**CI 期/AI Agent 域**，不假设运行时 controller）|
| 收敛 | P-C2 | 每条规则带结构化 next-action |
| 收敛 | P-C3 | 每条规则带距离 metric（**仓库 metric** 优先）|
| 横切 | P-D1 | 分层依赖纯粹 |
| 横切 | P-D2 | fail-fast 优先（**限定编译期 + 启动期**）|
| 横切 | P-D3 | 边界类型自有 |
| 横切 | **P-E1** | **编译期前移优先**（任何能在编译期完成的事，不推到运行时）|

P-E1 是形态约束推出的独有原则，K8s 没有对应项（因为 K8s 没有"编译期"这个时间维度可以前移）。

---

## 6. GoCell 现状在三思想下的差距

### 6.1 系统工程视角
- 可观测性聚合层缺位（38 处 Health 各自实现）
- observed 状态归属未形式化（散落内存/进程/临时文件）
- **反馈回路开环**（仅 CI 期 detect，无 act → 缺 harvest）
- 故障容忍仅 LIFO 回滚，无 retry / self-heal
- 配置管理碎片化（18 个 Config struct）

### 6.2 软件工程视角
- governance 内聚崩坏（43 文件 / 15 rules_*.go 样板，违反 DRY/SRP）
- adapters→runtime/observability 反向依赖（DIP 失败）
- God interface（Cell.Init / Relay）
- amqp.* 等 SDK 类型泄漏（封装漏洞）
- archtest 仅返回 string error（错误未结构化）

### 6.3 第一性原理视角
- metadata.CellMeta vs cell.CellMetadata 双源（公理违例）
- observed 被当临时计算，不是数据
- FMT-18/19 strict-only 切两套规则集（应作为声明属性，被错做规则集合分裂）
- 规则仅"通过/失败"二值，无距离 metric
- Noop / DiscardPublisher 散落生产路径，违反 P-D2

### 6.4 三视角合一的总根因
GoCell 当前是**单世界（desired only）系统**，把 Bridle 三跃迁全部**降维处理**：
- 状态可见 → 状态被询问时返回
- 意图可表达 → 声明合法性检查
- 系统自收敛 → CI 期校验（仅 detect 不 act）

---

## 7. 强指导性意见（架构演进里程碑）

每个里程碑用语义化标识，明确**时间维度** + **推理链** + **怎么做**。

### M0-FOUNDATION：地基纯粹化（满足 P-D1/D2/D3 + P-B3）

**时间维度**：编译期 + 启动期。

**为什么必须**：公理违例不修则后续里程碑全部建立在不稳定地基上。

**怎么做**：
- `runtime/observability/poolstats` 接口下沉到 `kernel/observability/poolstats`，adapters 通过 DI 接收 sink
- Noop 集中到 `runtime/testutil/`，archtest `NOOP-PROD-IMPORT-01` 守护
- 合并 `metadata.CellMeta` 与 `cell.CellMetadata` 单一类型
- adapters 公开 API 重审：amqp.* / *sql.DB / *amqp.Error 全部包装

### M1-OBSERVED：状态接口收口（满足 P-A1/A2/A3）

**时间维度**：编译期 schema + 运行时接口（**不持久化为 yaml**）。

**为什么必须**：38 处 Health 重复违反 DRY/SRP；编程框架不能假设拥有持久层，必须把持久化责任移交宿主。

**怎么做**：
- 新建 `kernel/healthz` 接口包：`Aggregator` / `Probe` / `Snapshot` 类型
- 状态 schema 由 codegen 从 cell.yaml 生成 Go 类型（编译期）
- 默认实现：`runtime/observability/healthz/inmemory`
- 可选 adapter：`adapters/postgres/healthz` / `adapters/otel/healthz`（宿主选用或自己实现）
- archtest `HEALTHZ-WRITE-01`：禁运行时组件绕开 Aggregator 自定义 Health
- 38 处 Health 全部改为读 / 写 Aggregator
- **不创建 `generated/observed/*.yaml`**——持久化是宿主的事

### M2-LIFECYCLE：相位字段（满足 P-A1）

**时间维度**：编译期声明 + 运行时接口暴露（**不写回 yaml**）。

**怎么做**：
- `cell.yaml` / `slice.yaml` 加 `lifecycle: experimental | candidate | asset | maintenance | retired`（编译期声明，由 codegen 生成 Go 常量）
- governance 校验状态转移合法性（编译期）
- 运行时通过 `kernel/healthz.Aggregator` 接口暴露当前相位（与 desired 对比的差距由消费方计算）

### M3-RULE-ENGINE：CI 期规则引擎（满足 P-B2 + P-C2 + P-C3）

**时间维度**：CI 期。

**为什么必须**：15 个 rules_*.go 样板违反 DRY；规则需带 5 槽位（detect / evidence / next / level / harvest）才能驱动 harvest。

**怎么做**：
- `kernel/governance/engine.go`：唯一执行体
- `kernel/governance/rules/*.yaml`：64 条规则数据化（schema 含 5 槽位）
- `next-action` 类型：autofix / suggest / advisory / block / escalate
- 规则带 `metric`（距离函数：deprecation 剩余天数 / 覆盖率 / finding 数），不只是 bool
- 修 ADV-05 SeverityError 错分（一行）

### M4-COVERAGE：双向追溯（满足 P-B1）

**时间维度**：CI 期 archtest。

**为什么必须**：声明 → 实现是充分性，实现 → 声明是必要性。当前 GoCell 几乎只查正向。

**怎么做**：新增 5 条反向规则（**不含 SLICE-DECOUPLE，slice 同 cell 内不需要隔离**）：
- `IMPL-DECL-COVER-01`：跨 cell 的 Go import 必须经 contract（cell 间，非 slice 间）
- `HANDLER-DECL-COVER-01`：每个 http handler 必须出现在某 contract.yaml
- `EMIT-DECL-COVER-01`：每条 outbox emit 必须出现在某 contract.triggers
- `DEAD-CONTRACT-01`：active contract 必须有 handler 入口（语义漂移）
- `DEAD-CODE-01`：deprecated contract 引用代码不能在 main 分支

### M5-HARVEST：仓库收敛环（满足 P-C1）

**时间维度**：CI 期 + AI Agent 域（**不在运行时**）。

**为什么必须**：GoCell 只有编译期/CI 期对仓库有修改权限，运行时无权改宿主。Bridle Leap 3 在编程框架域必须落到这里。

**怎么做**：
- 收敛对象 = 仓库代码 + 元数据
- 触发器 = git push / 定时 CI / AI Agent 巡检
- 输入 = M3 规则引擎产出的 finding（带 next-action + metric）
- 动作分级：
  - autofix：CI bot 自动开 PR
  - suggest：写 advice 到 `harvest/` 目录
  - advisory：归入巡检报告
  - block：阻断当前 PR
  - escalate：人工 alert（R5）
- 双层 harvest 域分离：
  - `harvest/framework/`：GoCell 框架自演进
  - `harvest/app/{appID}/`：客户应用自演进（客户在自己仓库使用相同引擎，路径独立）
- 持久层 = git 本身（不需要新持久层）

### 7.1 演进顺序（修正：M5 不依赖 M1）

```
[M0-FOUNDATION] ──┬─→ [M1-OBSERVED] ─→ [M2-LIFECYCLE]
                  │                        （独立运行时分支）
                  │
                  └─→ [M3-RULE-ENGINE] ─→ [M4-COVERAGE] ─→ [M5-HARVEST]
                                              （CI 期分支）
```

| 里程碑 | 时间维度 | 依赖前置 |
|---|---|---|
| M0-FOUNDATION | 编译期/启动期 | — |
| M1-OBSERVED | 编译期/运行时接口 | M0 |
| M2-LIFECYCLE | 编译期/运行时接口 | M0（M1 已就绪后接入更顺）|
| M3-RULE-ENGINE | CI 期 | M0 |
| M4-COVERAGE | CI 期 | M3 |
| M5-HARVEST | CI 期 + AI Agent | M3 + M4 |

**关键修正**：M5-HARVEST 走的是仓库通道，**不依赖 M1-OBSERVED**（运行时分支）。这是与上一版的重要差别。M1/M2 是运行时形态完善，M3/M4/M5 是 CI 形态完善，两条分支可并行推进。

---

## 8. K8s 校准（同范式 / 等价异形）

K8s 是同范式（声明式 / 单源 / 校验链 / 闭环）但不同形态（集群运行时）。每条原则在两侧落到不同时间维度。

| 原则 | K8s 实例（运行时为主） | GoCell 等价异形 | 时间维度 |
|---|---|---|---|
| P-A1 | spec/status subresource | codegen 出 Go 类型 + 运行时接口 | 编译期 + 运行时 |
| P-A2 | apiserver 强制 status 由 controller 写 | `kernel/healthz.Aggregator` 接口 + adapter 注入 | 运行时接口 |
| P-A3 | conditions 层级 | 内存 Aggregator 树 + adapter 输出 | 运行时 |
| P-B1 | OwnerReference + finalizer | archtest 双向 + codegen 引用图 | 编译期 + CI 期 |
| P-B2 | OPA Gatekeeper（运行时 admission）| CI 期规则引擎 + 规则即 YAML | CI 期 |
| P-B3 | API GVK 唯一性（运行时）| 类型 owner 唯一（编译期）| 编译期 |
| P-C1 | controller manager 持续 reconcile（运行时）| CI bot + AI Agent harvest（仓库收敛）| **CI 期 + Agent 域** |
| P-C2 | Reconcile.Result + Event | next-action 五级（PR / advice / block / escalate）| CI 期 |
| P-C3 | ObservedGeneration vs Generation | 仓库 metric（剩余天数 / 覆盖率 / finding 数）| CI 期 |
| P-D1 | apimachinery / client-go 严格分层 | kernel→runtime→adapters 单向 import | 编译期 |
| P-D2 | initContainers + readinessProbe | 编译期 fail-fast + 启动期 fail-fast | 编译期 + 启动期 |
| P-D3 | typed client + scheme.Codec | adapters 公开 API 全 GoCell 类型 | 编译期 |
| **P-E1** | **（无对应——K8s 无编译期可前移）** | **任何能在编译期完成的事不推运行时** | 编译期 |

**校准结论**：12/13 在 K8s 找到等价异形（不是形式相同），证明推导落到了正确的工程抽象。**P-E1 是 GoCell 形态独有原则**，K8s 因没有"编译期"维度无对应项——这点反而验证了 GoCell 不是 K8s 翻版而是**同范式不同形态**的正确实例化。

---

## 9. 给 GoCell 架构师的决策准则（review 必问 12 条）

### 形态层（最高优先级）
- **#0**：**这件事应该在编译期还是运行时？默认前移到编译期**（P-E1）

### 系统工程层
- #1：状态有归属、时间维度、聚合层级吗？
- #2：规则失败有没有形成闭环（detect → action → metric）？
- #3：组件有故障容忍吗？

### 软件工程层
- #4：import 从下往上吗？
- #5：公开 API 用 GoCell 类型吗？
- #6：错误返回结构化对象还是字符串？
- #7：是新写还是已有抽象重复？

### 第一性原理层
- #8：唯一 owner 吗？
- #9：规则能数据化（不是 .go 代码）吗？
- #10：Noop 在生产被意外拾取会立即崩吗？
- #11：规则有距离 metric 吗（不只 bool）？

12 问回答全是"是"才合格。任何"否"都是架构债。**#0 优先级最高**——一旦放到运行时就回不来了。

---

## 10. 一句话定位

> GoCell 是**编程框架**而非集群运行时。下一阶段架构核心命题不是"复制 K8s 的运行时控制平面"，而是：
>
> **把 Bridle 三跃迁主要落在编译期 / CI 期 ——**
> **codegen 是状态可见的载体，archtest + 规则引擎是意图可表达的载体，**
> **harvest（CI bot + AI Agent 操作仓库）是系统自收敛的载体。**
>
> **运行时只暴露接口让宿主决定持久化与执行环境**（持久化交宿主、执行交宿主、降级交宿主）。
>
> K8s 把三跃迁全落在运行时，因为它拥有 etcd 与集群修改权限；GoCell 把三跃迁主要落在编译期/CI 期，因为只有那里它有完全控制权。**同范式、不同形态、不同时间维度的实例化** —— 这是对标 K8s 的正确方式。

---

## 附录：思想 → 形态 → 原则 → 落地的可追溯矩阵

| 原则 | 系统工程 | 软件工程 | 第一性原理 | 时间维度 | K8s 实例 | GoCell 里程碑 |
|---|---|---|---|---|---|---|
| P-A1 | 可观测性 | 封装 | 状态是数据 | 编译期 + 运行时 | spec/status | M1 + M2 |
| P-A2 | 配置管理 | DRY/SRP | 单源公理 | 运行时接口 | apiserver 强制 | M1 |
| P-A3 | 聚合 | 组合 | — | 运行时 | conditions 层级 | M1 |
| P-B1 | traceability | 契约即代码 | 充要条件 | 编译期 + CI 期 | OwnerReference | M4 |
| P-B2 | CI = 配置 | DRY | 形式化 | CI 期 | OPA / VAP | M3 |
| P-B3 | 配置管理 | SRP | 单源公理 | 编译期 | GVK 唯一 | M0 |
| P-C1 | 闭环 | testable | 收敛定义 | **CI 期 + Agent** | controller manager | **M5（仓库收敛）** |
| P-C2 | 执行动作 | 结构化错误 | 动作可表达 | CI 期 | Result+Event | M3 |
| P-C3 | 反馈量化 | — | 距离 metric | CI 期 | ObservedGeneration | M3 |
| P-D1 | 子系统分解 | DIP | 单向因果 | 编译期 | apimachinery 分层 | M0 |
| P-D2 | 故障显式 | 错误不吞 | 补丁不累积 | 编译期 + 启动期 | fail-fast probes | M0 |
| P-D3 | 边界 | 封装 | 协议显式 | 编译期 | typed client | M0 |
| **P-E1** | 早反馈 | 形态约束 | 权限模型 | 编译期 | （无）| 横切于 M0-M5 |
