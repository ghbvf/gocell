# MDM 示例 Cell — Epic 规划（探索 + 任务/PR 编排）

> 状态：Draft（探索产出，待评审定 Phase 边界）
> 目标：新建 `externalcells/mdm`（外部 module 示例根，见 §0′），最大化复用 GoCell 已有能力，端到端串起 MDM 本质流水线，
> 为后续 MDM 产品打基础；同时暴露并分类框架缺口，严格区分「框架该补」vs「MDM 该自建」。
> 关联：#1895（device-cert framework pivot）、#1939（framework-owned contract）、
> `examples/iotdevice`（最接近的现有 L4 设备示例）、`examples/orderfulfillment`（saga+projection 参考）。

---

## 0′. 开发模式修订（2026-06-18）：作为外部 cell（Operator-SDK）开发

> 本修订覆盖下文 §2 的「仓内 `examples/` + go.work member + `cmd/corebundle`」假设——新落点是 `externalcells/mdm`（外部 module 根，无 dash，区别于仓内 `examples/`）。
> MDM 示例改为**外部 cell（Operator-SDK 模式，ADR `202605281200`）**开发——dogfood 外部 cell 能力（#1081，10/12 milestone 已落地）。

**与仓内 example 的本质区别**：自有 module + manifest 模式 + 自建 composition root（不用 `cmd/corebundle`）+ 自有 `generated/` + 独立迁移命名空间。目录布局：

```
externalcells/mdm/                   ← 自有 module 根（不入根 go.work；#1722 发布 tag 后可抽离独立仓）
├── go.mod                           module github.com/ghbvf/gocell-mdm；require gocell satellite @vX.Y.Z
├── .gocell/manifest.yaml            modules:[{path:.,excludes:[generated/**,vendor/**]}] → Locator 自动 manifest 模式
├── cells/<...>/slices/.../slice.yaml  belongsToCell 必填（manifest 模式无路径自动派生）
├── contracts/ + generated/          MDM 自有契约 + 自有 codegen 输出（M2 module path 注入）
├── migrations/                      NNN_desc.sql（goose 原生），命名空间 "mdm" → schema_migrations_mdm（M8）
├── cmd/mdmd/main.go                 自建 composition root：composition.New(ids...).With(accesscore.Module(),...,<mdm>).WithMigrations("mdm",fs).Build()（M4）
├── assembly.yaml                    module: 字段组合 平台 cell（gocell module）+ MDM cell（本 module）（M5）
├── archtest_test.go                 archtest.RunStandardCellRules(t,cfg{ProductionMainPkgs:["./cmd/mdmd"],ExtraRules:[…]})（M3 子集）
└── go.work（dev）/ 独立 CI·release  #1722 发布 tag 前经 go.work/replace 消费 gocell
```

**新增依赖/缺口关系**（去重，不另建 issue）：
- **#1090（M9 跨 module 契约 publisher/consumer 注册表，OPEN）**——跨 module serve `_framework` 设备契约 / consume accesscore 事件缺编译期 `DEAD-CONTRACT`/`EMIT-DECL` 治理；M9 落地前 slice.yaml `waivers` 手工对齐。影响 PR2/PR8/PR12。
- **#1092（M11 starter repo + 指南，OPEN）**——本 epic 可充当首个真实外部 cell **starter 范例**并收口 M11（见 PR18）。
- **#1722（发布 satellite tag，未完成）**——真 `go get @vX.Y.Z` 的前提；之前 dev 期用 go.work/replace。

> 依赖 DAG 与 wave 编排不变（PR0 仍首、G3/G4 仍并行；原 G1 已移出框架轨、下沉 MDM=M7，见 §3.2/§7）；仅 PR0 骨架形态、PR4 迁移命名空间、PR2/8/12 跨 module 治理、PR18 验收口径按外部模式调整（已回灌对应 issue body）。

---

## 0. 核心洞察：休眠的证书底座

代码勘察（develop）最关键的发现——**框架的设备证书底座已建成但休眠**：

