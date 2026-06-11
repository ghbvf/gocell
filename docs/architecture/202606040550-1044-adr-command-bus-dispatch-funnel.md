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
| `COMMAND-ASYNC-DISPATCH-CALLER-01`（#1667 / #1673 / #1698 3-arg） | `(*outbox.Relay).WithCommandDispatch(reg, dispatch, claimer)`（#1698 起 **3-arg**，claimer 必填位置参）必须是直接 method-call，其 dispatch-map 把生成 `DispatchID` const 映射到**同包**生成 `DispatchAsync`（`generated/contracts/command/**`），异步路径下游半边 | Medium（Go 无 friend-package：「relay 只接生成 DispatchAsync」不可编译期表达；archtest value-allowlist 兜底。forwarded func-var = #1508-family data-flow 残留。与同步 `…REGISTER-CALLER-01` 共用 gh #1575） | **Hard**（go/types 绑 value 的 `*types.Func` + key 的 `DispatchID *types.Const` 身份 = pkg path + name，alias-proof；**form-complete**：扫全部 WithCommandDispatch selector，method-value capture / method-expression 即违例（#1673 F2 闭 call-only 盲区）；key↔value 同源校验（#1673 F1，今日单包 vacuous，第 2 个 codegen command 自动 bites）） |
| `COMMAND-ASYNC-EMIT-FUNNEL-01`（#1698） | 任何 `command.*`-topic 的 `kout.Emit` / `kout.NewEntry` 必须在 `runtime/command` 包内（即异步命令只能经 sanctioned `command.EmitAsync` 出口构造 + 发射）；producer 侧上游半边 | **Medium**（Go 无 friend-package：「业务只能经 EmitAsync emit 异步命令」不可编译期表达；producer↔relay 经 async outbox store 解耦，「每条异步命令必带身份」无法端到端编译期 Hard——同 ConsumerBase 运行期 key 构造 / #1282·#851·#893 族结构天花板。archtest caller-allowlist 兜底；**won't-do ceiling**，per-command codegen extractor 不抬升该天花板，**不开伪 Hard-升级 issue**） | **Hard**（`EmitAsync` 的 subject/commandID 为 required 位置参 → happy-path「emit 异步命令但无身份槽」编译不可表达） |

**闭环论证**：codegen-Hard 上游（typed funnel 不可手写，D1/D2）+ caller-allowlist-Hard 下游（raw `RegisterHandler` 在生成码外被调即 CI 红，D3）= 业务**既不能手写 typed funnel、也不能在 funnel 外用 raw registry** → 达成立项门「Hard 双向锁」。

**Medium 半边的 Hard 化路径（won't-do-now）**：把 `RegisterHandler`/`LookupHandler` 收进 `runtime/command/internal/` wrap 包使包外不可 import——但 `generated/` 与业务 cell 跨包，Go 包可见性无法表达「仅某几个生成包可调某导出符号」，与 #1282 family 同永久天花板。追踪：**gh #1575**（won't-do tracker，archtest godoc 点名），维持 Medium 为 Go 下天花板。

---

## 5. 演进路径（④/⑤ 落地时 amend 本 ADR）

PR-1 同步核心是 W3 的第一片。`Registry` map signature 与生成码 funnel 为后续保持**前向兼容的 seam**（不预设字段，需要时加）：

- **④ async outbox 桥（#1667，已落地——见 §Amendment 2026-06-06）**：codegen 派生单态 `DispatchAsync(ctx, reg, entry)`（从 `entry.Payload()` JSON unmarshal 回 typed `*Request` → `LookupHandler` → 复用同一 `Handler`，丢弃 `*Response` 回 `error`）；relay 按 routing-topic 在 composition-root 注入的 dispatcher-map 中匹配 command → 在进程内触发 `DispatchAsync`，否则发 broker。JSON marshal 在 outbox 边界发生（D4 同步 type-assert 路径不变）。**判别器 = routing-topic + dispatcher-map 成员，未改 sealed `Entry` wire envelope、未引入 metadata 约定 → 未触发 contract-fanout 5 载体**（原文「携带 command kind——触及 sealed Entry wire envelope 或 topic 约定，触发 contract-fanout」+「relay 消费按 command id LookupHandler」实现为：command entry 即 `eventType = command id` 的普通 entry，`LookupHandler` 留在生成 `DispatchAsync` 体内、relay 不直接调）。
- **⑤ idempotency 桥（#1669 映射半 + #1698 消费半，已落地）**：HTTP Idempotency-Key ↔ command_id 映射（复用 `runtime/http/idempotency` 派生 key + `kernel/idempotency.Claimer` 两阶段）。**映射原语半已落地（#1669 PR-A）**：`runtime/http/idempotency` sealed funnel 扩第二构造器 `DeriveCommandKey(tenant, subject, command_id)`——纯编译期形态，产同一 sealed `IdempotencyKey`、流同一 `Store.Claim` sink（详见 ADR-1449 §Amendment 2026-06-07）；#1610 cross-cell 同槽路由消费它。**Claimer-wrap 消费半已落地（#1698 PR-B，见 §Amendment 2026-06-08）**：`kernel/idempotency.Claimer` 两阶段包裹 relay 命令分发 + 三态生命周期；sealed-key→Claimer string-key 经新增 `IdempotencyKey.Flat()` 扁平化（node-agnostic）；per-instance 身份经 `outbox.Entry` 的 `AggregateID(subject)` + business-metadata（command_id）承载，随真实 devicecell 异步 command producer 一并落地。**§4 评级矩阵新增「命令幂等身份双向锁 funnel」行（见 §Amendment 2026-06-08）**，无 ✅→⚠️/❌ 降格。
- ~~**command-entry 值校验 funnel（#1588，Blocked-by ④）**：`DispatchAsync` 只做 typed JSON unmarshal，不执行 schema 值约束~~ **已交付（#1588，见 §Amendment 2026-06-08）**：`DispatchAsync` 在 topic-guard 之后、unmarshal 之前对 `entry.Payload()`（已是 wire JSON bytes）跑 `runtime/schemavalidate.Validator.Validate` 强制 request schema 值约束（minLength/maxLength/required/additionalProperties），违例 → `kout.NewPermanentError` → relay `MarkDead`。复用 HTTP 同源 validator（抽中性包 `runtime/schemavalidate`）；honor D4——async bytes 校验非 marshal round-trip（round-trip 仅在校验 sync typed 输入时出现，sync `Dispatch` 不变）。
- ~~**command consistencyLevel governance（#1044 子 issue）**：PR-1 不锁 level~~ **已交付（#1668，双层）**：`COMMAND-CONTRACT-CONSISTENCY-LEVEL-01` 下界约束 `consistencyLevel ≥ L1`，仅拒 `L0`（命令跨本地边界至少需 L1 LocalTx 原子性，L0 LocalOnly 结构上不适用）。**双层**（同 PROJECTION-CONSISTENCY-01 单 ID 双层范式）：Hard = `types.tmpl` 编译期 `const _ = uint(cellvocab.<level> - cellvocab.L1)` 对 codegen:true 命令契约 uint 下溢拦 L0；Medium = governance rule 兜底 codegen:false + in-memory fixture。下界（非 exact-lock）使现有 active L4 `devicecommand` 契约全部通过、零误伤。详见 §Amendment 2026-06-06（#1668）。
- **真实 binary async producer 接线（#1698 PR-B，已落地——见 §Amendment 2026-06-08）**：archetype ① **事件反应式**——`examples/iotdevice/cells/devicecell/slices/devicebootstrap` 订阅 `event.device-registered.v1` → handler 经 `command.EmitAsync` emit `command.devicecommand.enqueue.v1`（包进 `CellTxManager.RunInTx`，durable PG outbox writer 要 tx）；`examples/iotdevice/run.go` demo + durable 两模式都接命令-relay 子系统（首个生产 `WithCommandDispatch` callsite）。**archetype ② reconcile→command 已落地（#1757，见 §Amendment 2026-06-10）**——`devicecertrenewal.Reconciler`（reconcile.Loop，TickerTrigger）扫近过期证书 → `command.EmitAsync` emit `command.devicecommand.enqueue.v1`（`commandType: rotate-cert`），复用 #1698 wrap + 既有 dispatcher-map（无新 map 项）。其余 archetype（③ saga step→command）仍复用本 wrap，随各自 PR 落地。

