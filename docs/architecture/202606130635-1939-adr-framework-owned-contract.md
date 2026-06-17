# ADR: 框架归属契约（framework-owned contract）

- Status: Accepted
- Date: 2026-06-13
- Issue: #1939（本 ADR + 机制）；blocks #1899（设备中立契约消费方）；epic #1895；关联 #1052
- Scope: 引入「契约可由框架/控制面归属，而非某个 Cell」这一一等概念 + sealed owner 类型 +
  治理/codegen 落点 + 威胁模型。设备契约（deviceidentity/devicestate）是首个消费方，但本 ADR
  只定义机制，不落具体设备契约（那是 #1899）。

## Context

epic #1895（ADR `202606121500-1895`）把设备证书提升为框架能力，并在 D5 要求定义 4 个**中立、
provider-agnostic** 的设备契约（deviceidentity/devicestate/devicecompliance/remotecommand），供
未来 MDM/ZT 消费**任意** provider。落 #1899（PR-3/1895）时暴露一个概念模型层矛盾：

- **开源对标 4/4 一致**：中立、provider-agnostic 的契约由**框架/控制面拥有，永不绑单一 consumer**。
  - cert-manager：`CertificateRequest` 属 API group `cert-manager.io`（框架），ACME/CA/Vault/外部
    issuer 都是**实现方**，经 `issuerRef.Group` 区分；没有任何 issuer 拥有 `CertificateRequest`。
  - SPIFFE：Workload API spec 属 SPIFFE Steering Committee（多方治理），SPIRE/Istio/Consul 是实现方。
  - Kubernetes：CSI/CNI/Gateway-API 接口属 SIG/跨社区 WG，存储/网络/网关厂商是实现方，不拥有 API group。
  - step-ca：CA 拥有 enrollment 端点，provisioner 是认证策略配置，clients 是纯消费方。
  - **判据**：当一个契约的正确性要求 provider 可互换，该契约必须属框架层，不能属任何单一 implementer/consumer。
- **GoCell 现模型**把「谁定义/拥有 schema」与「哪个 Cell serve」揉进单一 `ContractMeta.OwnerCell string`，
  治理规则 REF-03 硬性要求 `ownerCell ∈ 已知 Cell`（`PhaseBase`，`lifecycle: draft` 不豁免），
  `endpoints.server/publisher` 同样校验为 Cell。100% 平台契约归 3 个 corecell。**无框架/runtime/actor
  归属路径**。把中立设备契约 park 到 `accesscore` 机械可过（path↔ownerCell 解耦），但开源 4/4 判为
  anti-pattern，语义错位（accesscore 是 access/RBAC cell，不 serve deviceidentity/EST）。
- 1895-ADR D2/D3 又**禁建 certcore 平台 corecell**（证书底座是 runtime primitive，不是平台 Cell）。

GoCell 既有两个相邻模式，但都不直接适配：

- **Pattern X** — `runtime/internal/contractbuild.NewFrameworkHTTP` + bootstrap RouteGroup
  （health/readyz/metrics/projection-rebuild；`http.framework.*` ID，**无 contract.yaml**，无 Cell，
  治理不可见）。提供「框架 serve」，但**缺 schema/版本/codegen/契约级测试**——这些恰是 #1899/FR-013 要的。
  相邻 ADR `202605261620-adr-cqrs-projection-lifecycle-harness.md` 曾否决 host-cell、选 Pattern X
  无-contract.yaml 路线，但其语境是 **schemaless 运维端点**，与版本化**消费契约**是不同需求。
- **Pattern Y** — contract.yaml + Cell `ownerCell`（所有现有契约）。提供 schema/codegen/版本，但
  REF-03 强制 Cell owner。

## Decision

引入 **framework-owned contract**：契约的「定义归属」可以是**框架**（而非某个 Cell），从根因解耦
`OwnerCell` 里被揉在一起的「schema 定义归属」与「serving Cell」。它是 Pattern X（框架 serve）与
Pattern Y（版本化 contract.yaml）之间的桥：框架归属契约有完整 contract.yaml/schema/codegen/契约测试，
但归属框架而非 Cell。

### D1 — sealed `ContractOwner` 类型联合（Hard）

`kernel/metadata` 新增保留 owner 值 `FrameworkOwnerSentinel = "_framework"`（镜像 metrics `_runtime`
sentinel；下划线前缀不是合法 Cell id，绝不与真 Cell 碰撞）+ sealed `ContractOwner` 类型联合
`Cell(id) | Framework`：

