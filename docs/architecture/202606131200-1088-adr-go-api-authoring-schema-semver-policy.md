# ADR: Go SDK 公开符号 + Authoring Schema 的 SemVer 政策（轴 A）

- 时间: 2026-06-13
- 状态: Accepted
- Issue: #1088（CLI versioned release + SemVer 承诺，M7）
- Scope: **轴 A** — Go SDK 公开符号（`kernel/`、`runtime/composition`、`contractspec` 的 exported 符号）+
  4 个 authoring schema（`cell.yaml` / `contract.yaml` / `slice.yaml` / `assembly.yaml`，
  对应 `kernel/metadata/schemas/*.schema.json`）。本 ADR **不治理** wire 契约（HTTP / event / command 运行时协议，
  那是轴 B，见 wire ADR `202605211200-adr-pre-v1.0-direct-v1-evolution.md` 与
  `.claude/rules/gocell/api-versioning.md`）。
- ref: Operator-SDK R9（外部消费方 `go get @vX.Y.Z` 编译耦合面）；SemVer §4（v0.x minor/patch 允许 breaking）；
  #1724（v0.x 自用不做承诺）；#1081（v1.0 GA 后外部 Cell 仓库承诺起点）

## Context

### 轴 A 的定义

GoCell 的版本化面由两条轴组成，必须分别治理：

- **轴 A（本 ADR）**：外部 Cell 仓库 `go get github.com/ghbvf/gocell@vX.Y.Z` 后**编译时**耦合的面。
  包括：
  1. Go SDK 公开符号：`kernel/`、`runtime/composition`、`contractspec` 的所有 exported 类型、函数、接口、常量。
  2. 4 个 authoring schema：`cell.yaml` / `contract.yaml` / `slice.yaml` / `assembly.yaml`
     （对应 `kernel/metadata/schemas/*.schema.json`），外部仓库用 `gocell validate` 对其进行静态校验。

- **轴 B（wire，非本 ADR）**：HTTP / event / command 运行时协议，治理规则已在
  `.claude/rules/gocell/api-versioning.md` 中落地「Pre-GA wire 破坏窗口至 2026-12-31」。
  轴 B 故意在 pre-GA 阶段**不走 SemVer**（in-repo 全调用方随同一 PR 原子更新，版本目录隔离此时为纯仪式）。

### 问题起点

issue #1088 M7 要求 gocell CLI 与 framework module **同 tag 原子发布**（恒等版本）。外部 Cell 仓库（#1081 Operator-SDK 模式）
通过 `go get github.com/ghbvf/gocell@vX.Y.Z` 使用框架，当轴 A 发生 breaking change 时，编译直接失败（R9 风险）。
这要求框架在轴 A 上有明确的版本政策，以便外部消费方知道何时需要适配。

### 两轴为何不能共享一个政策

轴 B（wire）的 pre-GA 窗口政策是「允许原地修改 active 版本，无需创建 v2」，其依据是 in-repo 无独立 wire 消费方。
轴 A（Go SDK）一旦有外部 Cell 仓库 `go get`，就**已经存在**独立消费方；把轴 A 的 SemVer 承诺塞进 wire ADR 会与
pre-GA 窗口正面冲突。因此必须新建独立 ADR，而非 amend wire ADR。

### CLI 与 framework 的关系

`cmd/gocell/` 是独立子 module（`github.com/ghbvf/gocell/cmd/gocell`）；CLI 与 framework 由 release 流水线
**原子同 tag 发布**（恒等版本，见 #1088 M7）。CLI 版本与 framework 版本一一对应，通过 `gocell version` 命令的
`compatible_framework_range` 字段表达运行时兼容范围（该字段从单一注入的 version 常量派生，非手维护，见
`docs/guides/cli-version-compatibility.md`）。

## Decision

### D1 — 分级承诺：v0.x intent，v1.0 GA 起 guarantee