> **概念修正（Amendment 2026-06-10）**：本行原把 archetype ② 记为「#1757 同步 HTTP→command」，与 #1669 ⑤ 设计讨论选定的 ② = **reconcile→command** 不符（archetype 在该讨论中重编号：① 事件反应式 = #1698、② reconcile→command = #1757、③ saga step→command 独立 issue）。已就地修正。

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
| archtest | `COMMAND-GEN-FUNNEL-SOLE-EMITTER-01` / `COMMAND-DISPATCH-REGISTER-CALLER-01` / `COMMAND-ASYNC-DISPATCH-CALLER-01`（#1698 3-arg） | `tools/archtest/command_dispatch_funnel_test.go` |
| archtest | `COMMAND-ASYNC-EMIT-FUNNEL-01`（#1698 producer 上游半边） | `tools/archtest/command_async_emit_caller_test.go` |
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
   - **D4 = 零序列化 fast-path**：`schemavalidate.Validator.Validate(ctx, body []byte)` 是 **JSON-bytes 校验器**；在 **sync** 路径复用它必须先把 typed `*Request` marshal 回 JSON——正是 D4 拒绝的 round-trip，故 sync `Dispatch` **不**复用 validator。**注意此论据只约束 sync 路径**：async `DispatchAsync` 的输入 `entry.Payload()` 本就是入站 JSON bytes，对它直接 `Validate` 不产生任何 marshal round-trip，故 async 边界复用同一 validator 与 D4 不矛盾（见 §Amendment 2026-06-08）。

3. **value-validation 归属不可信 command-entry 边界**（= §5 演进路径的 ④/⑤）：HTTP→command（handler 在入 cell 前已校验 untrusted JSON）、async outbox→command（D4 已注明 JSON marshal 在 outbox 边界发生）。这些边界落地时，request schema 的值约束在**该处**执行——schemaRef 因此最终*被*执行，只是不在 in-process fast-path 上冗余重检。command-entry validation funnel 设计与 ④ async 共同落地，跟踪为 **#1588**（`Discovered via /fix #1578 F5`）——**async outbox→command 边界已交付（见 §Amendment 2026-06-08）**；HTTP→command 边界今日无 wiring，落地时复用 HTTP handler 既有 validator（sync `Dispatch` 前已校验 untrusted body）。

**§4 评级矩阵逐行重评（ai-robust.md ADR amendment 必查）**：本 amendment **不改 §4 任一格**。§4 双向锁矩阵约束的是 *dispatch/register funnel*（typed Handler/Register/Dispatch 仅由 codegen 派生 + raw `RegisterHandler`/`LookupHandler` 调用方收口），与 *request value-validation* 正交——后者既不放宽前者的上游/下游 Hard，也不新增伪造面。D4（golden 锁 sync 形态）、D6（codegen fail-closed 上游 Hard + governance Medium）评级不变；无 ✅→⚠️/❌ 降格，无需补偿措施。

**为何不 silent defer（非 lazy）**：真实 blocker = 值校验的唯一真实消费点是不可信 command-entry 边界，须与 ④ async（JSON 真正跨不可信边界处）共同设计才有正确 altitude。本 PR 以 §D8 显式收口设计边界，funnel 随 ④ 落地。