- 两字段 unexported，唯一构造入口是 `ContractMeta.Owner()`（经包内 `resolveContractOwner` funnel）——
  包外**不能字面量伪造**框架 owner。
- 唯一取 cell 入口 `ContractOwner.Cell() (id string, ok bool)`：框架 owner 返回 `ok=false`——把
  「framework 当 cell 用」变成**类型级不可表达**（不是运行时谓词）。每个需要 cell 的下游必须经 `Cell()`，
  绕不过。
- 声明面是 `ownerCell: _framework`（或 `endpoints.server/publisher: _framework`，经 G-7 auto-derive）。
  contract.schema.json 的 `ownerCell` 是无 pattern 的 string，`_framework` 直接合法。

### D2 — cell-owner 规则结构性排除，非散落 escape（彻底/优雅）

cell-owner 引用规则**经 `Owner().Cell()` 自然只作用于 Cell owner**，框架 owner 结构性排除，**不在规则里散落
`if isFramework` 字符串 escape**：

- **REF-03**（owner 是已知 cell）：`cellID, ok := c.Owner().Cell(); if !ok { continue }`——typo'd cell
  仍解析为 Cell 并触发（REF-03 保值）。
- **REF-13**（provider 是 cell/actor）：框架 owner 的 provider 是框架，skip。
- **CONTRACT-CONSISTENCY-EMIT-01**：triggers↔outbox.Emit 耦合假设有 serving Cell slice，框架契约没有
  （emit 在 runtime serving 层），skip。
- CH-01（owner 非空）：`_framework` 非空，自然通过，无需改。active-only 规则（DEAD-CONTRACT-01、
  CONTRACT-ENDPOINT-TEST-MAPPING-01）因 D3 强制 draft 而自动跳过框架契约。

### D3 — framework 侧等价治理 `FRAMEWORK-OWNED-CONTRACT-SCOPED-01`（彻底：非豁免）

新增治理规则，使「写 `ownerCell: _framework`」是**re-route 到框架侧治理**，不是逃出所有治理：

1. **eligible kind**：只有 http/event 可框架归属（中立 provider-agnostic wire 面）。command/projection/
   saga/webhook/grpc 带 Cell-内部语义，必须 Cell owner。新 kind 经扩展 allow-set + red case 准入，
   未 vet 的 kind 今天 fail-closed。
2. **serving-scan lifecycle**（#2037 起 serving 已 wire，见下 §Serving + §Amendment 2026-06-18）：框架契约
   draft|deprecated 始终放行。active 框架契约放行**当且仅当**其 id ∈ 某 assembly 的 `frameworkContracts`
   （per-deployment serving opt-in）——这是 metadata 侧 serving-scan，DEAD-CONTRACT-01 的框架版；active 但
   无任何 assembly serve = fail-closed（声明 active 却无人 serve 的静默 dead 契约）。**双向闭环**：规则同时
   校验每个 `assembly.frameworkContracts` 条目是存在的、active 的、framework-owned 的 http/event 契约
   （堵手写 list drift）。实际 Go 挂载完整性（active 框架契约必有 bootstrap 框架 RouteGroup）由 bootstrap
   启动期 fail-fast（`validateFrameworkServing`）守——metadata 看不到 RouteGroup，故 wiring 完整性是 runtime
   边界（对标 SPIRE `catalog.Load` / controller-runtime `Builder.Build` / 本仓 gRPC #2204）。

### D4 — codegen owner-agnostic（无 fence 代码，自动）

`tools/codegen/contractgen` **零** `OwnerCell` 引用：框架契约按 kind 生成 wire types/iface/handler 与
Cell 契约**完全一致**。cellgen 由 slice.yaml `contractUsages` 驱动，无 slice 引用框架契约 ⇒ **自动不产 Cell
注册胶水**。「fence」是结构性的，无需额外代码（优雅简洁）。

### D5 — funnel 反旁路守卫 `CONTRACT-OWNER-CELL-FUNNEL-01`（Medium）

archtest 禁止 kernel/governance 直接 `*.Cells[*.OwnerCell]` 索引（旁路 `Owner().Cell()` 重新引入
cell-存在假设、把 `_framework` 当缺失 cell）。合法的 `.OwnerCell` 读（CCE-01 的相等比较、
kernel/registry.ByOwner 的分组）不受影响；只禁危险的 Cells-索引-by-OwnerCell shape。带 synthetic red case。

### Serving（本 ADR 延后；#2037 落地）

