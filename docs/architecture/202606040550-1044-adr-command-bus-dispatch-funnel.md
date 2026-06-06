# ADR: Command Bus — dispatch funnel + handler registry（同步核心 PR-1）

- 日期：2026-06-04
- 状态：Accepted
- 范围：framework-capability-roadmap **W3**（Command Bus）/ issue #1044 **PR-1**（同步核心）。前置 D1 W0 outbox wire envelope、D2 W2 HTTP idempotency 均已落地。
- 关联：`docs/plans/framework-capability-gaps/202605131500-004-capability-gap-analysis.md` 缺口 4 / `…/202605162100-005-framework-capability-roadmap-plan.md` W3
- 对标：Watermill `components/cqrs/{command_bus,command_processor}.go`（ref 见 §6）

> **真值边界**：本 ADR 是 command-bus 设计决策的概念单源。各 enforcement 的符号清单 / 盲区 / 反向自检活在对应 archtest 的 package godoc（`tools/archtest/command_dispatch_funnel_test.go`）与 governance 实现（`kernel/governance/rules_command.go`），本文件只汇总决策 + 评级矩阵，不复制。PR-1 只交付**同步核心**；④ async outbox 桥 / ⑤ idempotency 桥落地时 **amend 本 ADR**（§5 演进路径 + §4 评级矩阵逐行重评）。

---

## 1. 上下文与问题

`kernel/cellvocab.ContractCommand = "command"` 这个 contract kind 早已在闭集中，`runtime/command` 却只有 dispatch registry + queue discovery（原 `SweeperLifecycle` 已于 PR-A8 #1169 删除，L4 设备命令超时生命周期迁至 `kernel/reconcile.Loop`），`kernel/command` 是 L4 设备队列状态机——**两者都不是 dispatcher**。CQRS 写侧（命令 → 单一 handler → 响应）无框架支持，业务只能手撕。004 缺口 4「Command Bus 半空壳」/ 005 W3 要求补齐：`Dispatcher` + handler registry + codegen `kind:command` 派生 typed Command/Handler + 与 outbox/idempotency 协同，**立项门 = funnel 双向锁（上游 codegen Hard + 下游 callsite Hard）**。

PR-1 交付其中的**同步 in-process 核心**：codegen 派生 typed `Handler`/`Register`/`Dispatch` + sealed `runtime/command.Registry` + funnel 双向锁 archtest + governance 校验。异步（写 command outbox、relay 触发）与 idempotency 桥拆为 #1044 子 issue。

---

## 2. 决策 D1–D7