| 已落地代码（未 wire / 未 serve） | 位置 |
|---|---|
| Signer / Authorizer / RevocationStore + sealed `CertRequest`/`IssuedCert`/`CertScope` | `framework/runtime/certsigning/` |
| 服务端证书续期 Reconciler（泛化 iotdevice seed） | `framework/runtime/certlifecycle/reconciler.go` |
| 内嵌软 CA（零外部依赖，stdlib crypto/x509） | `adapters/softca/`（独立 go.mod） |
| 生产级 device principal issuer（`mintDevicePrincipal`，sealed） | `framework/runtime/auth/deviceprincipal.go` |

而 8 个设备契约 `http.deviceidentity.{enroll,renew,revoke,status}`、`http.devicestate`、
`http.devicecompliance`、`event.deviceidentity.{cert-issued,cert-revoked}` 全是
`ownerCell: _framework` + `lifecycle: draft`，**没有任何 cell 实现 / serve**（ADR-1939 D3：
framework 契约 active serving 未 wire 前 fail-closed 留 draft）。

**结论**：MDM 示例最高杠杆的价值，正是**第一次把这套底座端到端 wire + serve**——这同时满足
「最大化复用已有功能」「为 MDM 产品打基础（serving 是后续一切的 unblock）」「接线倒逼真实缺口浮现」。
示例从第一天用**真 X.509**（`certsigning`+`softca`），**不复制** iotdevice 的 demo 字符串 `rotate-cert`
模型（iotdevice 是另一条腿，US5 迁移另算）。

---

## 1. MDM 本质 → GoCell 原语映射

MDM 的本质 = **设备「期望态 ↔ 实际态」持续收敛的控制面 + 零信任决策叠加**（k8s controller 模型）。
用户给的 13 段流水线天然落到 L1–L4 + saga + reconcile + CQRS 的组合：

| # | 流水线阶段 | GoCell 原语 | 一致性 | 复用 / 新建 |
|---|---|---|---|---|
| 1 | 设备身份识别 | `certsigning`+`softca`+`deviceprincipal`；serve `deviceidentity.enroll/status` | L2（cert=OutboxFact） | **复用休眠底座（首次接线）** |
| 2 | 注册 / 绑定用户 / 绑定租户 | **Saga（L3）**：issueCert→createDevice→bindUser→bindTenant→activate，补偿撤证 | L3 | 复用 saga harness；新 enrollment saga |
| 3 | 设备状态采集 | HTTP/gRPC/MQTT/WebSocket 入站 → device state 事件 | L2（LocalTx+outbox） | 复用全 transport；serve `devicestate` |
| 4 | Inventory Fact 事实沉淀 | **Projection（L3 CQRS）**：状态事件 → 库存读模型，checkpoint exactly-once | L3 | 复用 projection harness |
| 5 | 动态分组 / Scope 计算 | **Projection**：facts → group membership 读模型（规则驱动） | L3 | **MDM 自建（M1）** |
| 6 | 策略匹配 | consume `event.policy.updated`（accesscore）+ group → 匹配策略到设备群 | L3 | 复用 accesscore policy 契约；新 matcher |
| 7 | Desired State 期望态生成 | 匹配结果 → per-device 期望 profile spec（「spec」侧） | L1/L2 | **MDM 自建（M2）** |
| 8 | 命令规划 | reconcile 决策：diff 期望 vs inventory-fact → 规划命令 | L4 | 复用 reconcile harness |
| 9 | 命令下发 | L4 命令队列 + async dispatch + claimer；在线 push(WS/MQTT/gRPC)，离线 push 由 MDM 自建 | L4 | 复用 command/iotdevice 模式；**离线 push = MDM 自建（M7）** |
| 10 | 设备回执 | 设备 ack 命令（L4）→ 更新实际态 | L4 | 复用 |
| 11 | 合规评估 | facts+回执 → posture 判定；serve `devicecompliance` | L3 | **MDM 自建判定（M3）**；serve 框架契约 |
| 12 | Reconcile 纠偏 | **Reconcile（L4）**：期望↔实际无限收敛环，drift 即 re-plan | L4 | 复用 reconcile harness（核心控制环） |
| 13 | 审计与零信任决策 | auditcore（事件驱动审计）+ accesscore PDP（compliance→device_trust→Authorize） | 横切 | 复用 auditcore + accesscore |

