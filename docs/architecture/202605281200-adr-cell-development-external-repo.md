# ADR: Operator-SDK + Workspace 双模式 Cell 开发

- Status: Accepted
- Date: 2026-05-28
- Tracks: gh issue #1081 (umbrella: Operator-SDK + Workspace 双模式 Cell 开发)
- M1 child: gh issue #1082 (metadata locator 抽象化)
- Implemented by: worktrees/073-metadata-locator (M1), 后续子 worktree 跟进 M2-M12

本 ADR merge 后，#1081 子 issue (M1-M12) 可进入 daily-planner wave。

---

## 背景 / Context

GoCell 当前只支持 monorepo 内 Cell 开发。三条贯穿性架构假设（A/B/C，见下节）使得
外部仓库或多仓库 Workspace 场景在工具链层面无法直接跑通。

### 现状问题清单

**R1 — archtest 不可外部 import**
`tools/archtest/` 为 `package archtest`，置于 `tools/` 目录下。外部 Cell
仓库无法 `go get github.com/ghbvf/gocell/tools/archtest` 并在自有测试中运行
等价的约束验证。

**R2 — codegen module path 硬编码**
`pathx.ContractIDToPackagePath`（`cmd/gocell/pathx/`）将 contract ID
映射到 `github.com/ghbvf/gocell/generated/contracts/...`，写死了 module
路径。外部 module 的 generated 包路径不同，codegen 生成产物路径错误。

**R3 — migration namespace 全局共享**
`adapters/postgres` 下的 migration runner 默认以单一全局 schema 版本命名空间
追踪已执行的 migration。多个外部 Cell module 各自带 migration 时会发生序号
冲突或相互覆盖。

**R4 — metadata parser 路径模式硬编码**
`kernel/metadata/parser.go` 内多个 path match 函数（`cellDirFromPath` /
`sliceDirsFromPath` / `contractDirFromPath` / `journeyIDFromPath` /
`matchAssemblyYAML`）以 `cells/` / `cells/*/slices/` / `contracts/` /
`journeys/` / `assemblies/` 五种固定前缀识别元数据文件。外部仓库可能使用不同
layout（比如 `src/cells/` 或多模块嵌套路径），parser 无法识别。

**R5 — governance targets 路径推导硬编码**
`kernel/governance/targets.go` 用 `strings.HasPrefix(f, "cells/")` /
`strings.HasPrefix(f, "contracts/")` 等路径前缀将文件路径反推为 contract ID
和 governance 目标。与 R4 同源，外部 layout 无法通过此推导。

**R6 — assembly.yaml 无 module: 字段**
`assemblies/*/assembly.yaml` 当前不支持声明 Cell 来自外部 module。装配器
无法将外部 Cell 纳入同一 assembly 进行物理打包。