> **修订（Amendment 2026-06-08，#1588 落地后回填）**：本段原把「typed-struct 级约束 IR（不 round-trip）」称为「正确实现」、把复用 byte-validator 称为「廉价实现违背 D4」。该判断基于「校验须覆盖 sync 路径、对 sync typed 输入 marshal 即 round-trip」的隐含前提，对**最终裁决（值校验只在 async 不可信边界、sync 不校验）不成立**：async 边界 `entry.Payload()` 已是入站 JSON bytes，复用 `schemavalidate` byte-validator 零 round-trip、honor D4，且全 schema 覆盖。#1588 因此采纳 byte-validator 复用（option b），typed-struct IR（option a）评估后因覆盖不全（仅 len/required，无 nested/pattern/enum）+ 需另起全新 archtest+golden 机制而**否决**。值校验落生成 `DispatchAsync` 体内、由既有 `COMMAND-GEN-FUNNEL-SOLE-EMITTER-01` golden 锁，无新 enforcement 机制。详见 §Amendment 2026-06-08。

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

---

## Amendment 2026-06-08 — ⑤ PR-B Claimer-wrap 消费半 + 首个真实异步 producer 落地（#1698）

**触发**：#1698 落地 §5 演进路径 ⑤ 的 **Claimer-wrap 消费半** + **真实 binary async producer 接线**（archetype ① 事件反应式 devicecell）。按 ai-robust.md「ADR amendment 必查」逐行重评 §4 + 重写矛盾原文（§5「Claimer-wrap 消费半延期」/「真实 binary producer defer」/「0 生产 WithCommandDispatch callsite」+ §4 第 3 行 2-arg 形态均已就地改写）。

**交付**（机制 + 首个生产 producer + 生产 wiring）：

1. **命令幂等身份双向锁 funnel**（`runtime/command/command_idempotency.go`）：
   - `const CommandIDMetadataKey = "gocell.command.idempotency_id"`（单源声明，producer + relay 共引；`gocell.command.` 前缀刻意避开 `kout.ReservedMetadataKeys` observability/principal 命名空间——commandID 是命令实例 dedup token，非 principal/observability 身份，故落 producer-owned business metadata map）。
   - producer funnel `EmitAsync[T](ctx, clk, emitter kout.Emitter, dispatchID CommandID, subject, commandID string, payload T) error`——唯一 sanctioned 异步命令 emit 出口，subject/commandID 必填位置参（**编译期 Hard**：「emit 异步命令但无身份槽」happy-path 不可表达）。内部 `NewEntry(topic=dispatchID)` + `WithAggregateID(subject)` + `WithMetadata{CommandIDMetadataKey: commandID}` + `emitter.Emit`。
   - relay funnel `ClaimKeyFromEntry(e kout.Entry) (key string, ok bool)`：tenant = `Principal().TenantID`（ctx 传播、`NewEntry` 注入；可空并映射为 `_notenant`）、subject = `AggregateID()`、commandID = `Metadata()[CommandIDMetadataKey]`；subject 或 commandID 为空 → `ok=false`（relay fail-closed dead-letter）；否则 `idemkey.DeriveCommandKey(tenant, subject, commandID).Flat()`。
   - `IdempotencyKey.Flat() string`（`runtime/http/idempotency/key.go`）= `ns + "\x00" + key`，node-agnostic，sealed key → Claimer string-key 唯一扁平化出口（详见 ADR-1449 §Amendment 2026-06-08）。

2. **relay Claimer 两阶段 + 破坏式 3-arg**（`runtime/outbox/relay.go` / `relay_command.go`）：`WithCommandDispatch(reg, dispatch, claimer)` **2→3 arg 破坏式改**（claimer 必填位置参，**编译期不可表达「接命令分发但无去重」**）；`Start()` nil-guard（dispatch 非空但 claimer nil → fail-fast）。命令分支：`ClaimKeyFromEntry` → `!ok` → `kout.NewPermanentError`（MarkDead，fail-closed）；否则 `Claimer.Claim`：**Acquired** → dispatch + Commit/Release；**Done** → 跳过 dispatch + MarkPublished（去重）；**Busy** → MarkRetry；**Claim infra err** → MarkRetry。

3. **command_id 取值 = 源事件 `entry.ID()`**（确定性、重投稳定；不碰 sealed envelope）。tenant = principal.TenantID；subject = device id。

4. **首个真实 producer**（archetype ① 事件反应式，`examples/iotdevice/cells/devicecell/slices/devicebootstrap`）：订阅 `event.device-registered.v1` → handler 经 `command.EmitAsync` emit `command.devicecommand.enqueue.v1`，**包进 `CellTxManager.RunInTx`**（durable PG outbox writer 要 tx）。

5. **生产 wiring**（`examples/iotdevice/run.go`，**首个生产 `WithCommandDispatch` callsite**）：demo + durable **两模式都接**命令-relay 子系统——demo `outboxtest.FakeStore`（store = writer 同一实例）/ durable `adapterpg.NewOutboxStore` + `NewOutboxWriter`；单一共享 `idempotency.NewInMemClaimer` 同喂 relay command-dispatch + ConsumerBase；`bootstrap.WithRelay` + `WithConsumerBase`；bootstrap emitter = WriterEmitter over store-writer，txManager demo `outbox.DemoCellTxManager()` / durable `persistence.WrapForCell(adapterpg.NewTxManager(pool))`。

6. **archtest**：新 `COMMAND-ASYNC-EMIT-FUNNEL-01`（`tools/archtest/command_async_emit_caller_test.go`）；`COMMAND-ASYNC-DISPATCH-CALLER-01` 更新到 3-arg。

**§4 评级矩阵逐行重评（无格降级）**：

