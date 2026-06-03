# ADR: MQTT transport 纳入 contract/codegen transports 多值真值源 (#1389)

- Status: Accepted
- Date: 2026-06-04
- Tracks: gh issue #1389（Parent EPIC #1138 MQTT adapter；Source = PR #1364 review F5 P1·Cx4）
- Builds on: `202605262101-047-mqtt-adapter`（plan §7:256 明确把 codegen 多值 transport defer 到 V11 后续）；`ai-robust.md` §"Hard 范本目录"（string-typed concept funnel / codegen funnel + golden / sealed construction）
- Implemented by: PR resolving #1389（worktree `161-mqtt-transports-truth-source`）

## Context

### 问题：MQTT transport 身份脱离契约真值源

PR #1364 把 MQTT adapter 接进 `examples/iotdevice` 时，用**单通道 DI swap**（非计划原设想的双通道镜像）让 verifier 观测 MQTT 投递。结果 MQTT 的 transport 身份只活在两处手写字面量：

1. 手写 `mqttTopicPublisher`（`adapters/mqtt/mqtt.go`）——运行时投递通道。
2. smoke 的 `ContractTransport: "mqtt"` 字符串字面量——与契约真值无绑定。

`event.device-registered.v1` 的 `contract.yaml` 当时**无** `transports:` 字段（parser 隐式默认 amqp），所以「该契约能不能走 MQTT」这件事在契约层是不可见的——MQTT 是 demo-only 观测通道，靠 DI 旁路接出，契约 / codegen / governance 三层都不知情。PR #1364 已在 smoke 加注释澄清 `ContractTransport` 非契约真值（避免误读），但真值源整合作为 V11 后续工作明确 defer（plan §7）。

### 既有真值源形态（#1389 之前）

GoCell 的 transport 在 #1389 之前是「每 kind 一个硬编码 wire 协议」的隐式约定，散落在三处字面量：

- `kernel/metadata/parser`：无 `transports` 概念，contractgen 模板按 kind 硬编码 `Transport: "amqp"` / `"http"`。
- `tools/codegen/cellgen`：`SubscriptionGenSpec.Transport` 字段硬编码 `"amqp"`（实测**死字段**——cell.tmpl 不引用它，订阅 transport 来自契约侧生成的 `NewSubscription`）。
- 各 `generated/.../spec_gen.go`：`Transport: "amqp"` 等字面量（codegen 产物）。

没有任何载体表达「一个契约可以绑定一组 sanctioned transport」。要让 MQTT 成为 `device-registered` 契约的一等 transport，必须先把 transport 提升为**契约声明层的多值真值源**，再让 codegen / governance / 运行时 / smoke 全部从该真值源派生。

## Decision

### D1 — `cellvocab.Transport` typed 闭集 + `AllTransports()` 单源

`kernel/cellvocab` 新增 `type Transport string` 与 5 个 const（`amqp` / `mqtt` / `internal` / `http` / `grpc`），并以 `var allTransports` + `AllTransports()` 作为**全仓 transport 闭集的唯一权威源**。对标 k8s `core/v1.Protocol +enum`（闭集，平台 fail-fast 拒未知值），区别于 AsyncAPI 的开放 `protocol` string。`ParseTransport` round-trips 每个成员（cellvocab round-trip 测试守）。

派生消费方：
- `metadata.TransportEnum`（`AllTransports()` 的 string 投影）+ `metadata.IsKnownTransport` —— governance FMT-39 与运行时共读。
- `contract.schema.json` 的 `transports` 扁平 enum —— 由 `TestSchemaConstantsMatchSchemaLiterals#transportEnum` **字节锁**到 `AllTransports()`（schema literal 漂移即 CI 红）。
- parser per-kind 默认派生（D3）。

### D2 — `ContractMeta.Transports []string`（契约声明层多值字段）

`contract.yaml` 支持 `transports: [amqp, mqtt]`，解析进 `ContractMeta.Transports`。这是契约「能绑定哪组 sanctioned transport」的声明真值源。`device-registered` 是当前唯一显式多值契约（`[amqp, mqtt]`）。

### D3 — 省略即按 kind 默认派生（byte-identical 兼容）