| # | 决策 | enforcement 载体 | AI-robust 档位 |
|---|------|-----------------|---------------|
| **D1** | **每契约单态化 codegen 自由函数**，非泛型方法。issue 字面 `Dispatcher.Dispatch[C,R](ctx,cmd)(R,err)` **Go 不可表达**（方法不能有类型参数）。改为 `command.tmpl` 为每个 `kind:command` 契约生成单态 `Dispatch(ctx, reg, *Request)(*Response, error)` + `Register(reg, h Handler)`，完全对标 saga `BuildDefinition(impl Impl)`。 | `command.tmpl` → `command_gen.go` golden（`synth_command_command_gen_go.golden`）；`COMMAND-GEN-FUNNEL-SOLE-EMITTER-01` | **上游 Hard**（codegen funnel + golden 字节锁） |
| **D2** | **typed Handler 仅活在生成码**：typed `Handler` interface / `Register` / `Dispatch` 只在 `generated/contracts/command/**`（CLAUDE.md 禁手编 `generated/`）。业务要 type-safe 派发**必须**写 `contract.yaml`(kind:command,codegen:true) 跑 `gocell generate contract`——即 funnel。裸写 typed 命令 handler 不可表达。 | `COMMAND-GEN-FUNNEL-SOLE-EMITTER-01`（生产包外无手写 look-alike trio 声明） | **上游 Hard / 下游 Medium**（见 §4） |
| **D3** | **sealed Registry，异构 handler 装箱 `any`**：`runtime/command.Registry` 私有 `map[CommandID]any` + RWMutex。一个 registry 持有多个 typed Handler（每契约不同 Handler 类型），Go 异构 typed map **无法去 `any`/erasure**——同 saga 持 untyped `*Definition`。`RegisterHandler`/`LookupHandler` 收口写/读。 | `runtime/command/registry.go`（unexported 字段，包外不可构造/读写）；`COMMAND-DISPATCH-REGISTER-CALLER-01` | **下游 Hard / 上游 Medium**（见 §4） |
| **D4** | **同步 type-assert，不用 JSON round-trip**：生成 `Dispatch` 把 boxed handler 断言回 typed `Handler` 后直调。零序列化、in-process 惯用。saga 用 JSON 是因跨异步 step 边界；PR-1 同步不跨边界。JSON 统一性留 ④ 异步。 | 生成码形态（golden 锁） | 形态由 golden 锁（上游 Hard） |
| **D5** | **「编译期注册唯一性」= sole-emitter funnel + runtime KindConflict**。issue ③ 字面「编译期 Handler 注册唯一性」中**「编译期阻止第二次 runtime `Register` 调用」Go 不可表达**（运行时多次调用无法编译期拦）。落地 = (a) 编译期：typed `Register` 唯一来源（sole-emitter）；(b) 运行时：`RegisterHandler` 第二次同 id → `KindConflict`（对标 Watermill `DuplicateCommandHandlerError`）。 | `COMMAND-GEN-FUNNEL-SOLE-EMITTER-01`（a）+ `Registry.RegisterHandler` runtime guard（b） | a 上游 Hard / b runtime guard（Medium） |
| **D6** | **codegen fail-closed，无 stub 降级**：`kind:command,codegen:true` 缺 request **或** response schemaRef → `buildCommandSpec` 硬错（不静默生成空包）。governance `COMMAND-CONTRACT-SCHEMA-REF-01` 在 validate 期并行兜底。schemaRef 的作用 = **派生 typed `*Request`/`*Response` 签名**，**非**运行时值约束门；sync `Dispatch` 不执行 request value-validation（见 §Amendment 2026-06-04）。 | `contractgen.buildCommandSpec`（codegen Hard 半边）+ `COMMAND-CONTRACT-SCHEMA-REF-01`（governance Medium 兜底） | **codegen 上游 Hard + governance Medium**（同 saga step-schema-ref 范式） |
| **D7** | **layer = `runtime/command`**：dispatcher 核心入 `runtime/command`（已有 dispatch registry + queue discovery；原 SweeperLifecycle 已于 PR-A8 #1169 删除），依赖 `kernel/+pkg/`，不依赖 cells/adapters。生成码在 `generated/contracts/command/**` import `runtime/command`（`generated/` 可 import 任意层，无环）。 | 分层依赖规则（`go-standards.md`）+ build | 结构性（build 守） |

---

## 3. 拒绝的备选

- **运行时泛型自由函数** `command.Dispatch[C,R](ctx, reg, cmd)` + reflect/type-assert registry：**拒**——业务可手写 typed handler 调泛型 dispatch，**defeats 上游 Hard**（typed funnel 不再是生成码独占）。D1/D2 的单态化 codegen 是立项门必需。
- **无 registry，直接传 typed handler** `Dispatch(ctx, h Handler, req)`：更简、更 Hard（纯类型系统，无 `any`、无 Medium caller-allowlist），但**违背 issue ③「handler registry + 注册唯一性」**，且 ④ async（relay 只有 command id + payload，需按 id 查 handler）必然重新引入 registry——churn。registry-by-command-id 是 command bus 的**标准结构**（对标 Watermill `CommandProcessor.AddHandlers` 按 command 类型注册单一 handler），同步亦然，故 PR-1 即建。
- **`registration struct{ handler any }` wrapper 作「未来字段 seam」**：**拒**——预设未来需求；PR-1 用 `map[CommandID]any` 直存，④/⑤ 真需字段时再引入 struct（不预设、抽象前提到位再建）。
- **新增 `ERR_COMMAND_` 前缀 / code**：**拒**——`ERR_COMMAND_` 已注册、`ErrCommandNotFound` 已存在，复用之 + `ErrConflict`/`ErrInternal`/`ErrValidationFailed`，零 errcode 扇出。

---

## 4. Funnel 双向锁评级矩阵（诚实记录 Go 天花板）