- `COMMAND-GEN-FUNNEL-SOLE-EMITTER-01`：**不变**（上游 Hard / 下游 Medium）——本 PR 不触及 typed Handler/Register/Dispatch/DispatchAsync 派生。
- `COMMAND-DISPATCH-REGISTER-CALLER-01`：**不变**（上游 Medium / 下游 Hard）——`RegisterHandler`/`LookupHandler` caller-allowlist 不动（devicecell producer 经 `EmitAsync`、relay 经生成 `DispatchAsync`，均不直接调）。
- `COMMAND-ASYNC-DISPATCH-CALLER-01`：**评级不变**（上游 Medium / 下游 Hard），形态从 2-arg 更新到 3-arg（claimer 必填位置参纳入 method-call 形态校验，下游 Hard 覆盖**净增**）。
- **新增行 `COMMAND-ASYNC-EMIT-FUNNEL-01`**（producer 上游半边）：**下游 Hard**（`EmitAsync` subject/commandID required 位置参，happy-path「无身份槽」编译不可表达）+ **上游 Medium**（Go 无 friend-package，archtest caller-allowlist：`command.*`-topic 的 `kout.Emit`/`NewEntry` 必须在 `runtime/command`）。

**Medium 结构天花板（诚实记 won't-do ceiling，不伪装延期 Hard）**：producer↔relay 经 async outbox store **解耦**，「每条异步命令必带身份」**无法端到端编译期 Hard**——producer 把命令写成 store 里的 `outbox.Entry`，relay 异步读回，二者不在同一调用栈，Go 类型系统无法表达「凡进 store 的 command-topic entry 必带 commandID」。这与 ConsumerBase 运行期 key 构造 / #1282·#851·#893 同族**永久结构天花板**。补偿 = 上游 bypass archtest（`COMMAND-ASYNC-EMIT-FUNNEL-01`，禁 funnel 外构造 command-topic entry）+ 下游 fail-closed relay（`ClaimKeyFromEntry` 缺身份 → `MarkDead`）**双锁兜底**。**不开伪 Hard-升级 issue**——per-command codegen extractor 不会抬升该结构天花板（producer/relay 跨 async 边界是 command-bus 的本质形态，非可消除的实现缺陷）。

**无 ✅→⚠️/❌ 降格，无补偿措施缺口**：⑤ PR-B 未改 `kernel/outbox.Entry` wire envelope（commandID 走既有 business-metadata map + AggregateID，均 producer-owned 既有字段）；OUTBOX-ENTRY 系列封装 + §4 同步双向锁均不受影响；新增 producer funnel 是**净增**覆盖。**contract-fanout 处置（正向陈述）**：`WithCommandDispatch` 的 2→3 arg 变更是 `runtime/outbox` 内部 builder method（非跨 cell conformance 接口 / 非 wire schema），其全部 callsite——1 生产（`examples/iotdevice/run.go`）+ N 测试（`relay_command*_test.go` / `command_dedup_e2e_test.go`）——已在本 PR 内同步更新到 3-arg；该签名不进入 `contract.yaml` / generated conformance / migration / errcode 任一载体，故 contract-fanout 的机器守卫（IMPL-DECL / HANDLER-DECL / EMIT-DECL / DEAD-CONTRACT）覆盖的跨 cell wire 契约不受其影响。
## Amendment 2026-06-08（#1588：command-entry request-schema 值校验 funnel — async 不可信边界落地）

**触发**：§5 演进路径「command-entry 值校验 funnel（#1588，Blocked-by ④）」落地（触发条件 = ④ #1667 已落）。按 ai-robust.md「ADR amendment 必查」逐行重评 §4 + 重写矛盾原文（§D8 reason #3 加 sync-scope 澄清、§5 标 delivered、Amendment 2026-06-04「为何不 silent defer」段就地修订——见各处 §Amendment 2026-06-08 回填）。

**D11：async `DispatchAsync` 在不可信边界跑 byte-validator；sync `Dispatch` 不变（D4/§D8 保持）**。生成 `DispatchAsync` 在 topic-guard 之后、`json.Unmarshal` 之前，对 `entry.Payload()`（已是入站 wire JSON bytes）调 `runtime/schemavalidate.Validator.Validate`，强制 request schema 值约束（`minLength`/`maxLength`/`required`/`additionalProperties`/`pattern`/...全 schema 语义）。这是 HTTP request-body 校验的不可信边界对位物——**与 sync 路径正交**：sync `Dispatch` 的输入是 in-process 第一方 typed `*Request`，仍刻意不校验（§D8 可信边界 + D4 零序列化）。**honor D4**：async 输入本就是 JSON bytes，对它 `Validate` 零 marshal round-trip（§D8 reason #3「round-trip 违背 D4」只约束「校验 sync typed 输入」，对 async bytes 不成立）。

**D12：机制 = 复用既有 byte-validator + 抽中性包，非全新 codegen IR**。

1. **包抽取**：`runtime/http/schemavalidate` 的 transport-neutral 核心（`Validator`/`NewValidator`/`Validate`/safe-message helper）迁到 `runtime/schemavalidate`；HTTP-专用 `WriteValidationError` 删除（generated HTTP handler 改调既有 `httputil.WriteError`——`Validate` 恒返回 `*errcode.Error(ErrValidationFailed)`，`WriteError` 经 `errors.As` 渲染为 400，原 `WriteValidationError` 的 non-errcode→400 fallback 是 dead code）。HTTP handler 与 command 生成包共用同一中性 validator，命令包不再 import `runtime/http/*`。
2. **codegen embed**：`contractgen.embedRequestSchema`（HTTP+command 共享 helper）read→`bundleSchemaRefs`→`json.Compact`→`NewValidator` 编译期 fail-fast，存 `spec.RequestSchemaJSON`。command **无条件** embed（D6 保证 request schemaRef 恒存在；不套 HTTP 的 `HasBody` gate），`command.tmpl` 恒 emit 包级 `requestSchemaJSON` + `requestValidator` + `DispatchAsync` 的 `Validate` 调用——**无「command 无校验」逃逸路径**。
3. **settle 分类**：schema 违例确定性、不可重试 → 生成码以 `kout.NewPermanentError` wrap → relay `handleFailedEntry` → `MarkDead`（与既有 decode-failure / topic-mismatch 同 permanent 类，#1673 F3 机制复用，零新增）。回答 issue「值校验失败分类随 #1588」。