本 ADR 起初 **不** wire 框架 serving（无 active 框架契约，提前建 serving 是 speculative）。**#2037（PR-8a）
落地了 serving harness**，机制（与本 ADR 原计划一致，但 serving-scan 选 runtime fail-fast 而非静态 RouteGroup
扫描，见 §Amendment 2026-06-18）：

- `assembly.yaml` 新增 `frameworkContracts: [...]` —— per-deployment serving opt-in（framework 契约无 cell，
  boundary 派生不可见，故 assembly 显式声明）；codegen 派生 `generatedFrameworkServedContracts()`（modules_gen.go，
  generatedverify 守 drift）。
- composition root 构造 Service（`cellmodules/deviceserving`）+ `bootstrap.WithFrameworkHTTPServing(expected, routes)`；
  bootstrap phase5 把框架 RouteGroup（CellID=""）挂到声明 listener，phase0 `validateFrameworkServing` reconcile
  expected（codegen）vs wired routes，不匹配启动期 fail-fast。
- `NewFrameworkHTTP` **不**用于业务框架契约（它继续只服务 `http.framework.*` schemaless 运维端点）；generated
  handler 已内嵌 codegen 派生的 contractSpec，serving 复用它，无平行 ContractSpec 构造。

health/metrics 等 schemaless 运维端点仍**不迁移** contract.yaml（无版本化 wire 契约需求，与 Pattern X 并存非冲突）。

## 威胁矩阵

| 威胁 | 缓解 | 评级 |
|---|---|---|
| 包外伪造框架 owner | sealed `ContractOwner`（unexported 字段，唯一 `Owner()` 构造） | Hard |
| 把 framework 当 cell（越权解析为缺失/存在 cell） | `ContractOwner.Cell()` 对框架返回 ok=false，类型级不可表达 | Hard |
| `ownerCell: _framework` 逃出所有治理 | `FRAMEWORK-OWNED-CONTRACT-SCOPED-01`（kind + serving-scan lifecycle + assembly 条目校验）；FMT 格式规则照常作用 | Medium |
| 旁路 `Owner()` 直接 `Cells[OwnerCell]` | `CONTRACT-OWNER-CELL-FUNNEL-01` archtest（typed scan + red case） | Medium |
| active 框架契约静默 dead（声明 active 却无人 serve） | 双层（#2037）：① metadata serving-scan（active 框架契约须 ∈ 某 `assembly.frameworkContracts`，validate 期 fail-closed）；② bootstrap 启动期 `validateFrameworkServing`（声明 served 但无 wired RouteGroup → fail-fast，对标 gRPC #2204 / SPIRE / controller-runtime）。「must-wire」方向是文档化 Go 天花板（#851/#893/#1282 族） | Medium |
| 框架契约误生成 Cell 注册胶水 | cellgen slice-driven，无 slice 引用 ⇒ 结构性不产胶水（D4） | Hard（结构性） |

每条 enforcement 与实现同 PR 闭环（sealed 类型 + 治理 + archtest + 反向 red case + 本 ADR）。无 Soft。

## 与既有 ADR 的关系

- **`202605261620-adr-cqrs-projection-lifecycle-harness.md`**（Pattern X 无-contract.yaml）：**不冲突，互补**。
  那里治理的是 schemaless **运维端点**（health/projection-rebuild），本 ADR 治理版本化**消费契约**。二者
  并存是恰当分层：运维端点无需 schema/版本，消费契约需要。本 ADR 不改写该 ADR。
- **`202606121500-1895` D5**：D5 要求 4 中立契约落 `contracts/`，未指定 owner。本 ADR **实现** D5 的归属
  机制（框架归属）。已在 1895-ADR D5 加前向引用。

## Consequences

- **正向**：中立设备契约（#1899）可获得**框架归属** + 版本化 schema/codegen/契约测试，对齐开源 4/4 范式，
  不 park 到 accesscore（消除 anti-pattern），不建 certcore corecell（守 1895-D2）。机制 Hard 核 + Medium 守卫，
  无 Soft。
- **代价**：新增一个 owner 概念 + 1 治理规则 + 1 archtest；REF-03/REF-13/CCE-01 经 `Owner().Cell()` 改读
  （非散落 escape）。serving 延后到 active 化 PR。
- **后续**：#1899 用框架 owner 落 deviceidentity/devicestate（draft）；framework serving + D3 active 扩展在
  EST wire PR；command/其它 kind 的框架归属按需扩展 D3 allow-set。
