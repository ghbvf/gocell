# GoCell 软件工程视角能力审查 — 不可分割 / 冗余 / 不该有

| 项 | 值 |
|---|---|
| 评估日期 | 2026-05-04 |
| 基准 commit | `11600a4f`（develop） |
| 评估视角 | 软件工程 — SRP / DRY / YAGNI / 关注点分离 / 形态层边界 |
| 与第一性原理报告关系 | `20260504-first-principles-capabilities-and-replaceable-deps.md` 答"必须哪些"，本报告答"该不该有 / 有没有冗余" |

---

## 1. 不可分割（atomic — 拆了公理塌陷）

按 **SRP + 删它系统不成立** 双重判定。

### 1.1 8 项核心原子能力

| # | 能力 | 单一 concern | 拆解后果 |
|---|---|---|---|
| 1 | **Cell / Slice 模型** | 业务建模的最小单位 | 删了 GoCell 不存在 |
| 2 | **Contract 边界** | 跨 cell 通信的唯一通道 | 拆了 = cells 直 import，分层崩 |
| 3 | **生命周期编排** | 启停的最小协议 | 拆了 = 部署单元无法装配 |
| 4 | **元数据治理** | 声明式架构的最后一道闸 | 拆了 = yaml 与代码漂移不可见 |
| 5 | **静态架构守卫**（archtest） | 防腐的唯一武器 | 拆了 = 守卫只能靠人审 |
| 6 | **代码生成** | 消除手工对齐成本 | 拆了 = cell.yaml ↔ Go 字面量永远漂移 |
| 7 | **时钟抽象** | 可测试性根基 | 拆了 = TTL/lease 类测试必须 wall-clock，CI flake |
| 8 | **Outbox 接口** | L2/L3 一致性的协议层 | 拆了 = L2 Cell 无统一抽象 |

### 1.2 8 项不可单独删的支撑工具

`kernel/depgraph`（archtest 必需）/ `kernel/registry`（assembly 必需）/ `kernel/ctxkeys`（context 传播必需）/ `kernel/worker` + `kernel/health` 接口 / `kernel/idempotency` 接口（outbox 必需）/ `kernel/crypto` 接口（KeyProvider 抽象）/ `kernel/persistence` 接口（TxRunner 抽象）/ `kernel/scaffold` 引擎

**共 16 项。删任意一项 → 公理塌陷。**

---

## 2. 冗余（duplicated — 同一 concern 多套实现 / 概念分裂）

### 2.1 真冗余（应消除）

**1. `ContractMeta`（kernel/metadata）↔ `ContractSpec`（kernel/wrapper）双定义** ⚠️ 严重

- 同一 "contract" 概念两套结构：前者 yaml 解析（治理用）、后者代码字面量（运行时用）
- governance FMT-17 单向校验，PR 评审中易漂移
- **已登记 backlog**：`KERNEL-CONTRACTSPEC-CONTRACTMETA-DUAL-DEF-01`（P1 + ADR 前置）

### 2.2 伪冗余（实际是分层 / 替身意图，不动）

| 表面冗余 | 实际意图 | 判定 |
|---|---|---|
| `runtime/{outbox,command,crypto}` 与 kernel 同名 | type alias 包暴露 SDK 表面给 cells | 合理 |
| `kernel/depgraph` + `tools/depgraph` | kernel 定义数据模型（不依赖 x/tools），tools 用 packages.Load 构图 | 合理 |
| `pkg/contracts` vs `contracts/` 顶层 | 前者共享 Go 类型，后者权威 yaml | 合理 |
| `pkg/ctxkeys` vs `kernel/ctxkeys` | 前者通用 correlation/trace，后者 cell-model 标识 | 合理 |
| `runtime/eventbus` vs `adapters/rabbitmq` | 同接口两实现（LSP 范例 + dev/test 替身） | 合理 |
| `runtime/auth` JWT / ServiceToken / mTLS 三种 token | 同 concern 三场景（用户 / 服务 / peer） | 合理 |

**真正冗余只 1 项**。其余表面冗余都是分层意图，不该消除。

---

## 3. 不该有（misplaced — 越界 / YAGNI 违例 / 空承诺）

### 3.1 严重违例（最大问题）

**1. 平台 Cell（`cells/{accesscore,auditcore,configcore}`）在 framework 仓** ❌