**拒绝**：typed-struct 级约束 codegen IR（option a）——覆盖不全（仅 len/required，无 nested/pattern/enum/format）+ 须另起全新 archtest+golden 机制；byte-validator 复用（option b）覆盖全 schema 且零新机制，胜出。**拒绝**：在 sync `Dispatch` 内 marshal→validate（§D8 已记否决，违 D4）。

**§4 评级矩阵逐行重评（无格降级）**：值校验落生成 `DispatchAsync` 体内，与 dispatch/register funnel **正交**，**不新增 enforcement 机制**（骑既有 funnel）：

- `COMMAND-GEN-FUNNEL-SOLE-EMITTER-01`：✅ **不变**（上游 Hard / 下游 Medium）。golden 字节锁随 `DispatchAsync` 的 `Validate` 调用 + 包级 `requestSchemaJSON`/`requestValidator` emit 更新；validate 调用经 golden regen-diff 锁定，模板单源外不可手写、不可静默删——这是值校验「不可绕过」的上游 Hard 来源（无须新 archtest）。
- `COMMAND-DISPATCH-REGISTER-CALLER-01`：✅ **不变**（上游 Medium / 下游 Hard）。
- `COMMAND-ASYNC-DISPATCH-CALLER-01`：✅ **不变**（上游 Medium / 下游 Hard）。relay 仍只调生成 `DispatchAsync`（下游 Hard），故「值校验必经过」由「async 命令必过生成 DispatchAsync」继承。

**无 ✅→⚠️/❌ 降格，无补偿措施**：D11/D12 未改 `Entry` wire envelope、未新增 errcode（复用 `ErrValidationFailed`）/ Kind / schema / migration / interface 签名——**未触发 contract-fanout 5 载体**（包抽取是内部 refactor，generated 重生由 `generatedverify` 守）。anti-vacuity：render test 锁 `Validate` 调用 emit + E2E（`examples/iotdevice`）锁运行期 valid-过 / invalid-拦-permanent / relay-dead-letter。

enforcement 索引（§7）**无新增**——值校验由 §4 既有三 funnel 覆盖；`runtime/schemavalidate` 为运行时 validator 载体（非 enforcement 机制）。

**范围（显式，非 silent defer）**：HTTP→command 不在本 PR（仓内无 HTTP→command wiring；真落地时复用 HTTP handler 既有 validator，结构上已覆盖）。真实 binary async producer 接线 = #1698（正交）。

## Amendment 2026-06-10 — ② reconcile→command producer 落地（#1757）

**触发**：#1757 落地 §5 演进路径 **archetype ②（reconcile → command）**——iotdevice 证书续期。按 ai-robust.md「ADR amendment 必查」逐行重评 §4 + 重写矛盾原文（§5 第 5 子项原把 ② 记为「#1757 同步 HTTP→command」，与 #1669 ⑤ 设计选定的 ② = reconcile→command 不符，已就地修正 + 加修订注）。

**落地形态**：`examples/iotdevice/cells/devicecell/slices/devicecertrenewal.Reconciler` 实现 `reconcile.Reconciler`（producer 落在专属 `devicecertrenewal` slice，声明 `command.devicecommand.enqueue.v1` `role: invoke` + waiver——formal contract-usage 所有权，#1812 F3；证书状态（`cert_epoch` / `cert_expires_at`）以列形式持久化在 `devices` 行，由 deviceregister 播种），由 cell 的第二个 `reconcile.Loop`（`devicecert.renewal`，`TickerTrigger` off `c.clk`，`buildCertRenewalSweeper`，镜像既有 `buildCommandSweeper`）驱动。每 tick 扫 `devices` 表的近过期证书 → 对每台设备经 `command.EmitAsync(ctx, clk, bootstrapEmitter, cmdenqueue.DispatchID, deviceID, commandID, {commandType: "rotate-cert", …})` emit 一条 `command.devicecommand.enqueue.v1`，复用 #1698 已激活的 bootstrap emitter/txRunner + 既有 `WithCommandDispatch` dispatcher-map（**复用同一 enqueue dispatch，无新 map 项、无新契约**）。

**幂等 forcing function（#1812 F1，被 §Amendment 2026-06-12(#1820) 取代）**：原设计用 `devices.renewal_requested_epoch` / `renewal_requested_at` 列状态抑制重复 emit（时间窗 25h，与 relay Claimer 24h done-TTL 协调）。**该方案已被 §Amendment 2026-06-12(#1820) 全面替换为 command-queue active-uniqueness**——producer 现在无状态（侧表列已移除），正确性由 `kernel/command` 队列的 partial unique index（D1，ADR-1822）结构性持有。relay Claimer 退为纯 dispatch-time 优化，不再是 single-emit 保证的来源。详见 §Amendment 2026-06-12(#1820)。