`parser.parseContract` 在 `Transports == nil` 时按 kind 默认（`defaultTransportsForKind`）：event/command→`[amqp]`、http/webhook→`[http]`、grpc→`[grpc]`、projection/saga→`[internal]`、未知 kind→`nil`（交 FMT-39 兜空）。默认值**精确复现** #1389 之前每 kind 硬编码的 wire 协议，所以全部既有契约的生成 `ContractSpec.Transport` primary 保持字节不变——只有显式声明多值的契约（device-registered）产生新输出。对标 k8s `SetDefaults_Service`（`Protocol == "" → ProtocolTCP`）。

### D4 — governance FMT-39（声明层成员校验 + kind-compat 矩阵）

`gocell validate` 新增 FMT-39（`PhaseBase`，CI fail-closed），对每个契约的 `transports` 做四项正交校验：① 非空；② 每元素 ∈ `TransportEnum`；③ 无重复；④ kind↔transport 兼容矩阵（exact-singleton kinds：http/webhook=`{http}`、grpc=`{grpc}`、projection/saga=`{internal}`；subset kinds：event⊆`{amqp,mqtt,internal}`、command⊆`{amqp,internal}`）。矩阵用 `cellvocab.Transport*` const 表达，无裸字面量。**评级 Medium**（governance YAML-metadata validate 层，同 FMT-36/37/38 档位）；transport 闭集本身经 schema enum 字节锁到 cellvocab，是 Hard。

### D5 — contractgen 统一从 `transports[0]` 派生 primary + 多值时 emit set

`ContractGenSpec.Transports` 透传 `ContractMeta.Transports`；模板**统一**渲染 `Transport: {{printf "%q" (index .Transports 0)}}`（`spec.tmpl` 事件 + `handler.tmpl` HTTP），消除「每 kind 一个硬编码 transport 字面量」的旧形态——"generated Transport == transports[0]" 无 kind special-case。对**多值**契约（`len > 1`），`spec.tmpl` 额外 emit 导出 `var Transports = []string{...}`（声明序），供 out-of-band 订阅者引用契约真值源而非手写 transport 字符串。

同时**删 cellgen 死字段** `SubscriptionGenSpec.Transport`（硬编码 "amqp"，cell.tmpl 不引用——订阅 transport 实际来自契约侧生成的 `NewSubscription`，已隐含 `transports[0]`）；删除后全仓 codegen 重生成字节不变，证明该字段确为死代码。

### D6 — `contractspec.ContractSpec.Transport` 文档化为「派生 primary」，运行时**不**做成员校验

运行时 `kernel/contractspec.ContractSpec.Transport` godoc 改写为「PRIMARY transport = codegen 派生的 transports[0]；多值集经生成包的 `Transports` var 暴露」。**刻意不在 `Validate()` 加成员校验**（P1-C 决议）：

- 成员闭集在**契约声明层**已由 FMT-39 + schema enum 强制；生产 `ContractSpec` 只从 codegen（`Transport` = 已校验的 `transports[0]`）或 `contractbuild` funnel 到达运行时——运行时再校验是冗余。
- 运行时成员校验会**破坏 65 个测试夹具**：它们用描述性 transport label（如 `"inmem"` / `"memory"`）做 in-mem 通道标记，不在 sanctioned 闭集内。`contractspec` 保持 dependency-light 运行时值类型。

### D7 — smoke 引用契约真值（去字面量 + 编译期/运行期双绑定）

`device-registered` 契约声明 `transports: [amqp, mqtt]` 后，生成包暴露 `deviceregistered.Transports`。smoke 删 `ContractTransport: "mqtt"` 字面量，改 `ContractTransport: mqttTransportFromContract(t)`——后者遍历 `deviceregistered.Transports` 找 `cellvocab.TransportMQTT`，**不在集合则 `t.Fatalf`**。由此 verifier 的 MQTT 投递通道 Hard-绑定到契约真值：

- **编译期**：若契约去掉多值 transport，生成的 `var Transports` 消失 → smoke 引用 `deviceregistered.Transports` 编译失败。
- **运行期**：若 `Transports` 集合里没有 mqtt → `mqttTransportFromContract` fail。