**收敛主脊（关键设计纪律）**：阶段 7–12 构成**一个** per-device（或 per-group）reconcile 控制环——
观察实际态（inventory fact）→ 算期望态（policy 匹配）→ diff → 规划命令 → 下发 → 回执 → 再观察，
level-triggered + leader-elect + `LeaseToken.Epoch` fencing。阶段 1–6 喂它，阶段 13 包它。

> **正交边界（避免反模式）**：注册编排用 **saga**（边沿触发、有限步、跑完即终态，对标 Temporal）；
> 期望态收敛用 **reconcile**（水平触发、无限环，对标 controller-runtime）。二者正交，**绝不为同一收敛
> 同时建两套控制环**（见 `.claude/rules/gocell/saga.md` / `reconcile.md` 边界）。

---

## 2. 目标架构

### 2.1 Cell 分解（推荐，可调）

**装配内复用的 corecells（经契约调用，不直接 import）：**
- `accesscore` — auth、device principal→RowScopeDevice、ABAC PDP（device_trust 属性）、`http.policy.*`（已 active）、session
- `auditcore` — 事件驱动审计 append + RowScope 脱敏查询
- `configcore` —（可选）profile blob 下发：`(tenant,key,value,version)` + 热更新 + publish/rollback
- `syscore` — 健康视图

**新建 MDM cells：**

1. **`enrollcell`**（type: control, L3）— 设备身份 + 注册编排
   - slices：`enroll`（serve `deviceidentity.enroll`、跑 saga）、`renew`、`revoke`、`status`（serve `deviceidentity.status`）、`identitybind`（device↔user↔tenant）
   - wire：`certsigning`+`softca`+`deviceprincipal`；saga `Coordinator`（经 `sagaprojectiondeps.Resolve`）
   - publish：`event.deviceidentity.cert-issued/revoked`（**首个 publisher → active-ize**）、`event.mdm.device-enrolled`

2. **`inventorycell`**（type: edge/projection, L2+L3）— 状态采集 + 事实沉淀 + 动态分组
   - slices：`stateingest`（多 transport 入站：HTTP+gRPC+MQTT+WebSocket，L2 写 fact+outbox）、`inventoryproject`（CQRS 读模型）、`groupproject`（动态分组/Scope，M1）
   - 多租户 RLS device 表（PG，`tenant_id` + FORCE RLS）
   - serve：`http.devicestate`（查询侧）

3. **`convergencecell`**（type: control, L3+L4）— 策略匹配 + 期望态 + 命令规划/下发/回执 + reconcile 纠偏（**控制面主脊**）
   - slices：`policymatch`（consume `event.policy.updated`、accesscore policy client）、`desiredstate`（per-device spec，M2）、`commanddispatch`（L4 队列+async+claimer+push）、`receipt`、`reconcile`（L4 收敛环）
   - reuse：accesscore policy 契约（CellTransport client）、command harness、reconcile harness
   - 自有契约：`command.mdm.remotecommand.v1`（lock/wipe/locate/restart，**cell-owned**，M4）

4. **`compliancecell`**（type: projection, L3）— 合规评估 + 零信任 feed
   - slices：`complianceeval`（posture 判定，M3）、`complianceproject`（dashboard 读模型）、`ztfeed`（compliance→accesscore device_trust）
   - serve：`http.devicecompliance`（**首个 serving → active-ize**）

> 共 4 个新 cell + 复用 4 corecells = 8-cell MDM 装配。若评审觉得过大，`convergencecell` 可再拆
> `policycell`（6–7）/`commandcell`（8–10）/`reconcilecell`（11–12），但建议先合后拆。