| 阶段 | Go 公开符号（轴 A-1） | Authoring schema（轴 A-2） | 依据 |
|------|-----------------------|---------------------------|------|
| **v0.x（至 v1.0 前）** | **Intent**：尽量不 breaking；breaking 须 PR 说明 + CHANGELOG 标 `[breaking-api]` | **Intent**：字段只增不删优先，允许修正 schema 语义错误（如必填改可选、typo 修正） | SemVer §4：v0.x minor/patch 允许 breaking；#1724 v0.x 自用不做承诺 |
| **v1.0 GA 起** | **Guarantee**：删/改名/类型变更 → major；新增可选符号 → minor；bug fix / 无 API 变更 → patch | **Guarantee**：新增可选字段 → minor；删/改必填/改语义 → major | #1081 Operator-SDK R9 外部消费方；SemVer §3 |

「intent」意味着框架**尽力**遵守，但不承担与 major bump 等价的责任；「guarantee」意味着违反即为 release 流程 bug，
必须在 GA 的 enforcement 机制（见 D4）下阻止合并。

**v0.x 不是随意 breaking 的授权**：每个 breaking-api 变更仍须在 PR 说明动机，并在 CHANGELOG [Unreleased] 段
标注 `[breaking-api]`，使外部消费方能识别升级风险。

### D2 — CLI ↔ framework 兼容矩阵承载点

`gocell version` 输出三个字段：

```
cli_version:                v0.1.0
framework_version:          v0.1.0
compatible_framework_range: >=v0.1.0 <v0.2.0
```

- `compatible_framework_range` 由单一注入的 version 常量**派生**，非手写维护的独立常量。
- 每次 stable release 在 `docs/guides/cli-version-compatibility.md` 追加一行兼容矩阵记录。
- v0.x 阶段该兼容范围是 **best-effort intent**；v1.0 GA 后切换为 **hard guarantee**（由 D4 enforcement 守卫）。

### D3 — v0.x breaking-api CHANGELOG 标注约定

v0.x 阶段发生 Go 公开符号或 authoring schema 的 breaking 变更时：

1. PR body 标注 `[breaking-api]`，说明：(a) 哪个符号/字段变了；(b) 外部消费方需要做什么适配。
2. CHANGELOG `[Unreleased]` 段在 `### Breaking Changes` 下加条目，标注 `[breaking-api]`。
3. 变更不阻塞合并（v0.x intent-level，非 CI 门禁），但在 PR review 层面 flag out。

### D4 — v1.0 GA 切换为 guarantee 的 enforcement（待建 backlog）

v1.0 GA 时，意图层 → 机器强制的切换需以下动作：

- 接入 `golang.org/x/mod/cmd/gorelease`（或 `apidiff`）作为 CI 门禁，检测 Go 公开符号的 breaking change，
  输出须与 SemVer bump 级别吻合。
- authoring schema breaking change 检测：新增 JSON-schema diff 工具，校验 `required` 增加、字段删除、
  类型收紧是否与 major bump 对应。
- 两个工具的 CI 接入见「待登记 backlog」（本 ADR 落地时，请主 agent 建立 GitHub Issue 追踪）。

**当前状态（v0.x）**：上述门禁**不实现**。v0.x 政策为 intent-level（非伪 Hard），不建虚假 CI 门禁。

### D5 — contractspec 的外部 import 限制

`contractspec` 包含 contract schema 类型；外部 Cell 仓库**不应** import `contractspec`（应通过 `gocell validate`
CLI 校验，不直接 Go 依赖）。现有 archtest 已禁止此 import 路径，本 ADR 引用该 enforcement，不新建。

## 威胁矩阵

| 威胁 | 缓解 | 评级 |
|------|------|------|
| 外部 `go get -u` 踩 sealed marker constructor 签名变更，编译失败（R9 根因） | v0.x intent：PR 说明 + `[breaking-api]` CHANGELOG；v1.0 GA 后 gorelease CI 门禁阻止未预告的 breaking merge | Soft（v0.x）→ Medium（v1.0 GA，待建 backlog #gorelease-ci） |
| authoring schema 改必填字段，外部 `gocell validate` 失败 | v0.x intent：schema breaking 须 PR 说明；v1.0 GA 后 JSON-schema diff CI 门禁 | Soft（v0.x）→ Medium（v1.0 GA，待建 backlog） |
| v0.x「intent」被外部消费方误读为「guarantee」 | 本 ADR + cli-version-compatibility.md + cell-external-repo-quickstart.md 明确 v0.x = best-effort intent；`gocell version` 输出注明「pre-GA」 | Soft（文档澄清，无机器强制，可接受：v0.x 定义上不保证） |
| v1.0 GA 后忘记切换 intent → guarantee，enforcement 未接入 | 本 ADR §D4 列出 v1.0 GA checklist；GA ADR 的 PR body 必须逐条核销（见下 §v1.0 GA self-closure checklist） | Medium（self-closure checklist 是人工检查，GA PR review 强制对照） |
| contractspec 被外部误 import，绕过 CLI 校验路径 | 现有 archtest 已禁止（引用，不重复建） | Medium（现有 archtest 守卫） |
| CLI 与 framework 版本对应关系手动维护漂移 | `compatible_framework_range` 从单一注入 version 常量派生，非手维护；`cli-version-compatibility.md` 表由 release checklist 驱动追加 | Medium（派生逻辑的正确性由 release 流程保证，非编译期 Hard） |

