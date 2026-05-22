# GoCell 核心分析报告：洋葱四层 + 能力分级

| 项 | 值 |
|---|---|
| 生成日期 | 2026-05-22 |
| 基准 commit | `85ec077e7`（develop） |
| 触发 | 用户提问"GoCell 核心是 outbox 还是什么？" |
| 本文定位 | **事实型核心解构**——不下设计建议、不评分、不补 finding |
| 真值源 | `.specify/memory/constitution.md` §核心原则 + `docs/design/master-plan.md` §2.3 + `docs/plans/framework-capability-gaps/202605131500-002-framework-capability-inventory.md` |
| 配套文档 | `20260504-engineering-capability-domain-map.md`（14 能力 + 6 交互模式）/ `20260504-first-principles-capabilities-and-replaceable-deps.md`（L0 包级盘点） |

---

## 0. 一句话结论

**GoCell 的核心不是 outbox，而是 Cell/Slice 治理模型 + Registry 装配运行时**（`kernel/cell` + `kernel/metadata` + `kernel/contractspec`）。outbox 是核心之上的**第二层四大支柱之一**（一致性内核），跟 auth / observability / bootstrap 平级。

宪法 master plan §2.3 原话："**slice-cell Kernel 是最大差异化**"。其他能力都是"为了让 Cell 模型跑起来"的配套。

把 outbox 看成核心，类比上等于把 Spring 的 `@Transactional` 当成 Spring 的核心——它很重要，但脱离 IoC 容器它什么都不是。

---

## 1. 洋葱四层

```
┌──────────────────────────────────────────────────────────┐
│  L4. 治理工具链                                          │  ← 让 L1-L3 "违反不可表达"
│      tools/archtest + cmd/gocell validate + codegen      │
├──────────────────────────────────────────────────────────┤
│  L3. 内置 Cell                                           │  ← 用户开箱即用的"业务能力"
│      accesscore / auditcore / configcore                 │
├──────────────────────────────────────────────────────────┤
│  L2. 四大内核支柱                                        │  ← 围绕 Cell 模型的能力底座
│      ┌─────────┬──────────┬──────────┬──────────┐         │
│      │ Outbox  │   Auth   │ Observ.  │Bootstrap │         │
│      │（L2一致）│（身份/Z） │（metric/ │（phase   │         │
│      │         │          │ trace/   │  化装配）│         │
│      │         │          │ audit）  │          │         │
│      └─────────┴──────────┴──────────┴──────────┘         │
├──────────────────────────────────────────────────────────┤
│  L1. 灵魂层                                              │  ← 一切围绕这里
│      Cell/Slice 模型 + Registry 装配                     │
│      - cell.yaml / slice.yaml / contract.yaml 元数据驱动 │
│      - kernel/cell.{Cell, Registry, Subscribe, Mount}    │
│      - 一致性等级 L0–L4 + 6 条真相（宪法 §II）           │
└──────────────────────────────────────────────────────────┘
```

每一层都"为下一层服务"——L4 守护 L1-L3 的契约；L3 用 L1+L2 编排业务；L2 给 L1 提供能力词汇；L1 是其余一切的根基。

---

## 2. 能力分级（"如果删掉，GoCell 还是 GoCell 吗"）

| 层 | 模块 | 删掉后果 | 不可替换性 |
|---|---|---|---|
| **L1 灵魂** | `kernel/cell` + `kernel/metadata` + `kernel/contractspec` + Registry | **GoCell 不复存在**，退化成普通 Go web 框架 | **不可替换** |
| **L2 内核支柱** | `kernel/outbox` + `runtime/outbox` | 退化到只能 L0/L1 一致性，跨 Cell 通信必须同步 RPC，等同 go-kit | **关键，但可替** |
| L2 内核支柱 | `runtime/auth`（JWT / Session / Service Token / RBAC） | 业务必须自己写 JWT/Session/RBAC，等同裸 chi | 关键但可替 |
| L2 内核支柱 | `runtime/observability` + `pkg/redaction` | 失去自动 cell-label / trace / 脱敏 fail-closed | 关键但可替 |
| L2 内核支柱 | `runtime/bootstrap` + `runtime/shutdown` | 退化到手写 main 串接，丢 LIFO 卸载 + readiness 翻转 | 关键但可替 |
| **L3 内置 Cell** | accesscore / auditcore / configcore | 失去开箱即用的身份/审计/配置，但 Cell 模型仍可用 | 可重写 |
| **L4 治理** | `tools/archtest` + `cmd/gocell validate` + codegen | 失去 AI-rebust 编译期保障，Cell 模型仍可跑 | 可缺席 |

---

## 3. Outbox 的准确定位