**R7 — Composition Root 私有化在 cmd/**
`cmd/corebundle/` 是唯一可用的 wiring 入口；`CellModule` 接口与
`SharedDeps` 在 `cmd/` 包内，Go 包可见性规则下外部仓库不可 import。外部
Cell 没有合法路径注入到 corebundle 装配流程。

**R8 — Registry.Subscribe cellID 由 cellgen 注入**
当前 cellgen 从本地 cell.yaml 读取 cellID 并注入 `reg.Subscribe` 的第四位
置参数。外部仓库使用同一 cellgen 时，cellID 解析路径是否正确需要额外验证。

**R9 — gocell CLI 无 SemVer release**
外部 Cell 仓库通过 `go get github.com/ghbvf/gocell` 引入工具链，但 CLI
当前没有版本化发布约定，外部仓库无法 pin 到稳定版本。

**R10 — contract publisher / consumer 无跨 module 注册表**
当前 `EMIT-DECL-COVER-01` / `DEAD-CONTRACT-01` 等 invariant 只扫描本 repo
内的 cell 与 contract。跨 module 的 publisher / consumer 对齐靠人工维护。

**R11 — errcode 前缀无注册表**
`ERR_*` 前缀没有全局注册表。多个外部 Cell module 各自定义错误码时可能
前缀冲突，线上日志无法区分来源。

**R12 — 无外部 Cell 开发 onboarding 路径**
无 starter repo 模板，无独立仓库开发指南，外部团队入门成本高。

---

## 三条贯穿性架构假设

### A. 单一 module / 单一 go.mod

**证据：**
- `tools/archtest/module_root.go` 用 `go/build.Default.GOPATH` + 向父目录回溯
  找最近 `go.mod` 即停，假设只有一个 module root。
- `pathx.ContractIDToPackagePath` 将 contract ID 拼接为
  `github.com/ghbvf/gocell/generated/contracts/<id>` 路径，
  写死了 module 路径前缀。
- archtest suite 整体以 `go test ./tools/archtest/...` 启动，不支持在
  `go.work` 聚合场景下跨 module 扫描。

### B. filesystem topology = governance topology

**证据：**
- `kernel/metadata/parser.go:131-195`：`cellDirFromPath` /
  `sliceDirsFromPath` / `contractDirFromPath` / `journeyIDFromPath` /
  `matchAssemblyYAML` 五个函数以固定路径 segment 位置（`parts[0] == "cells"`
  等）识别元数据文件。
- `kernel/governance/targets.go:145-320`：`strings.HasPrefix(f, "cells/")` /
  `strings.HasPrefix(f, "contracts/")` / `strings.HasPrefix(f, "journeys/")` /
  `strings.HasPrefix(f, "assemblies/")` 等前缀判断直接从文件路径派生 governance
  目标与 contract ID。
- 两处均无 "layout 配置" 注入点，只有对路径字面量的 split-then-index-compare。

### C. Composition Root 私有化在 cmd/

**证据：**
- `cmd/corebundle/` 是唯一完整 wiring 入口，包含 `SharedDeps`（包含
  `clock.Real()` / postgres / redis / rabbitmq 等所有基建）和
  `CellModule` interface（cell 向 corebundle 暴露自身的协议）。
- 两者均在 `cmd/` 包，Go 规则下包外不可直接 import `cmd/` 的 unexported
  symbol；即便 exported，`cmd/corebundle` 位于 main-adjacent 包，外部
  module import 会引入传递依赖冲突。
- 没有 plugin discovery 机制（无 `plugin.Open` / gRPC-plugin / `fx.Module`
  外部注册），外部 Cell 无法在运行时注入。

---

## 决议 / Decision

GoCell 同时支持三种 Cell 开发模式，**不破坏当前 monorepo 工作流**：

### 模式一：Monorepo 模式（gocell 自身）

conventional layout（`cells/` / `contracts/` / `journeys/` / `assemblies/`）。
**无 manifest 文件**，所有工具链走现有路径推导逻辑（不变）。

### 模式二：Operator-SDK 模式

外部仓库自有 `go.mod`，通过 `go get github.com/ghbvf/gocell` 引入 gocell
作为库依赖。仓库根放 `.gocell/manifest.yaml` 声明 layout。独立 CI / release
pipeline，可 pin 到 gocell 版本标签（M7 提供）。

典型场景：`acme/payment-cell` 独立仓库，团队自维护 CI 并发布自己的版本。

### 模式三：Workspace 模式

`go.work` 聚合 gocell + 多个外部 Cell module，开发期联调。Workspace 根放
`.gocell/manifest.yaml`，通过 `modules:` 列表声明每个子模块路径。

典型场景：平台团队在本地同时开发 gocell 本体和 `acme/payment-cell`，
需要联调 contract 边界。

---

## M1 Locator 设计

> 本节是 M1（#1082）的完整设计。其余 M2-M12 只列规划，不在本 ADR 展开设计细节。

### 设计原则：单策略 struct，非 pluggable interface

参照 buf v2 `buf.yaml` modules schema、kubebuilder PROJECT config
`pkg/config/v3/config.go`、kustomize `api/types/kustomization.go`：
上述工具均使用 **单策略 + 文件探测** 而非 strategy interface。引入
`LocatorStrategy interface` 会把"选择哪种 layout"的决策点分散到调用方，
产生跨 CLI surface 的配置漂移风险。

`kernel/metadata.Locator` 是单一 struct，持有一个 `LocatorMode` 枚举字段，
不对外暴露 strategy 接口。

### LocatorMode 枚举

```go
// kernel/metadata/locator.go

// LocatorMode 表示 metadata 文件的发现模式。
type LocatorMode int

const (
    // LocatorAuto selects Conventional unless .gocell/manifest.yaml exists at
    // root, in which case Manifest mode is used. This is the default when
    // NewLocator/NewLocatorFS is called without WithLocatorMode.
    LocatorAuto LocatorMode = iota

    // LocatorConventional 使用 GoCell monorepo 约定的 5 种路径模式，
    // 无需 manifest 文件。
    LocatorConventional

    // LocatorManifest 从 .gocell/manifest.yaml 读取 layout 配置，
    // 支持自定义路径模式与多模块 workspace。
    LocatorManifest
)
```

### 自动探测规则

`NewLocator(root, opts...)` / `NewLocatorFS(fsys, opts...)` 在 `LocatorAuto` 模式下按以下顺序确定模式：

1. 若 `<root>/.gocell/manifest.yaml` 存在 → `LocatorManifest`
2. 否则 → `LocatorConventional`

`ParseLocatorMode(s string) (LocatorMode, error)` 将 CLI 字符串（`"auto"` / `"conventional"` / `"manifest"`）转换为 `LocatorMode`。

### CLI override hatch

`gocell validate` 与 `gocell check` 命令首批接入 `--layout=auto|conventional|manifest`
flag（默认空字符串等价于 `auto`），通过 `WithLocatorMode(mode)` 传入 `NewLocator`。
其余 CLI surface（scaffold / generate / verify 等）继承 `LocatorAuto` 行为——若
`.gocell/manifest.yaml` 存在则切 manifest 模式，否则走 conventional，
无需额外 `--layout` flag（`--layout` flag 在后续 PR 补齐）。

### manifest.yaml schema

位置：`<repo-root>/.gocell/manifest.yaml`（Operator-SDK 模式）或
`<go.work-dir>/.gocell/manifest.yaml`（Workspace 模式）。

```yaml
version: v1
modules:
  - path: .                          # required，相对于 manifest 文件所在目录
    includes:                        # optional；省略时使用 conventional 5 种模式
      cells: ["cells/*/cell.yaml"]
      slices: ["cells/*/slices/*/slice.yaml"]
      contracts: ["contracts/**/contract.yaml"]
      journeys: ["journeys/J-*.yaml"]
      assemblies: ["assemblies/*/assembly.yaml"]
      actors: "actors.yaml"          # workspace-level singleton
      statusBoard: "journeys/status-board.yaml"  # workspace-level singleton
    excludes: ["generated/**", "vendor/**"]
  - path: ./acme-payment-cell        # Workspace 模式：第二个 module