**注**：v0.x 阶段威胁 T1/T2 评级为 Soft，因为 SemVer §4 明确允许 v0.x breaking；此时接 CI 门禁
反而违背「intent」语义。v1.0 GA 后提升为 Medium（CI 门禁）是预期的升级路径，登记 backlog 而非
现在虚建占位门禁。

## v1.0 GA self-closure checklist

v1.0 GA ADR 的 PR body 必须逐条核销下列项，reviewer 必须逐行对照通过后才能 approve：

- [ ] `gocell version` 的 `compatible_framework_range` 语义从 best-effort intent 更新为 hard guarantee，
      文档（cli-version-compatibility.md、cell-external-repo-quickstart.md）同步更新。
- [ ] `golang.org/x/mod/cmd/gorelease`（或同等 apidiff 工具）CI 门禁接入，breaking Go API 变更被 CI 阻断。
- [ ] authoring schema JSON-schema diff 工具接入 CI，breaking schema 变更被 CI 阻断。
- [ ] 本 ADR 状态从 `Accepted` 改为 `Superseded by <v1.0 GA ADR>`。
- [ ] `.claude/rules/gocell/api-versioning.md` §兼容窗口中关于轴 A 的 best-effort 注释更新为 guarantee。
- [ ] `cli-version-compatibility.md` 表中 v1.0 行 Notes 列从 `best-effort intent` 改为 `SemVer guarantee`。

## Enforcement 说明

- **v0.x 当前**：本 ADR 的约束是 **intent-level（Soft），不是 CI 门禁（非 Hard/Medium）**。
  符合 SemVer §4 精神（v0.x 定义上不做 API stability 承诺）且与 #1724 措辞一致。
  Soft 在此是**结构性正确的**（v0.x 本就不能承诺 API 稳定），而非本仓库 ai-robust 章程所禁止的
  「因懒惰而用 Soft」——后者禁止的是本可以做成 Medium/Hard 却选 Soft 的情形。
- **v1.0 GA 后**：升级为 Medium（CI 门禁），见 §D4 与 §v1.0 GA self-closure checklist。

## Out of scope

- Wire 契约（HTTP / event / command）：见 wire ADR `202605211200-adr-pre-v1.0-direct-v1-evolution.md`
  + `.claude/rules/gocell/api-versioning.md`。
- DB schema migration：已有独立治理（goose + DestructiveDownPermit GUC）。
- `examples/` 下的契约：demo 用途，不在 v1.0 GA 承诺范围（与 wire ADR 一致）。
- `internal/` 包：包外不可 import，无外部消费方，不走本 ADR 的承诺体系。

## Related

- Wire ADR: `docs/architecture/202605211200-adr-pre-v1.0-direct-v1-evolution.md`（轴 B，仅治 wire 契约）
- `.claude/rules/gocell/api-versioning.md`（Pre-GA wire 破坏窗口 2026-12-31，仅限轴 B）
- Issue #1724（v0.x 自用不做承诺，本 ADR 与其措辞调和）
- Issue #1081（Operator-SDK R9 外部消费方，v1.0 GA 承诺对象）
- Issue #1088（CLI versioned release + SemVer 承诺，本 ADR 归属 milestone）
- `docs/guides/cli-version-compatibility.md`（CLI ↔ framework 兼容矩阵）
- `docs/guides/cell-external-repo-quickstart.md`（外部仓库使用指南）