### 2.2 拓扑（双形态，单源选型）

- **demo**：in-memory event bus、in-proc CellTransport、mem repo、单 binary。`GOCELL_CELL_ADAPTER_MODE=demo`。
- **postgres**：PG RLS device 表、RabbitMQ broker、Redis claimer/locker、saga/projection PG journal+checkpoint、
  split-topology remote CellTransport（演示 cell↔cell 远程）。经 `eventtransport.Resolve` /
  `replaydeps.Resolve` / `sagaprojectiondeps.Resolve` 按 topology fail-closed 选型，**不静默降级**。

---

## 3. 框架缺口分类（核心：框架该补 vs MDM 该自建）

> **框架 vs 产品域判据（防 MDM/厂商能力混入框架）**：进框架 `adapters/` / `runtime/` 必须**同时**满足
> ① **provider-agnostic**——标准协议/通用接口、无厂商特征（EST / MQTT / OIDC / softca ✓；
> APNs=Apple、FCM=Google、WSTEP 等厂商专有 API ✗）；② **非单一产品线专属**——≥2 消费方或设计上可复用。
> 任一不满足 → 归消费方模块（MDM 等）自建。此判据的 durable 落点是 PR-18 的 MDM ADR；其**结构性硬保证**
> 是模块边界：`externalcells/mdm` 自有 module，framework module 不可 import 它 → 厂商 provider 进不了框架。

### 3.1 框架该补 → 拆独立 backlog epic（**不混进示例 PR**）

| ID | 缺口 | 为何是框架职责 | 示例里怎么处理 |
|---|---|---|---|
| **G2** | EST(RFC 7030) 协议前端（`runtime/http/est`） | 标准设备 enrollment 协议前端，provider-agnostic（#1895 spec PR-8b 已规划，`runtime/http/est` 不存在） | 示例先用自定义 enroll HTTP 契约；EST 标准前端走 G2 |
| **G3** | cert 底座 composition wiring helper（cellmodules） | `certsigning`+`softca`+`deviceidentity` 目前每个消费者手接；应有 `cellmodules/certdeps`（类 `eventtransport`/`replaydeps` 的 topology-gated resolver） | 示例先手接，**接线本身暴露 helper 需求**；若手接重复即抽 helper（避免 copy-paste） |
| **G4** | enrollment-credential issuer（非 bootstrap token） | device principal issuer 已落地（#1811），但 enrollment 专用凭据签发仍缺（FR-012：EST 不复用 setup bootstrap token） | 示例用最简 enrollment token，标注「等 G4」 |

> **原 G1（push/wake）经边界审计重分类为 MDM 自建**（见 §3.2 M7 / §7）：APNs/FCM/WebPush 是厂商专有 API
> （非 provider-agnostic），违反上文判据①。push（seam + provider）整体归 MDM 模块（`externalcells/mdm`），
> 框架现在**不承载 push**；在线闭环靠 WS/MQTT/gRPC，离线 push 由 MDM 自建 noop/log 占位。**升格门槛**：仅当
> ≥2 个框架消费者独立需要离线唤醒，才另开 epic 把**通用 seam**升格进框架（厂商 adapter 永不进框架）。
> G2/G3/G4 ID 保持不变（避免 #2302/#2303 连带改号）。

### 3.2 MDM 该自建 → 示例内做，**不塞框架**