- **framework 契约 draft→active 迁移 PR 必须同步**（#2037 落地此清单，机制见 §Amendment 2026-06-18）：
  (1) wire 框架 serving RouteGroup（assembly.frameworkContracts → codegen `generatedFrameworkServedContracts()`
  → composition root `bootstrap.WithFrameworkHTTPServing` + bootstrap phase5 挂载；**不**用 `NewFrameworkHTTP`，
  复用 generated handler 内嵌 contractSpec）；(2) 扩展 D3 lifecycle 规则为 serving-scan（active 框架契约须 ∈ 某
  assembly.frameworkContracts + 条目校验）+ bootstrap 启动期 fail-fast；(3) 补 Journey coverage（迁 active 的
  框架契约必须有 Journey 引用；framework-serving journey 用 `cells: [_framework]`，REF-06 结构性豁免 sentinel）。

## Amendment 2026-06-18 — #2037（PR-8a：framework HTTP serving harness）

落地 §Serving 与 Consequences 迁移清单。**与本 ADR 原计划的一处偏离 + 理由**：原 D3 设想 serving-scan = 静态
「扫描 serving RouteGroup」。但 governance 是 metadata-only，看不到 Go RouteGroup；开源对标（cert-manager
issuerRef condition / k8s APIService Available 是 runtime；SPIRE `catalog.Load` Constraints.Check /
controller-runtime `Builder.Build` 是 startup 构造期 error）一致表明「已声明但无 serving 后端」的检测在成熟框架是
**runtime/startup**，不是静态扫描——且本仓 gRPC PDP gate（#2204）与 HTTP `ResolveAuthorizer` 已是同款启动期
fail-fast idiom。故 serving-scan 分两层落地：

- **metadata serving-scan**（`FRAMEWORK-OWNED-CONTRACT-SCOPED-01`，validate 期，Medium）：active 框架契约须 ∈ 某
  `assembly.frameworkContracts`；双向校验 assembly 条目是 active framework http/event 契约。
- **bootstrap 启动期 fail-fast**（`validateFrameworkServing`，phase0，Medium）：codegen 派生的 must-serve 集
  （`generatedFrameworkServedContracts()`）与 wired 框架 RouteGroup 不匹配即 fail-fast（must-wire 方向是文档化
  Go 天花板，同 #851/#893/#1282）。**不**新增 archtest（runtime 查实际挂载强于 AST 扫描，无间接持有盲区）。

**`CONTRACT-ENDPOINT-TEST-MAPPING-01` 框架豁免**：该 active-only 规则原假设有 serving cell slice；框架契约无 cell
（D4），故 `isActiveServeStylePlatformContract` 结构性排除 `Owner().IsFramework()`（同 REF-03/13/CCE-01 模式）——
framework 契约的契约级覆盖是 bootstrap/cellmodules 框架层测试，非 cell-slice verify.contract。

**首个 active 锚点**：`http.devicestate.v1`（L0 只读）draft→active；`cellmodules/deviceserving` 提供诚实 baseline
（无 presence 后端时 `state=unknown` + observedAt=判定时刻，schema 明文「unknown 时 observedAt 仍必填」，绝不臆造
online/offline）。写路径 + status.v1 留各自 PR 经本 harness active 化。威胁矩阵「active 框架契约静默 dead」行已重评。

## 参考

- 对标：cert-manager Issuer/CertificateRequest + external-issuer；SPIFFE/SPIRE Workload API +
  `pkg/common/catalog` Constraints.Check（startup fail-fast）；controller-runtime `pkg/builder` Build（构造期 error）；
  k8s kube-aggregator APIService `Available` condition；smallstep/certificates provisioner。
  `ref: spiffe/spire pkg/common/catalog/catalog.go`、`ref: kubernetes-sigs/controller-runtime pkg/builder/controller.go`。
- 内部：`kernel/metadata/owner.go`、`kernel/metadata/types.go`（AssemblyMeta.FrameworkContracts）、
  `kernel/governance/rules_framework_owned.go`、`kernel/governance/rules_contract_test_mapping.go`、
  `kernel/assembly/generator.go`（generatedFrameworkServedContracts）、`runtime/bootstrap/framework_serving.go`、
  `cellmodules/deviceserving/`、`tools/archtest/contract_owner_cell_funnel_01_test.go`、ADR `202606121500-1895`、
  `202605261620-adr-cqrs-projection-lifecycle-harness.md`、`.claude/rules/gocell/contract-fanout.md`。