```

**单模块外部 repo**：`modules` 仅一项 `path: .`，省略 `includes` 时使用 conventional
5 种模式，`excludes` 默认含 `generated/**`。

**Workspace 多模块**：每项 `path` 指向 `go.work` 中 `use` 的子目录之一。

**`actors` / `statusBoard` 是 workspace-level singleton**：manifest 中多个
module 条目均填写时，`Locator` 在解析阶段 fail-fast，返回
`ErrDuplicateWorkspaceSingleton`。

### path.Match glob 语义

`includes` 字段使用 `fs.Glob` 语义（`path.Match` 规则）：

- `*` 匹配单段
- `**` 匹配零或多段（由 `Locator.discoverManifest` 走 `fs.WalkDir` 实现，
  不依赖 `filepath.Glob` 对 `**` 的未定义行为）
- `excludes` 在 `WalkDir` 回调中逐路径 check，命中则 skip

### Locator struct 主要 API（落地形态）

```go
// kernel/metadata/locator.go

// MetadataSource is one YAML file discovered by Locator.
// Path is forward-slash relative to the locator root.
// Kind classifies the bucket (SourceCell / SourceSlice / SourceContract /
//   SourceJourney / SourceAssembly / SourceActors / SourceStatusBoard).
// CellID is populated for SourceCell / SourceSlice when the locator can derive
//   it from layout. In manifest mode with non-conventional layout, CellID
//   stays empty — parser requires slice.yaml to declare belongsToCell.
type MetadataSource struct {
    Path   string
    Kind   SourceKind
    CellID string
}

// SourceKind constants:
//   SourceUnknown / SourceCell / SourceSlice / SourceContract /
//   SourceJourney / SourceAssembly / SourceActors / SourceStatusBoard

// NewLocator 从 on-disk root 构造 Locator（auto-detect mode by default）。
func NewLocator(root string, opts ...LocatorOption) (*Locator, error)

// NewLocatorFS 从任意 fs.FS 构造 Locator（测试 / embed.FS 场景）。
func NewLocatorFS(fsys fs.FS, opts ...LocatorOption) (*Locator, error)

// Discover 遍历 locator root，返回所有 MetadataSource，按 Path 排序。
func (l *Locator) Discover() ([]MetadataSource, error)

// Mode 返回解析后的 LocatorMode（auto-detect 后）。
func (l *Locator) Mode() LocatorMode

// Root 返回绑定的 on-disk root（NewLocatorFS 构造时为空串）。
func (l *Locator) Root() string

// FS 返回底层 fs.FS，供 parser 共享同一文件句柄。
func (l *Locator) FS() fs.FS
```

`Discover` 的实现路径：

- `LocatorConventional` → `l.discoverConventional()` — 5 种固定路径模式
  （硬编码在 `locator_conventional.go`，原 parser.go 中的 path match 函数迁移至此）
- `LocatorManifest` → `l.discoverManifest()` — 按 manifest.yaml 中各
  module 的 `includes`/`excludes` 走 `fs.WalkDir`

LocatorOption 函数：

- `WithLocatorMode(m LocatorMode)` — 覆盖自动探测，CI 用 `WithLocatorMode(LocatorConventional)` 锁定。
- `WithManifestPath(p string)` — 覆盖默认 manifest 路径（`.gocell/manifest.yaml`）。

### parser.go 迁移策略（落地形态）

`kernel/metadata.Parser` 是消费 Locator 的唯一入口：

```go
// NewParser 创建从给定根目录读取的 Parser。
// 可传 LocatorOption 覆盖自动探测（如 WithLocatorMode(LocatorManifest)）。
func NewParser(root string, opts ...LocatorOption) *Parser

// Parse 通过 NewLocator 构造 Locator，再 Discover + 逐 MetadataSource 解析。
func (p *Parser) Parse() (*ProjectMeta, error)

// ParseFS 从 fs.FS 解析（测试 / fstest.MapFS 场景）。
func (p *Parser) ParseFS(fsys fs.FS) (*ProjectMeta, error)
```

Parser 内部调用 `loc.Discover()` 获得 `[]MetadataSource`，再按 `MetadataSource.Kind` dispatch 到对应的 `parseCell` / `parseSlice` / `parseContract` 等方法。Parser 不直接调用 `fs.WalkDir` / `filepath.Walk`（由 LOCATOR-DISCOVERY-FUNNEL-01 守）。

M1 PR 的实际变更范围：

1. 新增 `kernel/metadata/locator.go`（Locator struct + LocatorMode + SourceKind + MetadataSource）
2. 新增 `kernel/metadata/locator_conventional.go`（discoverConventional）
3. 新增 `kernel/metadata/locator_manifest.go`（ManifestSpec + discoverManifest）
4. `kernel/metadata/parser.go` 重构为消费 `Locator.Discover()` 输出（不再直接走 fs）
5. CLI `validate` / `check` 命令接入 `--layout` / `--manifest` flag。

---

## M1-M12 工作路线

依赖顺序如下；括号内为 gh issue 号。

### Phase 1 基线 — 解锁 Operator-SDK 跑通 validate/build/test

| 里程碑 | issue | 解决问题 | 前置 |
|--------|-------|---------|------|
| M1 | #1082 | metadata locator 抽象化 | 解 R4 + R5 | — |
| M2 | #1083 | codegen module path 注入 | 解 R2 | — |
| M3 | #1084 | archtest 提升为可被外部 import 的 library | 解 R1 | — |
| M4 | #1085 | CellModule 接口公开化到 runtime/composition/ | 解 R7 | — |

M1-M4 无相互依赖，可并行推进。

### Phase 2 装配 — 外部 Cell 可被 corebundle 装配并运行

| 里程碑 | issue | 解决问题 | 前置 |
|--------|-------|---------|------|
| M5 | #1086 | assembly.yaml 增加 module: 字段 | 解 R6 | M1 + M2 |
| M6 | #1087 | Registry.Subscribe cellID builder API | 解 R8 | M4 |
| M12 | #1093 | bootstrap cell ID closed set 校验 | 解 R11 部分 | M5 |

### Phase 3 政策 — 可对外发布、可独立升级、有 onboarding 路径

| 里程碑 | issue | 解决问题 | 前置 |
|--------|-------|---------|------|
| M7 | #1088 | gocell CLI versioned release + SemVer 承诺 | 解 R9 | — |
| M8 | #1089 | migration namespace per module | 解 R3 | — |
| M11 | #1092 | starter repo + 独立仓库开发指南 | 解 R12 | M1-M6 |

### Phase 4 闭环 — 跨仓库治理

| 里程碑 | issue | 解决问题 | 前置 |
|--------|-------|---------|------|
| M9 | #1090 | contract publisher / consumer registry | 解 R10 | M2 + M5 |
| M10 | #1091 | errcode 前缀注册表 | 解 R11 partial | — |

---

## AI-robust 评级（locator funnel）

locator funnel 的两个方向分别评级，对齐 `.claude/rules/gocell/ai-robust.md`
§"Funnel 双向锁评级"。落地 archtest 是 `LOCATOR-DISCOVERY-FUNNEL-01`
（`tools/archtest/locator_discovery_funnel_test.go`），含 A1/A2a/A2b/A5 四条子规则；
符号清单活在该 archtest 的 package godoc，不在本 ADR 复制。

> 外部仓库在 M3 (#1084 archtest library-isation) 落地前不受 `LOCATOR-DISCOVERY-FUNNEL-01` archtest 守护，依赖文档约定。M3 PR-1 起 archtest 已可外部 import（见下 §"M3 archtest library-isation 落地形态"），但 `LOCATOR-DISCOVERY-FUNNEL-01` 本身尚未迁入外部规则集（仍在 76 条迁移 backlog 中，PR-2..N 收口）；在它迁入前，外部仓库通过 `RunStandardCellRules` 获得的是 `PANIC-REGISTERED-01` 等已迁移规则，locator funnel 仍依赖文档约定。

### ENTERING funnel（path-prefix 比较，A1/A2 轴）

| 方向 | 形态 | 评级 |
|------|------|------|
| 下游 Hard | A2a/A2b form-uniqueness：`strings.HasPrefix(_, "cells/")` 和 `x == "cells"` 等路径比较形态的 callsite 必须 ⊆ `kernel/metadata/locator*.go`，EvaluateConstString 解析 const 引用，盲区负向自检 | **Hard** |
| 上游 Medium | A1 caller-allowlist：`fs.WalkDir` / `filepath.Walk` / `fs.ReadDir` 的调用方 ⊆ `{discoverConventional, discoverManifest}`（archtest allowlist 守，Go 类型系统无法封堵标准库 public function 调用） | **Medium（Go 结构性上限）** |

**ENTERING 上游为何不开升级 issue**：上游 Hard 的唯一可行路径是 cross-package sealed
interface + private constructor，在 Go 语言下对于"谁可以调用 `fs.WalkDir`"无法用类型系统
表达——`fs.WalkDir` 是标准库 public function。这是与 `SPAN-SETATTR-HOLDER-SEAL-01`
(#851) 同范式的 Go 结构性不可达情形，不满足 ai-robust.md §"Funnel 双向锁评级"
"存在低成本 Hard 化路径"的开 issue 前提，故 **不开升级 issue**，Medium 是该形态的永久上限。

### EXITING funnel（consumer-path 重建，A5 轴）

| 方向 | 形态 | 评级 |
|------|------|------|
| 下游 Hard | A5 form-uniqueness：EvaluateConstString 解析 `filepath.Join` 每个 arg，命中 banned token ("cells", "cmd") 即 CI 红；consumer scope = kernel/governance + cmd/gocell + kernel/metadata（funnel 文件外） | **Hard** |
| 上游 Medium | archtest caller-allowlist：Go 类型系统无法阻止包内代码任意调用 `filepath.Join`；A5 盲区负向自检补充 | **Medium（Go 结构性上限）** |

**EXITING 上游为何不升 Hard（决策 2026-05-30，原 gh issue #1235 → won't-do）**：A5 ban 的语义目标是
`filepath.Join(_, "cells"/"cmd", …)` 中的**字面量** layout token。评估过两条 Hard 化候选——
**(A)** Locator sealed envelope（把 `MetadataSource.Path` / `CellMeta.File` 等 seal 成 unexported +
envelope method）与 **(B)** typed pathx 包（`RelPath` newtype 收口派生）——**均不能**让该字面量在
type system 层不可表达：字符串常量 `"cells"` 与标准库 public `filepath.Join` 在 Go 下永远可敲，
seal 派生输出只让"正确派生"更便捷（"no need to reconstruct"），无法表达"**不能** reconstruct"。
下游字面量 ban 仍只能靠 A5 archtest 兜，A/B 不增量提升上游可表达性。这与 ENTERING 轴
（`fs.WalkDir` 标准库 public func）完全同构，亦与 `SPAN-SETATTR-HOLDER-SEAL-01` (#851) /
`HEALTHZ-HOLDER-SEAL-01` (#893) 同范式的 Go 结构性不可达情形。A 方案另需改写 ~166 处 path 派生点
（93 `.File` read + 73 derive）/ 5 个 metadata 类型 + printers raw-path 透传 + 破坏 M-series
外部仓库公开 API（`CellMeta.File` 等是已暴露字段），换取 illusory Hard，工作量/收益严重失衡。
故 ai-robust.md §"Funnel 双向锁评级""存在低成本 Hard 化路径"前提**不成立**，按同 §"ENTERING 上游
为何不开升级 issue"同款判定，**不开/不留升级 issue**，Medium 是该形态的永久上限；gh issue #1235
关闭为 won't-do。本 amendment 不改 A5 archtest 行为，下游 Hard 不变，无任何 §安全模型覆盖格降级。

**path 单一真值源**：A5 funnel 的语义边界是 `MetadataSource.Path`（`Locator.Discover()` 输出）——
governance / CLI 代码必须从 `MetadataSource.Path` / `CellMeta.File` / `SliceMeta.File` 等
Locator 输出派生路径，不得通过 `filepath.Join("cells", id)` 等形式重建。这是 funnel
"EXITING 侧"的约束语义。

---

## M3 archtest library-isation 落地形态（Amendment 2026-05-30）

M3 (#1084) 解 R1：让外部 Cell 仓库 `go get github.com/ghbvf/gocell/tools/archtest`
后在自有 `go test` 中运行 gocell 的架构不变量。**探查纠正了 R1 原始前提**：
`tools/archtest/module_root.go` 并未硬编码 module path（经 `gomodutil.ReadModulePath`
从 go.mod 动态读取），`package archtest` 本就可被 `go get`。真正阻塞是
**~158 条规则全活在 `_test.go`**——Go 永不为依赖编译 `_test.go`，外部仓库拿不到；
且无可编程入口。

### 落地分层（拆 PR，全量迁移）

| 项 | 形态 | 落地 PR |
|----|------|---------|
| 可 import API（`external.go`） | `CellRule{ID,Run}` 描述符 + `ConfigForExternalCell{BuildTags,ExtraRules}` + `StandardCellRules() []*CellRule` + `RunStandardCellRules(t,cfg)` + `PlatformModulePath` const。每个 `ConfigForExternalCell` 字段都被一条已 ship 的规则读取（无 no-op 占位字段）；misconfigured rule（nil / nil Run / 空 ID）`t.Errorf` fail-fast 不静默跳过 | **PR-1** |
| 第一批迁移 exemplar | `PANIC-REGISTERED-01` 逻辑 MOVE 到 `panic_invariants.go`（非 test），platform 路径经 `PlatformModulePath` 派生，scan scope 由 driver 供给；`cfg.BuildTags` 供第二趟 build-tag 扫描（外部仓库传自己的 production tags，gocell dogfood 传 `FlatNonDefaultTags()`）；builtin-`panic` shadow 检测内联进规则（关闭纯 AST `isPanicCallExpr` 唯一残留盲区）；gocell `_test.go` dogfood 同一 `CheckPanicRegistered`（单源） | **PR-1** |
| 迁移收敛 ratchet | `ARCHTEST-MODULE-PATH-FUNNEL-01`（`module_path_funnel_test.go`）+ **frozen baseline**（`testdata/module_path_funnel.baseline`，PR-1 基线 **76 文件** backlog）。非 golden：`-update` 永不重写，monotone ceiling，新 offender ⊄ baseline 即 CI 红（无 `-update` 洗白路径） | **PR-1** |
| ratchet PR-time gate | `TestArchtestModulePathFunnel` 入 `hack/verify-archtest-invariants.sh`（bare-literal AST ratchet + #1304 起 typed const-eval `no-reconstruction` 子检查，~0.5s），fail-on-new 在 PR 时即时生效，不再仅 nightly | **PR-1** |
| 跨 module smoke | `external_smoke_test.go`：throwaway 临时 module `replace` 本仓 + import 真 `archtest` + 跑 `RunStandardCellRules`，断言捕获 consumer module 的 bare panic（锁住 import-as-dependency / 无 `-update` flag panic / `findModuleRoot` 解析 consumer go.mod） | **PR-1** |
| 剩余 ~76 规则族迁移 | errcode / span / saga / outbox / cell / layer-05..10 / `LOCATOR-DISCOVERY-FUNNEL-01` … 逐 PR 缩 baseline 至空 | **PR-2..N**（#1302） |
| 净新 `PROD-MAIN-WIRING-NOOP-REJECT-01`（reviewer P0 #3） | 扫 composition-root noop/in-memory wiring；落地时同 PR 把 `ProductionMainPkgs` 字段加回 `ConfigForExternalCell`（PR-1 删除该 no-op 占位字段，premise 到时再加） | #1303 |
| FreezingArchRule baseline（外部既有库增量采纳） | 通用 `BaselinePath` + CI 只读 + 修复自动收缩（ratchet 已先行落地 frozen-baseline 范式，本项是其通用化）。**仍 #1304 开放，demand-gated**——触发 = 真实 brownfield 外部 adopter；greenfield starter 不需，建 = dead plumbing | #1304 |
| ~~独立 module 抽取 `…/archtest`~~ | ~~go.work 多 module~~ → **转 #1561（go.work P6 tools 拆 module）接管，不在 #1304 范围** | #1561 |
| ratchet 上游 Hard 化 + 下游 residual 闭合 | **已落（#1304，本 PR）**：① 下游 typed const-eval 闭合 const-of-const/跨包 residual（**Hard**）；② 上游空-baseline（#1302 全量迁移已完成）= **Medium 纯-ban 终态**（permanent ceiling，非 Hard，见下评级修正） | #1304（done） |

### 核心不变式：platform-vs-scan 路径拆分

规则对 **GoCell 平台符号路径**（errcode / redaction / panicregister，外部仓库作依赖在固定
路径导入）的引用保持锚定 `PlatformModulePath`；规则的**扫描范围**（扫哪个 module 找违规）
由 driver（`Run(t, Typed(...))`→`findModuleRoot` 从运行 module 的 go.mod 解析）供给。二者对应
`go/analysis` 中 `printf` 硬编码 `"fmt.Printf"` / `copylock` 硬编码 `"sync"`（稳定依赖路径）
vs `pass.Pkg`（被分析目标）的标准分离。

### AI-robust 评级（ARCHTEST-MODULE-PATH-FUNNEL-01，funnel 双向锁）

| 方向 | 形态 | 评级 |
|------|------|------|
| 下游 Hard（bare literal） | bare `"github.com/ghbvf/gocell[/…]"` STRING 字面量经 AST BasicLit 前缀匹配检出（`firstBarePlatformLiteralLine`）；offenders 经 frozen baseline ratchet（空=纯 ban） | **Hard** |
| 下游 Hard（reconstruction，**#1304 已闭合**） | `+` 拼接经 **typed const-eval**（`EvaluateConstString` go/types 常量折叠）捕获——闭合此前 residual 的 **const-of-const + 跨包 const**；sanctioned 形态 `PlatformModulePath+"/x"` 及其同包**派生 const**（`const q = p+"/b"` 其中 `p = PlatformModulePath+"/a"`）经 **provenance 追溯**（const object identity = name+pkgpath，alias-proof / same-value-forge-proof）例外。test-variant typed load 已落地（`implements_funnel` 同范式）、object-identity 例外解决 sanctioned 误伤——原「受阻：规则活在 `_test.go`」前提**已失效** | **Hard** |
| 下游 residual（c）**permanent ceiling，非 Hard** | runtime string ops（`strings.Join`/`fmt.Sprintf`/`[]byte`）拼装的路径**非编译期常量** → go/types 折不动 → 检测器够不到。封堵需 SSA/dataflow，超 archtest 天花板；刻意混淆过不了 review。**permanent won't-do（不另开 issue）**，同 #851/#893/#1282/#1424 族 | **Medium（永久天花板）** |
| 上游 Medium（**permanent ceiling，非 Hard**） | Go 无法阻止包内写 bare 字面量；archtest 在 CI test 时捕获、非编译期。frozen baseline **现已空（#1302 全量迁移完成）= 纯 ban**：消除 exception 逃逸（`-update` 永不重写 baseline，新 bare 字面量 ⊄ baseline 即 CI 红，唯一容纳 = 手改 frozen baseline 的 review-first diff）。**空 baseline 是该规则形状能达到的最强上游形态，但仍 Medium**——「源码不写某字符串值」类型系统 Hard 不可达 | **Medium（永久天花板）** |

下游对所有编译期-常量重构（bare/字面量片段/同包 const/const-of-const/跨包 const）现为 **Hard**；
仅 runtime string ops (c) 为 permanent ceiling。上游 Medium 为 permanent Go 天花板（空 baseline 已达最强形态）。
**不再有「过渡」评级**——`#1304` 已闭合下游 residual，并修正「空 baseline = Hard 终态」的 overclaim
为「Medium 纯-ban 终态」。符号清单 + 盲区自检活在 `module_path_funnel_test.go` 的 godoc，不在本 ADR 复制。

> **Amendment 2026-05-30 round-3**：原文（round-2）要求「无 N≥3 字面量 fragment-split
> 作为每个 M3 迁移 PR 的人工 review checklist 条目」——**已撤销**。`no-fragment-split`
> 自检现 flatten 任意片段数的 `+` 链，operand 含同包字面量 const，N≥3 与 const-Ident 片段
> 拼接（如 `const a,b,c = …; a+b+c`）均机器捕获，不再是人工盲区。adversarial 残留
> （const-of-const / 跨包 const / runtime string ops）见下 Amendment 2026-06-07。

> **Amendment 2026-06-07（#1304 ③ 落地，逐行重评本节）**：检测器从 AST-only flatten
> 升级为 **typed const-eval**（`EvaluateConstString` go/types 常量折叠 + provenance 追溯
> 例外）。重评影响——
> - 上方「AI-robust 评级」表已**整体重写**（非追加）：原「下游残留 Medium（过渡）」行因
>   const-of-const + 跨包 const **已闭合**而拆为「下游 Hard（reconstruction）」+「下游
>   residual (c) permanent ceiling」两行；原「上游 Medium（过渡）/ Hard 终态 = baseline
>   清空」修正为「上游 Medium（permanent ceiling）」——**空 baseline 不是 Hard 终态**
>   （archtest CI-time 捕获，非编译期；类型系统 Hard 对「源码不写某字符串值」不可达）。
> - `no-fragment-split` 自检（AST `flattenPlatformConcat`/`collectStringLiteralConsts`/
>   `constMap`）被 typed `collectPlatformReconstructions` 取代（删，无双路径）。RED 自检迁
>   `internal/modulepathfunnelfixture`（const-of-const / 跨包各一 + sanctioned / unrelated /
>   runtime-ops GREEN）。
> - 子项表（上）：① 转 #1561、② demand-gated 续延、③ 本 PR done。
> - **无格子由 ✅ 降级**——本次全部是 residual 收紧（Medium 过渡 → Hard）+ overclaim 修正
>   （Hard 终态 → Medium permanent ceiling）。runtime-ops (c) 为已知 permanent ceiling，
>   非新降级。`ref:` TNG/ArchUnit FreezingArchRule（空 store = 纯 ban，亦 test-time）/
>   golang.org/x/tools go/analysis（typed Pass）。

### 开源对标（`ref:` 见 commit）

- `golang.org/x/tools/go/analysis`：`Pass{Fset,Files,Pkg,TypesInfo}` 同构；`Analyzer` 值 +
  `multichecker.Main([]*Analyzer)` slice（无 registry）+ `analysistest.Run(t,dir,a,patterns)`
  driver 供 target；printf/copylock 平台路径固定。
- ArchUnit：`rule.check(importedClasses)` 分离供给、`ArchTests.in(StandardRules.class)`
  预定义集、`FreezingArchRule`/ViolationStore 增量采纳、custom rule 同接口无 registry。
- arch-go：`config.Load(modulePath)`。

---

## 威胁矩阵 / 边界条件

| 场景 | 风险 | 处置 |
|------|------|------|
| Monorepo 误植 `.gocell/manifest.yaml` | 切到 manifest 模式，可能解析错误路径 | `--layout=conventional` flag 显式 override；governance 检查双模式声明冲突 |
| manifest `path` 含 `../` 越界 | 路径逃逸至 repo 边界外 | `Locator` 在解析期拒绝绝对路径与含 `..` 段的相对路径，fail-fast，返回 wrapped `fmt.Errorf` 包含 `"path escape not allowed"` 或 `"absolute path not allowed"` 提示，无独立 sentinel |
| manifest 漏写 `excludes: ["generated/**"]` | `generated/` 下的 contract YAML 被重复解析，parser 报重复 ID | `excludes` 默认含 `generated/**`；用户显式写空时 warn |
| symlink 引入循环 / 逃逸 repo 外（#1592） | `fs.WalkDir` 死循环 / 解析 repo 外 metadata | **Amendment 2026-06-05（#1592）**：原表述（`os.DirFS` + `fs.WalkDir` 不追踪 symlinked dir）是 false-secure——`os.DirFS` 跟随 symlink，`fs.WalkDir` 从一个位于 symlinked base 之下的 walk-root 会进入该 symlink，故一个指向 repo 外的 `modules[].path` symlink 会被 walk+read。已改为 `NewLocator` 经 `os.OpenRoot(root).FS()`（Go 1.24+ rooted fs）在 syscall 层 confine：任何经 symlink 逃逸 root 的路径被拒（`"path escapes from parent"`），覆盖 Stat/WalkDir/ReadFile 全路径；`fs.WalkDir` 的 `DirEntry.Type()&ModeSymlink` skip 保留为 in-root defense-in-depth。enforcement = `LOCATOR-ROOT-CONFINED-01` archtest（禁 kernel/metadata 调 os.DirFS）|
| Workspace 多模块 cell ID 冲突 | 同名 cell 被装配两次 | `parser.go` 现有 `duplicate cell ID` 校验保持不变，locator 不旁路此检查 |
| manifest `modules[*].path` 指向不存在目录 | WalkDir 报 `*PathError` | `NewLocator` / `NewLocatorFS` 在构造阶段对每个 `path` 调用 `fs.Stat` fail-fast，而非推迟到 `Discover()` 调用时 |
| go.work 中 use 的 module 不在 manifest modules 列表中 | manifest 遗漏子 module，相关 cell 不被扫描 | 本 ADR 不强制 go.work 与 manifest 对齐；M2 codegen module path 注入会在 codegen 阶段捕获不一致；long-term M11 指南补充手动校验步骤 |
| Workspace 多模块均声明 `actors` 字段 | workspace-level singleton 重复声明 | `Locator` 解析 manifest 时 fail-fast，返回 `ErrDuplicateWorkspaceSingleton`，明确指出重复的模块路径 |
| M3：外部仓库 import archtest 后触达 loader 原语绕过 Pass funnel | 外部代码 `packages.Load` + 手配 `*types.Info` 重建 INV-1 跨 load 配对 bug | **不削弱，但边界须精确**：对外部 import 方，唯一生效的 Hard 线是**防御 #1**——`Pass.Pkg` 是 `*types.Package`（非 `*packages.Package`，无 `.Syntax`），INV-1 跨 load 配对从 `Pass` 类型上不可重建；loader 原语在 `tools/archtest/internal/`（外部不可 import）。防御 #2（depguard 禁直接 import `packages`）与 #3（meta-archtest `PASS-FUNNEL-*`）是 **gocell 内部 `_test.go` 的自约束，对外部仓库不适用**——外部消费方可在自有 test 里 `import packages`，只要不从 `Pass.Pkg` 取 `.Syntax`（类型上不可达）即无 INV-1 向量。原"Hard 三线一致"表述高估边界，已更正 |
| M3：外部仓库的 `_test.go` 调 `RunStandardCellRules`，`findModuleRoot` 误解析到 gocell 依赖根 | 扫描目标错成 gocell 而非外部 module | `findModuleRoot` 从测试进程 cwd 向上回溯，外部 `go test` 的 cwd 在外部 module 内 → 命中外部 go.mod；gocell 依赖在 module cache（只读，非 cwd 祖先），不会被误命中 |
| M3：迁移 PR 改规则逻辑引入行为漂移（漏报违规） | 安全规则静默失效 | 每条迁移单源 dogfood（gocell `_test.go` 与外部走同一 `Check*`）+ Test* 名不变（ARCHTEST-VERIFY-COVERAGE-01 不漂移）+ RED fixture golden 锁违规检出 |
| M3：ratchet 新 offender 经 `-update` 洗白进 golden（fail-on-new 退化为 review 纪律） | 迁移期回归静默通过，golden 悄涨 | frozen baseline 取代 golden：`-update` 永不重写 baseline，`offenders ⊄ baseline` → CI 红，无洗白路径；唯一容纳方式是手改 frozen 文件（review-first 可见 diff）。`TestArchtestModulePathFunnel` 入 PR-time gate，回归 PR 时即红不待 nightly |
| M3：外部仓库 import `archtest` 触发包级 `-update` flag 重复注册 panic | 外部 `go test` 启动即 panic（`flag redefined: update`），根因不可推断 | `-update` flag 移入 `golden_harness_test.go`（`_test.go`）；Go 永不为依赖编译 `_test.go`（test binary 或否），外部 importer 不会注册该 flag——结构上消除，非约定 |
| M3：外部仓库生产 panic 藏在 `//go:build` tag 后，被 PANIC-REGISTERED 漏扫 | tag-gated 的非法 panic 逃逸安全规则 | `CheckPanicRegistered` 读 `cfg.BuildTags` 跑第二趟扫描；外部仓库传自己的 production tags，gocell dogfood 传 `FlatNonDefaultTags()`。默认趟恒扫，tag 趟覆盖 build-directive 文件 |
| M3：外部 `ExtraRule.Run` 调 `t.FailNow`/`t.Fatal` 中断 rule 循环，掩盖后续标准规则 | 一条外部规则致 FailNow → `runtime.Goexit` → 后续标准 gate 不执行，静默漏 gate | `ExtraRules` 信任边界写入 `ConfigForExternalCell` godoc：rule 必须返回 `[]Diagnostic`（经 `Report`→`t.Errorf`）不得自调 `t.Fatal/FailNow`。约定承载（外部 rule 代码不可强制），但 misconfigured rule（nil/nil Run/空 ID）已 fail-fast，非静默 |

---

## 测试 / 验证

**M1 PR 提供以下测试（落地状态）：**

1. `kernel/metadata/locator_test.go`：`fstest.MapFS` 三组 fixture
   - `TestLocator_ConventionalDiscover`：monorepo 标准 layout，`NewLocatorFS` 返回
     `LocatorConventional`，`Discover()` 中 `SourceCell` 条目与现有 parser 结果一致
   - `TestLocator_ManifestSingleModuleDefaults`：单模块外部 repo（`.gocell/manifest.yaml`
     存在），`Discover()` 返回 manifest 配置路径下的 MetadataSource 列表
   - `TestLocator_ManifestWorkspaceMultiModule`：两个 module path 的 workspace，
     `Discover()` 返回两个模块合并后的 SourceCell 列表

2. `kernel/metadata/locator_manifest_e2e_test.go`（Batch 4 补充）：os.TempDir
   manifest 模式 E2E；`NewLocator(root, WithLocatorMode(LocatorManifest)).Discover()`
   + `NewParser(root, WithLocatorMode(LocatorManifest)).Parse()` 整链验证

3. `cmd/gocell/app/helpers_test.go`（Batch 4 补充）：`buildLocatorOptions` 三种
   入参组合（空值/conventional/manifest/auto/invalid）+ `addLocatorFlags` 默认值解析

4. `cmd/gocell/app/validate_test.go`：`--layout=conventional` flag 覆盖
   manifest 自动探测的用例（fake fs 注入）

5. **手测 e2e**（PR description 列出步骤）：在 `examples/ssobff` 根目录临时
   写入 `.gocell/manifest.yaml`（单模块），跑 `gocell validate`，确认输出
   与不带 manifest 时等价。

---

## 参考 / References

- bufbuild/buf `buf.yaml` v2 modules schema:
  https://buf.build/docs/configuration/v2/buf-yaml/
- kubernetes-sigs/kubebuilder PROJECT external resources:
  `pkg/config/v3/config.go`
- kubernetes-sigs/kustomize Resources field:
  `api/types/kustomization.go`
- golang/go workspace `use` directive:
  `cmd/vendor/golang.org/x/mod/modfile/work.go`
- 关联 ADR（Hard funnel 双向锁参考范式）:
  `docs/architecture/202604242030-adr-kernel-wrapper-contract-observability.md` §8
- 关联 ADR（Medium 上游 + Hard 下游论证范式；A5 EXITING 轴已据此判定为永久上限 won't-do，见 §"EXITING 上游为何不升 Hard"）:
  `docs/architecture/202605271100-adr-probename-sealed-funnel.md` §AI-robust 评级