| 维度 | 说明 |
|---|---|
| 所在层 | **L2 内核支柱（4 个支柱之一）** |
| 在 GoCell 中扮演的角色 | 给 Cell 之间提供"一致性等级 L0–L4"中 **L2（OutboxFact）** 的实现原语 |
| 关键 API | `outbox.Emit` / `EntryHandler` / `HandleResult{Ack/Requeue/Reject}` / `Subscription` / `Settlement` / `ConsumerBase` |
| 关键不变量 | PG outbox lease_id CAS（fencing token）；ConsumerBase 两阶段 Claim/Commit/Release 幂等；DLX broker-native 路由 |
| 跟 Cell 模型的关系 | Cell 通过 `reg.Subscribe(spec, handler, cg, cellID)` 在 Registry 中声明订阅意图，bootstrap 把 RegistrySnapshot.Subscriptions drain 到 EventRouter——**outbox 是 Cell 模型的"事件传输实现"，不是 Cell 模型本身** |

**判断标准**：拿走 outbox，剩下的 `kernel/cell` + Registry + cell.yaml 仍能跑同步 HTTP RPC 形态的 Cell；拿走 Cell 模型，outbox 就只是个 transactional outbox 库（市面同类项目：watermill / go-broker / 自研 outbox 表）。

---

## 4. 跟同类框架的核心对照

| 框架 | 核心 | 类比中的"outbox 地位" |
|---|---|---|
| Spring | **IoC 容器**（`ApplicationContext` + `BeanFactory`） | `@Transactional` AOP—— 重要支柱，不是核心 |
| Kubernetes | **声明式 API + Controller Loop**（apiserver + controller-manager） | StatefulSet / Job —— controller 之上的具体形态 |
| Uber fx | **DI Graph + Lifecycle Hooks** | hooks 是核心机制，无对应 outbox |
| Kratos | **Service Layer + Transport 抽象** | event bus 是可选 transport 之一 |
| **GoCell** | **Cell/Slice 元数据模型 + Registry 装配** | **outbox 是 L2 一致性支柱**——重要，但不是核心 |

如果用一句话定义 GoCell：

> **"把 K8s 那套声明式分层架构思想，搬到 Go 单进程内做的 Cell-native 工程底座"**

outbox / auth / observability / bootstrap 都是这个思想下的具体落地。

---

## 5. 哪些信号能反向印证"核心是 Cell 模型"

证据散落在代码与文档约束中：

1. **宪法第一条原则**就是 "Cell-Native 分层架构"（`.specify/memory/constitution.md` §I），第二条是"Cell 治理与六条真相"（§II）——outbox 在宪法里直到 §VI（事件驱动与一致性等级）才出现，是规则不是核心。
2. **master plan §2.3 关键决策汇总**第一条："底座核心 = **slice-cell Kernel 是最大差异化**"——明确否决了"outbox / auth 是核心"的解读。
3. **包依赖方向**：`kernel/cell` 被 `kernel/outbox` / `runtime/outbox` / `runtime/auth` / `runtime/bootstrap` 全部依赖，反之不成立——依赖图最深处才是核心。
4. **codegen 主语**：`cmd/gocell generate cell` / `generate assembly` / `generate contract` 三个主命令全部以"cell 元数据"为输入，outbox 是 cell 内部的实现细节。
5. **archtest 守护焦点**：151 条规则中超过 60% 直接守 cell.yaml / slice.yaml / contract.yaml 派生的不变量（命名 / 依赖归属 / contract 对齐 / cellID 位置必填等），outbox 专属规则只有约 10 条（HANDLERESULT-FIELDS-FROZEN / LEASE-ID-CAS / SERVICE-01 等）。
6. **业务开发者 onboarding 第一课**：不是"如何用 outbox"，而是"如何写 cell.yaml + slice.yaml + 通过 validate"——见 docs/guides/ 与既有 onboarding 文档。

---

## 6. 衍生洞察

理解清"Cell 模型是核心、outbox 是支柱"之后，可以推出几个相关判断（不在本报告范围内展开，仅作 pointer）：

- **演进方向**：GoCell 进一步简化业务开发的杠杆点，应该投资在"让 Cell/Slice 元数据更少重复"（codegen 单源派生），而不是"让 outbox 更易用"——参见 `docs/plans/framework-capability-gaps/202605221303-006-spring-comparison-and-simplification-roadmap.md` §4 P0 三条。
- **跟 Spring 对照**：GoCell 的 Cell 模型对应 Spring 的 IoC 容器；outbox 对应 `@Transactional` + `@EventListener`；治理工具链对应 Spring 的注解扫描 + AOP 织入——参见同文 §1-§2。
- **跟 K8s 对照**：cell.yaml 对应 K8s 的 PodSpec；Registry 对应 controller；archtest 对应 admission webhook；codegen 对应 CRD generator。