| ID | 能力 | 为何是 MDM 职责 |
|---|---|---|
| **M1** | 动态分组 / Scope 引擎 | group membership 计算规则是 MDM 业务语义，非框架通用能力（projection 自建） |
| **M2** | 策略匹配 + Desired State 引擎 | 把 accesscore policy 应用到设备群、生成 per-device 期望 profile，是 MDM 应用逻辑 |
| **M3** | 合规评估引擎 | diskEncryption/antivirus/patch/firewall posture 判定是 MDM 业务（serve 框架 `devicecompliance` 契约，但判定逻辑自写） |
| **M4** | 远程命令契约 `command.mdm.remotecommand.v1`（lock/wipe/locate/restart） | command kind 必须 cell-owned（ADR-1939），是 MDM 专有命令语义 |
| **M5** | PG 表分区 | 验证结论：**非框架职责**（backlog X13「等生产流量阈值」），cell 规模到瓶颈时自决。示例先不分区（RLS dual-layer 够），backlog 记触发条件 |
| **M6** | 应用管理 / OS 更新 / attestation | MDM 高级域能力，超出本示例 foundation 范围 → backlog |
| **M7**（原 G1） | 推送/唤醒通道（`PushNotifier` seam + apns/fcm/webpush provider） | APNs/FCM/WebPush 是**厂商专有 API**（Apple/Google），非框架通用机制。整体归 MDM 模块自建：foundation 期仅 seam + noop/log 占位（在线闭环靠 WS/MQTT/gRPC），真实 provider = post-foundation。框架升格门槛 = ≥2 框架消费者，且厂商 adapter 永不进框架 |

---

## 4. 分阶段实施（依赖序）

> 每阶段都是可独立交付的里程碑，用户可在任意 Phase 后叫停。Phase 0–1 是「foundation」（最高杠杆）。

- **Phase 0 — 地基：首次接线休眠证书底座（unblock 一切）**
- **Phase 1 — 注册编排（saga）+ 身份绑定**
- **Phase 2 — 状态采集 + 事实沉淀 + 动态分组（CQRS）**
- **Phase 3 — 策略匹配 + 期望态 + 命令规划/下发/回执（控制面主脊）**
- **Phase 4 — 合规评估 + 零信任 + 审计闭环**
- **Phase 5 — 全能力补全 + 可观测性 + 双拓扑 + 验收文档**

依赖：Phase 0 unblock 全部；saga（P1）先于 convergence（P3）；inventory（P2）先于 compliance（P4）；
postgres/split-topology（P5）依赖全 cell 就位。

---

## 5. PR 编排

**TDD 纪律**（严格红→绿）：每个 PR 的 contract test + archtest 先以 Wave commit 落 RED，再让实施转 GREEN。
每个 PR 带契约扇出矩阵（`.claude/rules/gocell/contract-fanout.md`）。范围切割的 carve-out 必须同步 backlog。