issue 立项门要「上游 Hard + 下游 Hard」。**闭环 funnel 由两条 invariant 的 Hard 半边合成**；它们对称的另一半是 **Go 语言天花板**（非疏漏），与 `OUTBOX-RECONSTRUCTION-CALLER-01` / SPAN-SETATTR-HOLDER-SEAL(#851) / HEALTHZ-HOLDER-SEAL(#893) / outbox principal-write(#1282) 同族永久天花板。

| Invariant | 语句 | 上游 | 下游 |
|-----------|------|------|------|
| `COMMAND-GEN-FUNNEL-SOLE-EMITTER-01` | typed Handler/Register/Dispatch**/DispatchAsync**（#1667 起含 async）仅由 `command.tmpl` 派生（生产包外无手写 look-alike trio 声明） | **Hard**（codegen funnel + golden regenerate-and-diff 字节锁；`command.tmpl` 单一 emitter） | Medium（AST/types 声明扫描 + 反向 synth fixture） |
| `COMMAND-DISPATCH-REGISTER-CALLER-01` | `(*command.Registry).RegisterHandler`/`LookupHandler` 调用方 ⊆ `generated/contracts/command/**` + `runtime/command` 自测 | Medium（Go 无 friend-package：「仅生成码可调」不可编译期表达；archtest caller-allowlist 兜底） | **Hard**（`ResolveMethodCall` 按 pkg path + receiver type 绑定 callee，alias/同名异型不匹配） |
| `COMMAND-ASYNC-DISPATCH-CALLER-01`（#1667 / #1673） | `(*outbox.Relay).WithCommandDispatch` 必须是直接 method-call，其 dispatch-map 把生成 `DispatchID` const 映射到**同包**生成 `DispatchAsync`（`generated/contracts/command/**`），异步路径下游半边 | Medium（Go 无 friend-package：「relay 只接生成 DispatchAsync」不可编译期表达；archtest value-allowlist 兜底。forwarded func-var = #1508-family data-flow 残留。与同步 `…REGISTER-CALLER-01` 共用 gh #1575） | **Hard**（go/types 绑 value 的 `*types.Func` + key 的 `DispatchID *types.Const` 身份 = pkg path + name，alias-proof；**form-complete**：扫全部 WithCommandDispatch selector，method-value capture / method-expression 即违例（#1673 F2 闭 call-only 盲区）；key↔value 同源校验（#1673 F1，今日单包 vacuous，第 2 个 codegen command 自动 bites）） |

**闭环论证**：codegen-Hard 上游（typed funnel 不可手写，D1/D2）+ caller-allowlist-Hard 下游（raw `RegisterHandler` 在生成码外被调即 CI 红，D3）= 业务**既不能手写 typed funnel、也不能在 funnel 外用 raw registry** → 达成立项门「Hard 双向锁」。

**Medium 半边的 Hard 化路径（won't-do-now）**：把 `RegisterHandler`/`LookupHandler` 收进 `runtime/command/internal/` wrap 包使包外不可 import——但 `generated/` 与业务 cell 跨包，Go 包可见性无法表达「仅某几个生成包可调某导出符号」，与 #1282 family 同永久天花板。追踪：**gh #1575**（won't-do tracker，archtest godoc 点名），维持 Medium 为 Go 下天花板。

---

## 5. 演进路径（④/⑤ 落地时 amend 本 ADR）

PR-1 同步核心是 W3 的第一片。`Registry` map signature 与生成码 funnel 为后续保持**前向兼容的 seam**（不预设字段，需要时加）：

- **④ async outbox 桥（#1667，已落地——见 §Amendment 2026-06-06）**：codegen 派生单态 `DispatchAsync(ctx, reg, entry)`（从 `entry.Payload()` JSON unmarshal 回 typed `*Request` → `LookupHandler` → 复用同一 `Handler`，丢弃 `*Response` 回 `error`）；relay 按 routing-topic 在 composition-root 注入的 dispatcher-map 中匹配 command → 在进程内触发 `DispatchAsync`，否则发 broker。JSON marshal 在 outbox 边界发生（D4 同步 type-assert 路径不变）。**判别器 = routing-topic + dispatcher-map 成员，未改 sealed `Entry` wire envelope、未引入 metadata 约定 → 未触发 contract-fanout 5 载体**（原文「携带 command kind——触及 sealed Entry wire envelope 或 topic 约定，触发 contract-fanout」+「relay 消费按 command id LookupHandler」实现为：command entry 即 `eventType = command id` 的普通 entry，`LookupHandler` 留在生成 `DispatchAsync` 体内、relay 不直接调）。
- **⑤ idempotency 桥（#1044 子 issue）**：HTTP Idempotency-Key ↔ command_id 映射（复用 `runtime/http/idempotency` Claimer 两阶段）。Blocked-by ④。
- **command-entry 值校验 funnel（#1588，Blocked-by ④）**：`DispatchAsync` 只做 typed JSON unmarshal（typed struct 即结构契约），不执行 schema 值约束（minLength/required/…）；untrusted-payload 值校验在此不可信 async 边界落地（Amendment 2026-06-04 已点名归属）。
- ~~**command consistencyLevel governance（#1044 子 issue）**：PR-1 不锁 level~~ **已交付（#1668，双层）**：`COMMAND-CONTRACT-CONSISTENCY-LEVEL-01` 下界约束 `consistencyLevel ≥ L1`，仅拒 `L0`（命令跨本地边界至少需 L1 LocalTx 原子性，L0 LocalOnly 结构上不适用）。**双层**（同 PROJECTION-CONSISTENCY-01 单 ID 双层范式）：Hard = `types.tmpl` 编译期 `const _ = uint(cellvocab.<level> - cellvocab.L1)` 对 codegen:true 命令契约 uint 下溢拦 L0；Medium = governance rule 兜底 codegen:false + in-memory fixture。下界（非 exact-lock）使现有 active L4 `devicecommand` 契约全部通过、零误伤。详见 §Amendment 2026-06-06（#1668）。
- **真实 binary async producer 接线（#1044 子 issue，gh backlog，Blocked-by ④）**：④ 交付机制 + relay-level E2E（mem outbox store 绑生成 `enqueue.DispatchAsync` 扮演 composition root）；devicecell 异步 enqueue 命令 + iotdevice durable-mode relay `WithCommandDispatch` 接线随真实 producer 落地（给无 producer 的 consumer 接 binary wiring = dead wiring，故 defer）。

amend 时须回到 §4 矩阵逐行重评（ai-robust.md ADR amendment 必查）：见 §Amendment 2026-06-06。

---

## 6. 对标（Watermill cqrs）

`ref: watermill components/cqrs/command_bus.go` + `command_processor.go`：

- `CommandProcessor.AddHandlers` 强制**一 command 类型 ↔ 一 handler** 映射，重复注册返回 `DuplicateCommandHandlerError`（"command handler for command %s already exists"）→ 印证 D3/D5 的 `RegisterHandler`→`KindConflict`。
- 路由按 command 身份（Watermill 用反射 `Marshaler.Name(command)`；GoCell 用**显式 codegen `DispatchID`**——更 Hard，无反射）。
- Watermill `CommandBus.Send` 是 broker-async（发 topic）= GoCell ④ `DispatchAsync`；GoCell 的**同步 in-process `Dispatch` 是 GoCell 特化的 fast-path**，Watermill 无对应（broker-first）。registry-by-command-id 是 command bus 标准结构，同步亦适用。

---

## 7. Enforcement 索引（导航，单源在各载体 godoc）

| 载体 | ID / 名 | 文件 |
|------|---------|------|
| archtest | `COMMAND-GEN-FUNNEL-SOLE-EMITTER-01` / `COMMAND-DISPATCH-REGISTER-CALLER-01` | `tools/archtest/command_dispatch_funnel_test.go` |
| governance | `COMMAND-CONTRACT-SCHEMA-REF-01` / `COMMAND-CONTRACT-CONSISTENCY-LEVEL-01` | `kernel/governance/rules_command.go` |
| codegen | `kind:command` 生成器 + golden；`COMMAND-CONTRACT-CONSISTENCY-LEVEL-01` 编译期 const-guard | `tools/codegen/contractgen/{builder,generator}.go`（`validateCommandLevel`）+ `templates/{command,types}.tmpl` |
| runtime | sealed `Registry` | `runtime/command/registry.go` |

---

## Amendment 2026-06-04（F5：schemaRef 边界澄清 — value-validation 不在 sync fast-path）

**触发**：PR #1578 round-2 codex review finding C4/F5（Cx3，需人工裁决）。观察：D6 强制 `kind:command,codegen:true` 声明 request/response schemaRef，但 D4 的同步 `Dispatch` 把 boxed handler 断言回 typed `Handler` 后直调，**从不**对 typed `*Request` 执行该 schema 的值约束（`minLength`/`maxLength`/`required`/`additionalProperties`）。表面上「schemaRef 被强制存在但不被执行」是声明强于执行的不一致。

**裁决（Option A，#1578 用户确认）**——**D8：schemaRef = typed-signature 来源，value-validation 在不可信边界，不在 in-process sync fast-path**：

1. **schemaRef-presence（D6）的语义 = 派生 typed `*Request`/`*Response` 签名**，不是「运行时值约束契约」。D6 fail-closed 仍成立（缺 schemaRef ⇒ 无法派生 typed signature ⇒ 硬错），但其被强制的理由是「签名来源」而非「约束被执行」。两者不矛盾，本 amendment 只是把 D6 的*意图*显式化。

2. **sync in-process `Dispatch`（D4）刻意不执行 request value-validation**。三条理由：
   - **typed-struct 即契约**：调用方传入 typed `*Request`，Go 类型系统已强制字段*类型*；`additionalProperties:false` 在 typed struct 上**结构性不可违反**（无法塞未知字段）。剩余未执行约束仅 string 长度 / `required` presence。
   - **可信边界**：sync `Dispatch` 的调用方是 **in-process 第一方 Go 代码**（派发 cell），非不可信 wire 输入。JSON-schema 值约束是 untrusted-JSON 的 wire 卫生规则；在 in-process typed 调用上重复执行是对编程错误的 defense-in-depth，非安全/正确性边界。
   - **D4 = 零序列化 fast-path**：HTTP 的 `runtime/http/schemavalidate.Validator.Validate(ctx, body []byte)` 是 **JSON-bytes 校验器**；复用它必须先把 typed `*Request` marshal 回 JSON——正是 D4 拒绝的 round-trip。在 sync 路径强加 marshal+validate 直接违背 D4 的 in-process 惯用，故**不**复用 HTTP validator。

3. **value-validation 归属不可信 command-entry 边界**（= §5 演进路径的 ④/⑤）：HTTP→command（handler 在入 cell 前已校验 untrusted JSON）、async outbox→command（D4 已注明 JSON marshal 在 outbox 边界发生）。这些边界落地时，request schema 的值约束在**该处**执行——schemaRef 因此最终*被*执行，只是不在 in-process fast-path 上冗余重检。command-entry validation funnel 设计与 ④ async 共同落地，跟踪为 **#1044 子 issue**（`Discovered via /fix #1578 F5`）。

**§4 评级矩阵逐行重评（ai-robust.md ADR amendment 必查）**：本 amendment **不改 §4 任一格**。§4 双向锁矩阵约束的是 *dispatch/register funnel*（typed Handler/Register/Dispatch 仅由 codegen 派生 + raw `RegisterHandler`/`LookupHandler` 调用方收口），与 *request value-validation* 正交——后者既不放宽前者的上游/下游 Hard，也不新增伪造面。D4（golden 锁 sync 形态）、D6（codegen fail-closed 上游 Hard + governance Medium）评级不变；无 ✅→⚠️/❌ 降格，无需补偿措施。

**为何不 silent defer（非 lazy）**：真实 blocker = 正确实现（typed-struct 级约束 IR，不 JSON round-trip）是一个**全新 codegen 机制**（须自带 AI-robust 评级 + archtest + golden），且其唯一真实消费点是不可信 command-entry 边界——与 ④ async 共同设计才有正确 altitude；廉价实现（JSON round-trip）违背 D4。故 funnel 设计随 ④ 落地，本 PR 以 §D8 显式收口设计边界。

---

## Amendment 2026-06-06（④ async outbox 桥落地 — #1667）

**触发**：#1667 落地 §5 演进路径 ④（async outbox 桥），按 ai-robust.md「ADR amendment 必查」逐行重评 §4 + 重写矛盾原文。

**D9：DispatchAsync 镜像同步 Dispatch，复用 `Handler`，`LookupHandler` 留生成码内**。codegen 为每个 `kind:command,codegen:true` 契约派生 `DispatchAsync(ctx, reg, entry kout.Entry) error`（生成码内 `var _ command.AsyncDispatchFunc = DispatchAsync` 编译期自证），与同步 `Dispatch` 同构、唯一差异 = 从 `entry.Payload()` JSON unmarshal 回 typed `*Request`（同步直收 typed），其余 `LookupHandler` → type-assert `Handler` → 调 handler 一致。**复用同一 `Handler` 接口**（不新增 `HandlerAsync`）；async 丢弃 `*Response`、只回 `error` 供 relay settle/retry。`ctx = entry.RestoreContext(ctx)` 在调 handler 前还原 principal/observability 身份（跨 async 边界）。

**D10：判别器 = routing-topic + dispatcher-map 成员（零 Entry 改动、零 metadata）**。command entry 即 `eventType = command id` 的普通 `outbox.Entry`（`NewEntry(clk, ctx, commandID, payload)`，`RoutingTopic()` = command id）。relay 持 composition-root 经 `(*outbox.Relay).WithCommandDispatch(reg, map[CommandID]AsyncDispatchFunc)` 注入的 dispatcher-map + registry；`publishBatch` 每条 entry：命中 map → 进程内 `fn(ctx, reg, e.Entry)`（跳过 marshal + broker），否则发 broker。settle 复用 broker 路径 writeBack，但**失败分两类**（#1673 F3 / §Amendment 2026-06-06 round-2）：成功 `MarkPublished` = 命令已消费；**确定性框架错误**（reg nil / routing-topic ≠ DispatchID / decode 失败 / no-handler / wrong-type）由生成 `DispatchAsync` 经 `kout.NewPermanentError` 标记，relay `handleFailedEntry` 识别 → 直接 `MarkDead`（不耗重试预算，错配 entry fail-closed 不被错 handler 消费）；**handler 业务 error** 透传不 wrap → `MarkRetry` 至预算耗尽。**未改 sealed `kernel/outbox.Entry`、未引入 metadata 约定 → 未触发 contract-fanout 5 载体**；判别器 = 闭合 dispatcher-map 成员资格（命名空间 `command.*.v1` + 闭合 map 天然隔离事件 topic），比 metadata 标记更优（无 business-writable 伪造面）。reg 作为 `AsyncDispatchFunc` 位置参（非 composition-root 闭包捕获），使 map 值是 archtest 可静态解析的**直接生成符号**。

**§4 评级矩阵逐行重评（无格降级）**：

- `COMMAND-GEN-FUNNEL-SOLE-EMITTER-01`：覆盖**扩大**（`DispatchAsync` 纳入 sole-emitter free-func 名集合 {Register, Dispatch, DispatchAsync}，golden 字节锁含 DispatchAsync）；上游 Hard / 下游 Medium **不变**。
- `COMMAND-DISPATCH-REGISTER-CALLER-01`：**不变**。`DispatchAsync` 体内 `LookupHandler` 仍在 `generated/contracts/command/**`，调用方集合不动；relay 经注入的 generated `DispatchAsync`（free func），**从不直接调** `LookupHandler`/`RegisterHandler`，不新增 raw caller。
- **新增第三行** `COMMAND-ASYNC-DISPATCH-CALLER-01`（异步路径下游半边）：上游 Medium（Go 无 friend-package，共用 gh #1575）/ 下游 Hard（go/types value-resolution 绑生成 `DispatchAsync` 符号身份）。与上游 Hard 半边 `COMMAND-GEN-FUNNEL-SOLE-EMITTER-01`（DispatchAsync 入 sole-emitter + golden）合成异步路径闭环双向锁，与同步路径同结构。

**无 ✅→⚠️/❌ 降格，无补偿措施**：D10 未改 `Entry`，OUTBOX-ENTRY 系列封装与 §4 同步双向锁均不受影响；新增 async funnel 是**净增**覆盖。

**范围（显式 backlog，不 silent）**：本 PR = 机制（codegen DispatchAsync + relay 分支 + 注入 option + archtest + ADR）+ relay-level E2E（mem outbox store 绑生成 `enqueue.DispatchAsync` 扮演 composition root，`examples/iotdevice`）。**真实 binary async producer 接线**（devicecell 异步 enqueue + iotdevice durable relay 接线）→ gh backlog（area-eventing/type-feat/pri-p2，Blocked-by #1667）；值校验 = #1588；**确定性框架错误的 permanent 分类（→ MarkDead）已随 #1673 F3 落地**（与值校验正交——框架错误 100% 不可恢复），**值校验失败分类 + handler 显式 permanent 能力**随 #1588 细化（复用本 PR 落地的 `kout.NewPermanentError` settle 机制，无需重做）。注：5 个 `command` 契约中仅 `enqueue` 为 `codegen:true`（其余 4 个 `codegen:false`），故仅 `enqueue/v1/command_gen.go` 生成 DispatchAsync。

enforcement 索引（§7）新增：archtest `COMMAND-ASYNC-DISPATCH-CALLER-01`（`tools/archtest/command_dispatch_funnel_test.go`）。

---

## Amendment 2026-06-06（#1668：command consistencyLevel governance — 双层下界交付）

**触发**：§5 演进路径子项「command consistencyLevel governance」落地（#1668，拆自 #1044）。

**交付**：`COMMAND-CONTRACT-CONSISTENCY-LEVEL-01` —— `kind:command` 契约声明的 `consistencyLevel` 必须 **≥ L1**（LocalTx），`L0` 拒。命令跨本地边界（HTTP / async entry → cell）至少需单 cell 事务原子性；`L0 LocalOnly` 为纯 in-slice 计算语义，结构上不适用于命令。**下界**约束（非 exact-level lock），故现有 active L4 `devicecommand` 五契约 + synth_command（L4）全部通过，零误伤。

**双层形态（同 PROJECTION-CONSISTENCY-01 单 invariant ID 双层范式）**：

| 层 | 载体 | 覆盖 | 评级 |
|----|------|------|------|
| Hard 主门控 | `types.tmpl` 编译期 `const _ = uint(cellvocab.{{.ConsistencyLevel}} - cellvocab.L1)`（L0 → uint 下溢 → `go build` 失败）+ `builder.go::validateCommandLevel`（ParseLevel 守，使模板渲染合法 cellvocab identifier） | codegen:true 命令契约 | **上游 Hard**（codegen funnel + golden 字节锁；L0 不可表达为可编译树） |
| Medium 兜底 | governance `validateCOMMANDCONTRACTCONSISTENCYLEVEL01`（`gocell validate` PhaseBase CI gate） | 所有命令契约（codegen:false + in-memory ProjectMeta fixture） | **Medium**（archtest/runtime guard 不可达的 in-memory 向量兜底） |

empty / 非法 level 不由本规则报——由 `FMT-03`（contract consistencyLevel validity）+ parser 非空拒兜底，避免双报同一根因。

**为何前移而非延后（issue 原文 deferral 前提已失效）**：#1668 issue body 将 Hard 编译期门列为「未来，随 ④/⑤ codegen 形态稳定」。但 command codegen（D1 `command.tmpl`/`command_gen.go` golden + D6 `buildCommandSpec` + `types.tmpl`）在 PR-1 即已交付并 golden-lock——延后前提已蒸发。Hard 半边是 `types.tmpl` 现成 const-guard idiom 的复制（projection L3 → command L1），低成本且与 projection 结构对齐，故同 PR 双层交付（彻底 + AI-Hard）。

**§4 评级矩阵逐行重评（ai-robust.md ADR amendment 必查）**：本 amendment **不改 §4 任一格**。§4 双向锁矩阵约束的是 *dispatch/register funnel*（typed Handler/Register/Dispatch 仅由 codegen 派生 + raw `RegisterHandler`/`LookupHandler` 调用方收口）。`COMMAND-CONTRACT-CONSISTENCY-LEVEL-01` 约束的是 *contract consistencyLevel 下界*，与之**正交**——既不放宽 funnel 上游/下游任一 Hard，也不新增伪造面。§4 两行 invariant **逐行重评**：

- `COMMAND-GEN-FUNNEL-SOLE-EMITTER-01`：✅ 评级不变（上游 Hard / 下游 Medium）——本规则不触及 typed Handler/Register/Dispatch 派生。
- `COMMAND-DISPATCH-REGISTER-CALLER-01`：✅ 评级不变（上游 Medium / 下游 Hard）——本规则不触及 `RegisterHandler`/`LookupHandler` caller-allowlist。

无 ✅→⚠️/❌ 降格，无需补偿措施。**不触发 contract-fanout**（`consistencyLevel` 既有字段，新增对它的约束 ≠ wire schema 改动）。

---

## Amendment 2026-06-06 round-2（#1673 review fix：F1 同源 + F2 form-complete + F3 permanent settle）

**触发**：#1673（④ async dispatch 落地 PR）codex review，3 条 Cx3 闭环。按 ai-robust.md「ADR amendment 必查」逐行重评 §4 + 重写矛盾原文（D10「失败 `MarkRetry`」+ §"范围"「permanent/transient 分类随 #1588 细化」均已就地改写）。

**F1（identity 错配闭环，Cx3）**：原 ④ 的 `WithCommandDispatch(reg, map[CommandID]AsyncDispatchFunc)` 把 command-id（key）与 dispatcher（value）作为 composition-root 两个自由输入，可错配；生成 `DispatchAsync` 用写死的 `LookupHandler(DispatchID)`、不校验 entry topic。两层闭环：

- **上游 Hard（codegen funnel + golden）**：`command.tmpl` 的 `DispatchAsync` 体首加 trust-boundary 自检 `if entry.RoutingTopic() != string(DispatchID) { return permanent }`——错配 entry 消费时 fail-closed（永久错误 → MarkDead，配 F3），不被错 handler 消费。模板单源 + golden 字节锁，所有 `codegen:true` command 自动获得。
- **下游 Medium→Hard（archtest 同源）**：`COMMAND-ASYNC-DISPATCH-CALLER-01` 扩展校验 map key 是**同包**生成 `DispatchID` const（不只 value 是 `DispatchAsync`）。今日单包 vacuous，第 2 个 codegen command 自动 bites。

**不引入 `AsyncDispatchBinding`**：topic guard 已在 trust boundary 消除「错配 → 错 handler 消费」的安全后果；binding 仅把 wiring 错配从 CI-catch 提前到 compile-catch、边际收益递减且增新类型——按「优雅简洁」放弃（topic guard = runtime 自证，archtest 同源 = wiring CI 兜底，两者已闭环）。

**F2（archtest form-complete，Cx2）**：§4 第 3 行原下游 Hard 声称「form-unique」，但实现 `isWithCommandDispatchCall` 只匹配 `call.Fun` 直接 SelectorExpr——method-value capture（`f := r.WithCommandDispatch; f(reg, badMap)`，间接 call 的 Fun 是 Ident）与 method-expression（args 偏移）绕过整个 map 扫描，**实现未达声称**。修复：改扫**全部** `WithCommandDispatch` selector（`EachInSubtree[SelectorExpr]` + `ResolveMethodCall`），非直接-method-call 形态即违例，对齐已验证的 `COMMAND-DISPATCH-REGISTER-CALLER-01` 范式；RED fixture 补 capture + method-expression 两形态，self-check 分别断言 `total≥4` + `form≥2`。下游 Hard 由此名副其实。

**F3（permanent/transient settle，Cx3）**：见上 D10 重写。生成 `DispatchAsync` 5 类确定性框架错误经 `kout.NewPermanentError` 标记；relay `publishBatch` 对 `MarshalEnvelope` 失败同样 wrap permanent（搭车，同类确定性永久）；`handleFailedEntry` 入口 `isPermanentDispatch(err)`（`errors.As(*kout.PermanentError)`）→ permanent 直接 `MarkDead`，handler 业务 error（不 wrap）走既有 retry 预算；dead-letter slog 加 `permanent` 布尔区分 permanent-dead 与 budget-exhausted-dead。

**§4 矩阵逐行重评（无格降级）**：

- `COMMAND-GEN-FUNNEL-SOLE-EMITTER-01`：**不变**（golden 字节锁随 `DispatchAsync` topic-guard + permanent-wrap 更新，仍 codegen 单源；上游 Hard / 下游 Medium）。
- `COMMAND-DISPATCH-REGISTER-CALLER-01`：**不变**。
- `COMMAND-ASYNC-DISPATCH-CALLER-01`：覆盖**增强**（key 同源 F1 + form-complete F2），评级**保持** Medium 上游 / Hard 下游——F2 把下游 Hard 从「声称」变「名副其实」（非降级），F1 同源是净增覆盖。
- **无 ✅→⚠️/❌ 降格**：D10 未改 `Entry` wire envelope，OUTBOX-ENTRY 系列与同步双向锁不受影响；settle 行为变更（permanent → `MarkDead`）是 outbox store 状态机内事件、非 wire 契约扇出（未触发 contract-fanout 5 载体——无 interface 签名 / schema / migration / errcode 新增）。

**F4（可观测维度）不在本轮**：命令 dispatch 与 broker publish 共享 `published` stat/metric 的 sink 维度区分，因今日 0 命令 producer（dead observability，sink 维度命名需真实流量决定），完整并入 **#1674**（已含 F4/F5/F6）随真实 producer 接线统一落地。

**eventbus.md「Relay 命令分发」节**同步重写 settle 语义（原「失败 `MarkRetry`」）。