## P2.5 决议：不新增 `TRANSPORT-SEALED-FUNNEL-01` archtest（已被既有 Hard 封闭）

#1389 计划阶段曾设想一条防御纵深 archtest `TRANSPORT-SEALED-FUNNEL-01`。落地核查后**判定不立项**：transport → `ContractSpec.Transport` 这条 surface 已被既有机制完整封闭，新 archtest 会是冗余 belt-and-suspenders，而非 gap-closer。封闭证据：

| Surface | 既有守卫 | 评级 |
|---------|---------|------|
| transport 闭集 ↔ schema enum | `TestSchemaConstantsMatchSchemaLiterals#transportEnum` 字节锁到 `cellvocab.AllTransports()` | Hard |
| `contract.yaml transports` 校验 | governance **FMT-39**（成员 + 重复 + kind-compat） | Medium |
| codegen → `generated/.../*_gen.go` | regen golden 字节锁 + 模板 `index .Transports 0` 派生 | Hard 上游 |
| **任意**手写 `ContractSpec{Transport:…}` | **`NO-MANUAL-CONTRACTSPEC-LITERAL-01`**（既有）禁 `generated/` + `runtime/internal/contractbuild` 三 funnel 之外的一切 `contractspec.ContractSpec{}` 字面量；`contractbuild` 在 `runtime/internal/` 下编译期不可被业务 import | Hard |
| smoke ↔ 契约 | 编译期绑定 `deviceregistered.Transports`（D7） | Hard |

一个 transport 字符串**无法**在 funnel 之外进入 `ContractSpec`——`NO-MANUAL-CONTRACTSPEC-LITERAL-01` + codegen golden 已把 surface sealed。`TRANSPORT-SEALED-FUNNEL-01` 若实现为「模板必须从 `index .Transports 0` 派生」的 form-lock，与下面的单元 derivation lock 重叠；若实现为「禁手写 transport 字面量」，与 `NO-MANUAL-CONTRACTSPEC-LITERAL-01` 重叠。AI-robust §"Soft 严禁立项" 的对偶面是：**冗余的 Medium archtest 本身是治理债**，不应为凑数立项。

### 替代落地：render_test 单元 derivation lock（关闭真实缺口）

核查中发现一个**真实缺口**：`TestRender_Event_Transports` 原两个子测试都用 amqp 作 primary（`["amqp"]` / `["amqp","mqtt"]`），故都**无法**区分 `index .Transports 0` 派生与硬编码 `Transport: "amqp"`。已强化为**两个子测试都驱动 non-amqp primary（mqtt-first）**：`["mqtt"]`→`Transport:"mqtt"` 且无 `var Transports`；`["mqtt","amqp"]`→`Transport:"mqtt"` 且 `var Transports = []string{"mqtt","amqp"}`（声明序）。

- **derivation lock 的意义**：golden 锁的是 regen 一致性（generated == 模板输出），**不**锁「primary 是否真的 track transports[0]」。一个把模板改回硬编码 `"amqp"` 并重生成 golden 的回归会**通过** golden（自洽），但破坏派生语义。强化后的 render_test 用 mqtt-primary 把该派生语义锁在单元层（device-registered golden 的 amqp-primary 抓不到）。
- **mutation-verified**：把 `spec.tmpl` 的 `Transport` 改回硬编码 `"amqp"`，两个子测试都失败；恢复后绿。

这是 unit-test 强化（非 archtest），是该缺口的正确载体：它测的是模板**行为**（render 输出随 transports[0] 变），比解析 `.tmpl` parse tree 的 form-lock 更直接、更稳。

## AI-robust 评级