| PR | Phase | 内容 | 复用/新建 关键 | 主要原语 |
|---|---|---|---|---|
| **PR-0** | 0 | `externalcells/mdm` 骨架：assembly + 外部 module + 空 `enrollcell` + demo 拓扑，health/readyz green | 复用 iotdevice run.go 形态 | bootstrap |
| **PR-1** | 0 | `enrollcell` 接 `certsigning`+`softca`+`deviceprincipal`；serve `http.deviceidentity.status`（L0 读），active-ize status 契约 | 复用休眠底座 | certsigning, L0 |
| **PR-2** | 0 | serve `deviceidentity.enroll`（L2 发证）+ renew + revoke；publish `event.deviceidentity.cert-issued/revoked`（首发布者，active-ize event） | 复用底座 | L2 OutboxFact |
| **PR-3** | 1 | enrollment saga 契约 + Impl（issueCert→createDevice→bindUser→bindTenant→activate）+ 补偿（bind 失败撤证）；wire `Coordinator`（`sagaprojectiondeps.Resolve`）；publish `event.mdm.device-enrolled` | 复用 saga harness（参考 orderfulfillment） | L3 saga |
| **PR-4** | 1 | `identitybind` slice：device↔user↔tenant；多租户 RLS device 表（PG，`tenant_id`+FORCE RLS+`RowVisibility` 位置参） | 复用 TxManager/RLS（参考 accesscore user_repo） | L1/L2, RLS |
| **PR-5** | 2 | `inventorycell` + `stateingest`：多 transport 入站（HTTP+gRPC+MQTT+WebSocket），L2 写状态 fact+outbox | 复用 4 transport（参考 iotdevice grpc/mqtt + websocket hub） | L2, transports |
| **PR-6** | 2 | `inventoryproject`：状态事件→inventory 读模型（projection harness，exactly-once checkpoint，rebuild/OnReset）；serve `http.devicestate` 查询 | 复用 projection harness | L3 CQRS |
| **PR-7** | 2 | `groupproject`：动态分组/Scope（**M1**，规则驱动 projection） | MDM 自建 | L3 CQRS |
| **PR-8** | 3 | `convergencecell` + `policymatch`：consume `event.policy.updated`（accesscore）+ group → 匹配；accesscore policy 经 CellTransport client 调用 | 复用 accesscore policy 契约 | L3, CellTransport |
| **PR-9** | 3 | `desiredstate`（**M2**）：per-device 期望 profile spec store | MDM 自建 | L1/L2 |
| **PR-10** | 3 | 命令规划 + L4 dispatch + receipt：L4 命令队列 + async claimer + 在线 push（WS/MQTT/gRPC）+ **MDM 自有 `PushNotifier` seam + noop/log provider（M7，离线 push 占位，不进框架）**；`command.mdm.remotecommand.v1`（**M4**，cell-owned：lock/wipe/locate/restart） | 复用 command/iotdevice 模式 | L4 command |
| **PR-11** | 3 | `reconcile` 纠偏（**L4 核心控制环**）：期望↔实际 diff→re-plan，level-triggered，leader-elect+`LeaseToken.Epoch` fencing | 复用 reconcile harness | L4 reconcile |
| **PR-12** | 4 | `compliancecell` + `complianceeval`（**M3**）+ `complianceproject`（CQRS dashboard）；serve `http.devicecompliance`（首 serving，active-ize） | MDM 自建判定 + serve 框架契约 | L3 CQRS |
| **PR-13** | 4 | `ztfeed`：compliance posture → accesscore `device_trust` 属性 → Authorize 决策（零信任闭环） | 复用 accesscore PDP | ABAC |
| **PR-14** | 4 | 审计接入：MDM events → auditcore（事件驱动 append；评估是否需 auditcore 加 `auditappenddevice` slice，或 MDM 自发审计事件） | 复用 auditcore | L2 audit |
| **PR-15** | 5 | postgres 拓扑全接线：PG RLS device 表、RabbitMQ broker、Redis claimer/locker、saga/projection PG journal+checkpoint，fail-closed 选型 | 复用 3 个 topology resolver | 全 adapter |
| **PR-16** | 5 | split topology：cell↔cell remote CellTransport，remote peer readiness probe，演示 `transport_mode` in_proc vs remote | 复用 `celltransport.Resolve` | CellTransport |
| **PR-17** | 5 | 可观测性全量：otel tracer + metrics provider，`cell_transport_*`/`auth_pdp_*`/`http_*` metrics，readyz verbose；webhook 出站到外部 SIEM（演示 webhook） | 复用 bootstrap WithTracer/WithMetricsProvider | otel/metrics/webhook |
| **PR-18** | 5 | journey 验收（`J-mdm-enrollment` / `J-mdm-compliance-converge`）+ status-board + README + ADR（MDM 示例架构 + 缺口 backlog 索引） | — | 文档/验收 |

**估算**：18 个 PR（Phase 0–1 共 5 个即可演示「身份+注册」端到端，是 foundation 的最小可叫停点）。

---

## 6. 决策点与风险

1. **deviceidentity 契约 active-ize 由谁负责**：示例 serve 即把 framework 契约 draft→active。需确认 ADR-1939 D3
   允许「example cell serve framework-owned 契约」——若框架团队希望 serving 走专门 framework serving wire 而非
   example，PR-1/PR-2 需调整为「示例消费 + 框架另起 serving PR」。**建议接线前与框架 owner 对齐**（决策点）。