**安全声明重评（ai-robust ADR amendment 必查）**：原落地的「时间窗抑制」威胁模型（离线设备有界积压 ≤ 28 条 / 并发扫描绕过时间窗 / retryInterval ≥ TTL 协调 Soft 约束）均已被 §Amendment 2026-06-12(#1820) 替换——详见该 amendment 的完整威胁矩阵。本节不重复已被取代的威胁分析。

**§4 评级矩阵逐行重评（无降格，零新增 enforcement——骑既有 funnel）**：

- `COMMAND-GEN-FUNNEL-SOLE-EMITTER-01`：✅ **不变**（上游 Hard / 下游 Medium）。#1757 未新增任何生成 trio——rotate-cert 复用既有 `enqueue` 生成码，零模板/golden 改动。
- `COMMAND-DISPATCH-REGISTER-CALLER-01`：✅ **不变**（上游 Medium / 下游 Hard）。无新 `RegisterHandler`/`LookupHandler` 调用方（`cmdenqueue.Register` 既有）。
- `COMMAND-ASYNC-DISPATCH-CALLER-01`：✅ **不变**（上游 Medium / 下游 Hard）。复用同一 `enqueue` 的 `WithCommandDispatch` dispatcher-map 项（无第 2 项）。**key↔value 同源校验（#1673 F1）仍 vacuous**——#1757 不引入第 2 个 codegen command，故「第 2 个 codegen command 自动 bites」的非空化条件维持未触发（诚实记录，非本 PR 盲区）。
- `COMMAND-ASYNC-EMIT-FUNNEL-01`：✅ **不变**（上游 **Medium** / 下游 **Hard**）。新增**第 2 个** `command.EmitAsync` 生产调用方（`devicecertrenewal.Reconciler`，archetype ②，落在专属 slice 并声明 `role: invoke` + waiver——#1812 F3 把 producer 的 contract-usage 所有权从「无 slice 归属」升到 slice metadata 声明 + `gocell validate` 校验；archetype ① devicebootstrap 为第 1 个）。上游单包守卫「`command.*` topic 的 raw `kout.NewEntry`/`Emit` 仅限 `runtime/command` 包」对新 producer **by-construction 合规**（走 sanctioned `EmitAsync` 公门）→ **无 archtest 改动**，坐实 issue「零新增 funnel」；won't-do ceiling 不变。下游 `EmitAsync` subject/commandID required 位置参（Hard）继续保证「emit 异步命令必带身份槽」编译期不可绕过。

**§4 funnel 矩阵再验证（#1819 cert-state 持久化变更）**：#1819 把 archetype ② producer 的**输入来源**从 ephemeral 内存 store 改为持久化 `devices` 行扫描（`devices` 表 repo），但 dispatch 路径**完全不变**——producer 仍经 sanctioned `command.EmitAsync` 出口、relay 仍经既有 `WithCommandDispatch` dispatcher-map 中的生成 `enqueue.DispatchAsync`，整条 #1698/#1699 async-dispatch funnel 原样复用。§4 四行 invariant 评级均无变化，**无 ✅→⚠️/❌ 降格**。

**无 ✅→⚠️/❌ 降格，无补偿措施**：dispatch 侧未改 sealed `Entry` wire envelope、未新增 command errcode/Kind/contract/schema——**命令侧未触发 contract-fanout 5 载体**（rotate-cert 内嵌既有 `enqueue` 的 opaque `payload`，`commandType` 字段既存）。migration↔`schema_guard` 注册一致性由既有 `pg_schema_guard_invariants` archtest 独立守卫（Hard），非本 ADR 新增 enforcement。enforcement 索引（§7）**无新增**。

**范围（显式，非 silent defer）**：证书状态（`cert_epoch` / `cert_expires_at`）以列形式持久化在 `devices` 行，由 mem 和 PG device repo 读写（#1819）——ephemeral `internal/devicecert/store.go` 已删除。**边界（#1827 F2 澄清）**：迁移 056 **之前**已存在的 `devices` 行的 `cert_expires_at` 为 NULL（056 只 ADD COLUMN、无 backfill），被续期扫描的 `cert_expires_at IS NOT NULL` 谓词永久跳过，须重新注册或重签发证书后才进入续期状态机——此为 demo 取舍（无 backfill 数据源），非缺陷。mem 模式（无 DSN）下 cert 状态随进程终止丢失。设备侧轮换完成（epoch advance / 应用新证书）属设备 dequeue 后固件行为，在本 producer 范围外。archetype ③（saga step→command）仍为独立 issue。

## Amendment 2026-06-11 — #1827 codex review fix（F1 emit/mark 原子性 + F2/F3 文档准确性）

**触发**：PR #1827（#1819 durable cert-state 落地）codex review，3 条 finding。按 ai-robust.md「ADR amendment 必查」逐行重评 §4 + 就地重写矛盾原文（上文 §Amendment 2026-06-10 中「emit 已提交但 mark 丢失」历史原文已被 §Amendment 2026-06-12(#1820) 全面取代，因 renewal_requested_epoch 列本身随 producer 无状态化已移除）。

**F1（P1·架构/原子性）—— emit 与 mark 落同一 tx**：原落地 `enqueueRenewal` 把 `command.EmitAsync` 包进 `RunInTx`，但 `MarkCertRenewalRequested` 在 tx **外**用原始 ctx 二次提交，留有 crash 窗口（emit 已提交、mark 未提交）。**修正**：mark 移入同一 `RunInTx` closure 并传 `txCtx`——durable PG 模式下 outbox writer 与 `devices` UPDATE 同走 `pgexec.PGExecutor` 的 ambient `pgx.Tx`，原子提交/回滚。**注**：`renewal_requested_epoch` / `renewal_requested_at` 列已随 §Amendment 2026-06-12(#1820) producer 无状态化移除——本 F1 在新设计下等价为「emit 与任何可选 producer-side mark 必须同 tx」；若 producer 无额外写入，则只有 emit tx，原子性自然满足。

**F2（P1·产品/运维）—— 旧行 NULL expiry 边界**：迁移 056 之前的 `devices` 行 `cert_expires_at` 为 NULL，被续期扫描永久跳过；`devices` 表无签发时间列 → 无 backfill 数据源，须重注册/重签发证书后才进入续期状态机——doc-accuracy 修正，非迁移 backfill 或一次性修复 job。

**F3（P2·运维/DX）—— README durable env 缺失**：`examples/iotdevice/README.md` Docker Mode 块补 `GOCELL_IOTDEVICE_CURSOR_KEY`（≥32 bytes，`run.go` `buildCursorCodec` durable fail-fast）+ `GOCELL_IOTDEVICE_DURABLE_SINGLE_POD=true`（`commandRelayClaimer` durable fail-fast，单 pod demo 边界显式 ack），否则按 README 操作 durable 启动即失败。

**§4 评级矩阵逐行重评（无降格，零新增 enforcement）**：F1 是**业务 bug 修复**（命令 producer 的 tx 边界），非 dispatch/register funnel 改动——§4 四行 invariant 评级与符号**全不变**，未改 sealed `Entry`、未新增 errcode/contract/schema/migration、未触发 contract-fanout 5 载体。F2/F3 为纯文档，无 enforcement 面。**无 ✅→⚠️/❌ 降格，无补偿措施**。

## Amendment 2026-06-12(#1820) — cert-renewal 正确性移交 command-queue active-uniqueness（#1820）

**触发**：#1820（cert-renewal producer 重设计）。按 ai-robust.md「ADR amendment 必查」逐行重评 §4 + 重写上文 §Amendment 2026-06-10 §Amendment 2026-06-11(#1820)（旧时间窗方案）矛盾原文（已加本 amendment 指针，旧内容已就地改写）。

**根本问题（#1808 簇 C1 根因分析）**：旧设计的 `devices.renewal_requested_at` 时间窗是 observe-then-decide 模式——producer 用侧表状态模拟「命令是否在飞行中」。根本缺陷：
1. **离线设备积压无界**：每过 retryInterval，即使命令仍在队列 Pending，producer 重发一条新命令进 outbox→relay→队列，活跃命令单调积累（near-expiry 窗口 30d，retryInterval 25h → 最多积压 ≈28 条）。
2. **并发竞态**：多副本或多 worker 并发扫描时，CAS on cert_epoch 不足以防止同 tick 内多条 emit（与 cert-manager issue #4642 同构）。
3. **retryInterval ≥ TTL 协调是 doc-only（Soft）约束**：无机器守卫，参数选错即破坏正确性。

**新设计（command-queue active-uniqueness，见 ADR-1822 `docs/architecture/202606121000-1822-adr-command-queue-active-uniqueness.md`）**：

- **D_AU1（队列拥有正确性）**：`kernel/command` 设备命令队列引入**非终态内活跃唯一性**（state-aware active-uniqueness）：`EnqueueOptions.IdempotencyKey`（exported `string` 字段）在命令处于非终态（Pending/Sent/Delivered）时唯一，命令到达终态（Succeeded/Failed/Expired/Canceled）时释放。PG = partial unique index on `commands` table，expression column `(metadata->>'_idempotency_key')`，`WHERE metadata->>'_idempotency_key' IS NOT NULL AND status IN (1,2,3)`（DDL 见 `adapters/postgres/migrations/061_commands_idempotency_active_index.sql`，in-transaction 非 CONCURRENTLY——示例规模可接受，生产 fleet 须改 CONCURRENTLY 双步方案）；in-mem 从 status 派生。两个实现由 `commandtest` 跨存储 conformance 套件强制一致。
- **D_AU2（不安全组合不可表达——EmitAsync 路径）**：`runtime/command.WithActiveUniqueness(deadline time.Time)` 是 `EmitAsync` 的 `EmitOption`，定义在 `runtime/command/command_idempotency.go`。`EmitAsync` 在 `hasActiveUniqueness && deadline.IsZero()` 时 fail-fast，使「有 active-uniqueness key 但 deadline 为零」在受控 emit 路径上不可通过。注：`kernel/command.EnqueueOptions.IdempotencyKey` 是 exported `string` 字段，raw kernel-API caller 可直接构造，但当前生产代码 0 个 raw caller；EmitAsync 是 sanctioned path。
- **D_AU3（producer 无状态）**：`devices.renewal_requested_at` 和 `renewal_requested_epoch` 列移除（schema guard golden Hard 守）。每 tick 对近过期证书无条件调用 `command.Enqueue(WithActiveUniqueness(deadline))`；命令已在队列且非终态时 enqueue 被幂等 noop（`ON CONFLICT DO NOTHING` / in-mem 集合 rejected）。
- **D_AU4（relay Claimer 降级）**：relay Claimer 从「single-emit 第一防线」降为「dispatch-time 去重优化」。relay 在 Claimer Acquired 后把 active-uniqueness key+deadline 注入 ctx，供下游 enqueue 使用。
- **D_AU5（批量扫描上限移除）**：批量扫描上限（`LIMIT certRenewalScanBatchSize` + 满批 `RequeueAfter` drain 循环）与无状态 producer **不兼容，已移除**。无状态 producer 不写任何抑制标记；保留 LIMIT + RequeueAfter 后，expiry-ordered 扫描每次 requeue 从头扫，前 N 条设备被反复 re-emit（`ON CONFLICT DO NOTHING` 吸收 emit，但扫描本身无限循环），tail 设备当前 tick 内永远排不到（正向饥饿）。新形态：每 tick 全量扫描近过期证书，无 LIMIT，逐台 enqueue，返回 `Result{}`（交还 TickerTrigger 按 interval 下次 tick）。正确性由 D1 队列 active-uniqueness 保证（重复 emit 幂等吸收），不依赖 producer 侧任何 drain 边界。Sweeper（已有）周期 expire 超 OverallDeadline 的 Pending 命令 → 终态 → key 释放 → 下次 tick 正常重 enqueue。

**威胁矩阵重评（ai-robust ADR amendment 必查）**：

| 旧威胁（时间窗方案） | 旧处置 | 新处置（queue active-uniqueness） |
|-------------------|---------|---------------------------------|
| 离线设备积压 N 条活跃命令 | 有界（≤28 条），但非零 | **消除**：PG partial index 保证至多 1 条非终态命令 |
| 命令永久 Pending（Sweeper 未触发） | 无路径 | **消除**：`WithActiveUniqueness(deadline)` 必须带非零 OverallDeadline → Sweeper expire → 终态 → key 释放 |
| 并发扫描绕过时间窗（多副本 / 多 worker） | 部分（CAS on cert_epoch + 时间窗），有竞态 | **消除**：PG partial index 唯一性由 PG 引擎串行化，并发 `ON CONFLICT DO NOTHING` 安全 |
| retryInterval ≥ TTL 协调是 Soft doc 约束 | doc-only，无机器守 | **移除该约束**：Claimer 降为优化，queue uniqueness 不依赖 TTL |
| 新威胁：active-uniqueness key 被 deadline-less 命令永久持有（经 EmitAsync） | （旧设计无此威胁） | **Hard at EmitAsync**（`EmitAsync` coupling guard：`hasActiveUniqueness && deadline.IsZero()` → fail-fast；受控 emit 路径不可通过） |
| 新威胁：active-uniqueness key 被 deadline-less 命令永久持有（经 raw kernel Enqueue） | （旧设计无此威胁） | **Medium**（`EnqueueOptions.IdempotencyKey` 是 exported 字段，raw caller 可绕过 coupling guard；0 生产 raw caller；`COMMAND-ASYNC-EMIT-FUNNEL-01` archtest 兜底） |

**新失效模式（诚实记录）**：`commandtest` conformance 套件是 Medium+ 保证（跨存储一致性 + WHERE 谓词正确性可被 conformance 测试抓住，但 Go 类型系统无法在接口层直接表达「两实现对 status 集必须一致」或「DDL WHERE 谓词字节精确」）。`schema_guard.go verifyIndexes` 锁定 index 名/唯一性/列（`"(expr)"`），但不读取 `pg_index.indpred`——WHERE 谓词由 conformance 而非 schema_guard 守卫（Medium+，非 Hard）。

**dispatch funnel 不变**：本 amendment 在命令**队列写入层**增加幂等保障，不改变 §5 演进路径中 producer 经 `command.EmitAsync` → outbox → relay → `DispatchAsync` → `kernel/command.Enqueue` 的路径——EmitAsync 仍经 sanctioned 出口，relay 仍用既有 dispatcher-map，`DispatchAsync` 仍是生成码唯一来源。

**§4 评级矩阵逐行重评（无降格，零新增 enforcement——dispatch funnel 正交）**：

- `COMMAND-GEN-FUNNEL-SOLE-EMITTER-01`：✅ **不变**（上游 Hard / 下游 Medium）。本 amendment 改的是队列层（`kernel/command`），不触及 `command.tmpl` 派生的 typed Handler/Register/Dispatch/DispatchAsync。
- `COMMAND-DISPATCH-REGISTER-CALLER-01`：✅ **不变**（上游 Medium / 下游 Hard）。`RegisterHandler`/`LookupHandler` caller-allowlist 不动。
- `COMMAND-ASYNC-DISPATCH-CALLER-01`：✅ **不变**（上游 Medium / 下游 Hard）。relay 经 `DispatchAsync` 到队列 Enqueue 的路径不变；active-uniqueness 是 Enqueue 内部语义，外部 dispatch funnel 感知不到。
- `COMMAND-ASYNC-EMIT-FUNNEL-01`：✅ **不变**（上游 Medium / 下游 Hard）。`EmitAsync` subject/commandID 位置参不变；producer 无状态化只移除了 Enqueue 之后的 producer-side mark，dispatch funnel 不变。

**无 ✅→⚠️/❌ 降格，无补偿措施**：本 amendment 未改 sealed `Entry` wire envelope、未新增 errcode/Kind/contract/schema——**未触发 contract-fanout 5 载体**。`devices` 列移除（DROP COLUMN migration）由 schema guard golden Hard 守卫。`kernel/command` 层新增 partial index + `WithActiveUniqueness` EmitAsync coupling guard 属**队列内部持久化语义**，与 command-bus dispatch funnel 正交。

**enforcement 分档（ai-robust「涉及 enforcement 必给评级」）**：
- PG partial unique index（table `commands`，expression `(metadata->>'_idempotency_key')`，`WHERE ... AND status IN (1,2,3)`）—— index 存在性 + 唯一性 + 列：**Medium+**（`schema_guard.go verifyIndexes` 锁 name/unique/`"(expr)"`；WHERE 谓词不在 `indpred` 覆盖范围）；PG 引擎在 enqueue 时强制该谓词：**Hard**（`ON CONFLICT DO NOTHING` 不可绕过）；WHERE 谓词正确性由 conformance 守卫：**Medium+（conformance-enforced）**（`commandtest` `ActiveKeyBlocksAcrossNonTerminal` 覆盖 Pending/Sent/Delivered；谓词遗漏任意非终态 status，PG conformance run 红）。
- in-mem active-uniqueness 与 PG 一致（同 status 集）—— **Medium+**（`commandtest` 跨存储 conformance 套件；两实现偏差 CI 红；Go 类型无法在接口层表达「status 集必须一致」，Medium+ 是正确档位）。
- `WithActiveUniqueness` coupling guard（不安全组合在 EmitAsync 路径不可表达）—— **Hard at EmitAsync**（`runtime/command/command_idempotency.go`：`hasActiveUniqueness && deadline.IsZero()` → fail-fast；`WithActiveUniqueness(deadline time.Time)` 是 `EmitOption`，签名要求非零 `time.Time`）；raw `kernel/command.EnqueueOptions.IdempotencyKey` 是 exported 字段——raw kernel-API 直接使用是 **Medium** residual（0 生产 caller；`COMMAND-ASYNC-EMIT-FUNNEL-01` 兜底）。
- `devices` 无 `renewal_requested_at` / `renewal_requested_epoch` 列—— **Hard**（`schema_guard.go` expectedColumns golden；列存在即红）。
- relay identity 注入（active-uniqueness key+deadline 进 ctx）+ E2E 验证—— **Medium**（`TestCertRenewalActiveUniquenessE2E`：单条活跃、离线积压不增长；PG partial index 是最终 Hard 兜底）。

enforcement 索引（§7）**无新增 dispatch-funnel invariant**——active-uniqueness 属 kernel/command queue 层，见 ADR-1822 §8 enforcement 索引。**无 ✅→⚠️/❌ 降格，无补偿措施**。

**范围（显式，非 silent defer）**：terminal-feedback 释放（设备回执 / DLX 驱动 epoch advance）仍在 producer 范围外，登记为独立 follow-up issue（per-key reconcile + terminal-feedback 释放，ADR-1822 §7.1 注明）。批量扫描上限 + RequeueAfter 续扫已移除（D_AU5）。