| 检查形态 | 评级 | 说明 |
|---------|------|------|
| transport 闭集 ↔ schema enum 字节锁 | **Hard** | `TestSchemaConstantsMatchSchemaLiterals#transportEnum`；schema literal 漂移即 CI 红 |
| codegen primary 派生 = transports[0]（generated 产物） | **Hard 上游** | regen golden 字节锁（review-gated `-update` 天花板，同仓内所有 golden） |
| primary 真的 track transports[0]（非硬编码） | **Medium**（unit derivation lock） | render_test non-amqp-primary + mutation-verified；golden 不覆盖此语义 |
| 手写 transport 字面量进 `ContractSpec` | **Hard** | `NO-MANUAL-CONTRACTSPEC-LITERAL-01`（既有）+ `runtime/internal/contractbuild` 编译期不可达 |
| `transports` 声明层校验（成员/重复/kind-compat） | **Medium** | governance FMT-39，同 FMT-36/37/38 档位 |
| smoke ↔ 契约真值绑定 | **Hard** | 编译期 `deviceregistered.Transports` 引用 + 运行期 `mqttTransportFromContract` fail（D7） |
| cellgen 死字段删除无回归 | **Hard** | regen 字节不变即证明死代码 |

无新增 archtest；本 PR 的 enforcement 是「既有 Hard 复用 + FMT-39 Medium + render_test derivation lock」。

## 威胁矩阵

| # | 威胁 | 缓解 | 残留 |
|---|------|------|------|
| T1 | 模板回归硬编码 transport 字面量，绕过 `transports[0]` 派生 | render_test non-amqp-primary derivation lock（mutation-verified）+ golden regen 一致性 | 无（单元 + golden 双覆盖） |
| T2 | 业务代码手写 `ContractSpec{Transport:"x"}` 自传 transport | `NO-MANUAL-CONTRACTSPEC-LITERAL-01`（既有 Hard）+ contractbuild 在 `runtime/internal/` 不可被业务 import | 无 |
| T3 | `contract.yaml` 声明非法 / kind-不兼容 transport | FMT-39（Medium，CI fail-closed）+ schema enum（Hard 字节锁） | T3' |
| T3' | `codegen: false` / in-mem fixture 契约不过 contractgen Hard 主门控 | FMT-39 governance Medium 兜底（与 saga contract 校验同形态） | governance Medium 是该子类唯一守卫（无 codegen funnel 可挂）；可接受 |
| T4 | smoke 误用契约不支持的 transport | `mqttTransportFromContract` 运行期 fail + 契约去多值时编译期 fail（D7） | 无 |
| T5 | 运行时 `ContractSpec.Transport` 携带闭集外值 | 声明层 FMT-39 + schema enum 已拦；生产 ContractSpec 只从 codegen/contractbuild 到达 | 测试夹具用 `"inmem"`/`"memory"` 描述性 label 是**刻意豁免**（D6），非威胁——它们不出生产 wire 路径 |

## 演进 / 后续工作（backlog）

**per-binding 运行时 transport 选路**：当前 #1389 让 transport 成为契约声明层的**多值真值源**，但运行时一个契约仍只按 primary（`transports[0]`）单路绑定。「一个生产 cell 把某契约**路由到** mqtt 而非 primary amqp」的 per-binding 运行时选路**未**落地（smoke 注释亦点明）——它需要订阅/发布侧按 binding 选 transport 的运行时机制（broker 多路 + cell 声明哪条 binding 用哪个 transport）。该工作 defer，backlog **gh #1548** 跟踪（smoke 注释亦点名该 issue 号）。本 ADR 落地后，`device-registered` 的 `[amqp, mqtt]` 多值已被契约 / codegen / governance 三层识别，primary 仍是 amqp，mqtt 是 sanctioned 但运行时尚未自动选路的 alternate。

## 参考

- 对标：k8s `core/v1.Protocol +enum`（闭集 transport）+ `SetDefaults_Service`（per-kind 默认）；asyncapi/spec `channel.servers`（transport 集合）。
- 既有守卫：`tools/archtest/no_manual_contractspec_literal_test.go`（`NO-MANUAL-CONTRACTSPEC-LITERAL-01`）；`kernel/metadata/schemas/schema_const_consistency_test.go`（transportEnum 字节锁）。
- 契约扇出闭环：`.claude/rules/gocell/contract-fanout.md`（contract.yaml payload/endpoint 变更触发 implementation matrix）。
- AI-robust 章程：`.claude/rules/gocell/ai-robust.md`（Hard 范本目录 / Soft 严禁立项 / funnel 双向锁评级）。