2. **convergencecell 粒度**：合（推荐先合）还是拆三 cell（policy/command/reconcile）。影响 PR-8~11 切分。
3. **push（M7，原 G1）下沉 MDM**：push 整体归 MDM 模块，框架不承载。foundation 期仅 `PushNotifier` seam +
   noop/log 占位（在线闭环靠 WS/MQTT/gRPC），真实 apns/fcm/webpush provider = post-foundation 且仍在 MDM 模块。
   框架升格门槛 = ≥2 框架消费者独立需要离线唤醒；厂商专有 adapter 永不进框架。
4. **多租户 reconcile**：iotdevice 的续期 reconcile 是 `SingleTenant()`；MDM 必须 `TenantScoped()`，command-id
   要编码 tenant 维度（`RECONCILE-TENANCY-DECLARED-01`）。设备扫描查询也要带 tenant。
5. **PG 分区（M5）**：示例先不分区，RLS dual-layer 够（百万级前）；backlog 记触发条件，避免过早优化。

---

## 7. Backlog 登记清单（范围外，须建 issue，不在示例 PR 内做）

**框架 epic（独立）：**
- [ ] G2 — EST(RFC 7030) 前端 `runtime/http/est`
- [ ] G3 — cert 底座 `cellmodules/certdeps` topology-gated wiring helper
- [ ] G4 — enrollment-credential issuer（非 bootstrap token）

> 原 G1（push/wake）经边界审计**移出框架轨**——APNs/FCM/WebPush 厂商专有 API 不进框架，下沉 MDM（见下 M7）。

**MDM future（示例外）：**
- [ ] M5 — PG 设备表分区（触发：单租户 >百万行 或 P95 查询 >500ms）
- [ ] M6 — 应用管理 / OS 更新管理 / attestation
- [ ] M7（原 G1）— push/wake：`PushNotifier` seam + apns/fcm/webpush provider，**MDM 模块（`externalcells/mdm`）自建**，post-foundation；框架升格门槛 = ≥2 框架消费者（厂商 adapter 永不进框架）

---

## 附：关键复用锚点（file:line）

- 设备示例蓝本：`examples/iotdevice/`（9 slice：register/list/status/command×6/command-internal/command-rpc/bootstrap/certrenewal/certcompletion）
- saga 参考：`examples/orderfulfillment/run.go`、`cells/orderfulfillmentcell/internal/sagaimpl/impl.go`；`framework/runtime/saga/coordinator.go:259`（`NewCoordinator`）；`cellmodules/sagaprojectiondeps/sagaprojectiondeps.go:54`（`Resolve`）
- projection 参考：`framework/kernel/projection/coordinator.go:131/220`（`NewCoordinator`/`Subscribe`）；`examples/orderfulfillment/cells/orderfulfillmentcell/slices/orderstatus/service.go:140`（Apply）
- 证书底座：`framework/runtime/certsigning/`、`framework/runtime/certlifecycle/reconciler.go`、`adapters/softca/`、`framework/runtime/auth/deviceprincipal.go`
- 多租户数据层：`adapters/postgres/tx_manager.go:69`（tenant scope 注入）、`framework/pkg/tenant/rowvisibility.go:94`、`corecells/accesscore/internal/adapters/postgres/user_repo.go`（`GetByIDInTenant` RowVisibility 位置参）
- transport：`framework/runtime/websocket/hub.go:738`（`BroadcastToSubject`）、`framework/runtime/webhook/receiver.go:96`、`framework/runtime/transport/{inprocess,remote}.go`
- 可观测性：`framework/runtime/bootstrap/options_metrics.go:29`（`WithMetricsProvider`）、`options_http.go:60`（`WithTracer`）、`framework/runtime/auth/pdp_metrics.go:54`
- 平台设备契约：`contracts/http/deviceidentity/`、`contracts/http/{devicestate,devicecompliance}/`、`contracts/event/deviceidentity/`、`contracts/http/policy/`（accesscore，已 active）
- 关键 ADR：`docs/architecture/202606121500-1895-adr-device-cert-framework-pivot.md`、`docs/architecture/202606130635-1939-adr-framework-owned-contract.md`