- **违例本质**：框架应提供 Cell 模型 + scaffold，**不应 ship 具体业务 Cell**
- **后果**：客户做 IoT / order / BFF 场景（见 `examples/{iotdevice,todoorder,ssobff}`）用不到 accesscore 的 RBAC，但仍要在 vendor 树看到。"装框架带了一套用户系统"的奇怪体验
- **混淆边界**：`corebundle` assembly 默认带这 3 个，让"框架"和"v1 平台"边界模糊
- **正确归属**：移到 `examples/platform-cells/` 或独立仓 `gocell-platform-cells`（与 K8s vs `kube-apiserver` / `coredns` 同关系）
- **形态层证据**：3 cell 的业务正确性已被本次审查显式列为"不在框架审查范围"（见 horizontal review §0.3）

### 3.2 YAGNI 违例（空 surface — 现在用不到却造好了的）

**2. `command` / `projection` contract kind 在平台层 0 实例**

- `kernel/metadata.ContractMeta.Kind` 声明 4 类（http / event / command / projection），但平台契约 49 条里只有 http (33) + event (16)
- command / projection 在 `contracts/` 顶层目录留空（仅 examples/iotdevice 用 command）
- 后果：`EndpointsMeta` 用 10 个 omitempty 字段表达 4 类拓扑，governance / codegen 5 处分支为 0 实例的能力买单
- 已登记 backlog（与 `KERNEL-CONTRACTSPEC-CONTRACTMETA-DUAL-DEF-01` 同根）

**3. `l0Dependencies` schema 字段全空 + L0 cell 0 实例**

- 3 个 core cell 的 cell.yaml 都是 `l0Dependencies: []`
- 项目内**无任何** `type: l0` 的 cell 实例
- CLAUDE.md 声称"L0 Cell（纯计算库）可被同一 assembly 内的兄弟 Cell 直接 import"，但 schema 字段是死代码路径
- 已登记 backlog（cells review §P2-第 8 条）

**4. `fixtures/` 仅 `.gitkeep`**

- CLAUDE.md 声称"供 run-journey 使用"
- 8 条 J-*.yaml 无任何 `fixtures:` 字段，runner 也未读取
- "目录承诺"与"代码现实"不一致 = 幽灵能力
- 已登记 backlog（与 `JOURNEY-ACTIVE-LIFECYCLE-EMPTY-01` 区域同根）

### 3.3 依赖越界（不该被拖入的 indirect）

**5. `hashicorp/vault/api` 拖入 `hcl` / `mapstructure` / `go-homedir` 等死链路**

- 这些 indirect 在 GoCell 项目内 **0 直接 import**（已 grep 验证）
- 完全是 vault api 内部依赖的传染
- framework 仓库不该拖这种 indirect 噪声

### 3.4 灰区（设计权衡 — 不算明显违例）

**6. `runtime/devtools/catalog` 在 runtime 层而非 devtools cell**

- J1 PR#357 选择 runtime/ 装载（30 行胶水接线）而非建独立 devtools cell（~5 文件 + schema）
- 严格 SRP 角度 runtime/ 装载"开发期工具"略越界
- 已在 backlog 登记 T10 触发条件项（出现 cell 自定义字段需求时再迁）

**7. `kernel/observability/` 缺 `doc.go`**

- 子模块存在但无包级文档说明与 `runtime/observability/` 的职责切分
- 这是治理疏漏而非"不该有"——已登记 backlog（kernel review §P1-第 2 条）

---

## 4. 总评

| 类别 | 数量 | 健康度 |
|---|---|---|
| **不可分割** | 16 项（8 核心 + 8 支撑） | ✅ 边界清晰，无模糊 |
| **真冗余** | 1 项（ContractMeta / ContractSpec 双定义） | ⚠️ 已登记 backlog |
| **严重违例** | 1 项（平台 Cell 在 framework 仓） | ❌ **最值得仲裁的架构边界问题** |
| **YAGNI 违例** | 3 项（command / projection kind、l0Dependencies、fixtures/） | ⚠️ 全部已登记 backlog |
| **依赖越界** | 1 项（vault indirect 死链路） | 🟡 触发条件型 |

### 一句话结论

GoCell 真正的软件工程问题不在"代码层冗余"（项目极少代码重复），而在**架构边界**——**平台 Cell 与 framework 仓的混合是最大违例**，其余 YAGNI 违例（4-kind 分类 / L0 cell / fixtures）都是次级表象，应一次性"要么落地要么撤回"。

> 报告结束。
