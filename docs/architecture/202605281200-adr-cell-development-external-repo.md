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
    // LocatorConventional 使用 GoCell monorepo 约定的 5 种路径模式，
    // 无需 manifest 文件。
    LocatorConventional LocatorMode = iota

    // LocatorManifest 从 .gocell/manifest.yaml 读取 layout 配置，
    // 支持自定义路径模式与多模块 workspace。
    LocatorManifest
)
```

### 自动探测规则

`Locator.Detect(root fs.FS)` 按以下顺序确定模式：

1. 若 `<root>/.gocell/manifest.yaml` 存在 → `LocatorManifest`
2. 否则 → `LocatorConventional`

### CLI override hatch

`gocell validate` 与 `gocell check` 命令首批接入 `--layout=conventional|manifest`
flag，显式 override 自动探测结果。其余 12 个 CLI surface（scaffold / generate /
verify 等）通过 `Locator.Detect` 自动工作，`--layout` flag 在后续 PR 跟
backlog issue 补齐。

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

### Locator struct 主要 API

```go
// kernel/metadata/locator.go

// Locator 发现元数据文件路径，屏蔽 conventional / manifest 两种 layout 差异。
// 零值无效；通过 NewLocator 或 DetectLocator 构造。
type Locator struct {
    mode     LocatorMode
    manifest *manifestConfig  // non-nil iff mode == LocatorManifest
    root     fs.FS
}

// DetectLocator 从 root 自动探测模式，可被 --layout flag override。
// override 为空字符串时走自动探测。
func DetectLocator(root fs.FS, override string) (*Locator, error)

// Paths 返回指定类型的所有元数据文件相对路径列表。
// kind 取值：KindCell / KindSlice / KindContract / KindJourney /
//           KindAssembly / KindActors / KindStatusBoard
func (l *Locator) Paths(kind MetadataKind) ([]string, error)

// RootDir 返回用于将相对路径转换为绝对路径的根目录。
func (l *Locator) RootDir() string
```

`Paths` 的实现路径：

- `LocatorConventional` → `l.discoverConventional(kind)` — 复用现有 5 种
  路径模式（硬编码在 locator.go 内，parser.go 中的 path match 函数迁移至此）
- `LocatorManifest` → `l.discoverManifest(kind)` — 按 manifest.yaml 中各
  module 的 `includes`/`excludes` 走 `fs.WalkDir`

### parser.go 迁移策略

现有 `kernel/metadata/parser.go` 中的 `cellDirFromPath` /
`sliceDirsFromPath` / `contractDirFromPath` / `journeyIDFromPath` /
`matchAssemblyYAML` 五个函数**保留原签名不变**，由 `Locator.discoverConventional`
内部复用（不删除，不暴露为 public API）。M1 PR 的变更范围：

1. 新增 `kernel/metadata/locator.go`（Locator struct + DetectLocator）
2. 新增 `kernel/metadata/manifest.go`（manifestConfig 解析）
3. parser.go 顶层入口 `ParseDir` 接受可选 `*Locator` 参数：`nil` 时走
   现有逻辑（向后兼容 monorepo 调用方），非 `nil` 时委托 `Locator.Paths`。
4. CLI `validate` / `check` 命令接入 `--layout` flag 并构造 `Locator`。

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
§"Funnel 双向锁评级"。

### 下游 Hard

archtest `METADATA-LOCATOR-PATH-COMPARE-01`，扫描范围
`kernel/metadata/**` + `kernel/governance/**`：

- **A2 form-uniqueness**：`HasPrefix(path, "<literal>")` / `Split-then-index-compare`
  / `path.Dir-compare` / `regex anchor` 等路径前缀比较形态的 callsite 必须
  ⊆ `kernel/metadata/locator*.go`。其他文件出现等价的路径比较形态即 CI 红。
- **盲区**：`strings.Contains` 形态未被 HasPrefix 形态锁覆盖 → 反向自检测试断言
  `strings.Contains(path, "cells/")` 等形态在 `kernel/governance/targets.go`
  不出现。

### 上游 Medium

archtest `METADATA-LOCATOR-PATH-COMPARE-01`，A1 caller-allowlist：

- `fs.WalkDir` / `filepath.Walk` / `fs.ReadDir` 的调用方 ⊆
  `{Locator.discoverConventional, Locator.discoverManifest}`（kernel/metadata/
  内部可见）。

**为何不开升级 issue**：上游 Hard 的唯一可行路径是 cross-package sealed interface
+ private constructor，在 Go 语言下对于"谁可以调用 fs.WalkDir"这一问题无法表达——
`fs.WalkDir` 是标准库 public function，任何包均可调用，不存在 Go 类型系统层面的封堵。
这是与 `SPAN-SETATTR-HOLDER-SEAL-01` (#851) 同范式的 Go 结构性不可达情形。开升级
issue 等于承诺 Go 不可能完成的任务，故不开（对比 ai-robust.md §Funnel 双向锁评级：
"Medium 上游 + Hard 下游的过渡形态，**必须同步开 gh issue 跟踪显式 Hard 化任务**"
的条件是存在低成本 Hard 化路径——此处不满足）。

此（Medium 上游 + Hard 下游）是 ai-robust §"Funnel 双向锁评级" 明文允许的合法
final 形态，不构成降级。

---

## 威胁矩阵 / 边界条件

| 场景 | 风险 | 处置 |
|------|------|------|
| Monorepo 误植 `.gocell/manifest.yaml` | 切到 manifest 模式，可能解析错误路径 | `--layout=conventional` flag 显式 override；governance 检查双模式声明冲突 |
| manifest `path` 含 `../` 越界 | 路径逃逸至 repo 边界外 | `Locator` 在解析期拒绝绝对路径与含 `..` 段的相对路径，fail-fast，返回 `ErrUnsafePath` |
| manifest 漏写 `excludes: ["generated/**"]` | `generated/` 下的 contract YAML 被重复解析，parser 报重复 ID | `excludes` 默认含 `generated/**`；用户显式写空时 warn |
| symlink 跨 module 引入循环 | `fs.WalkDir` 死循环 | `Locator.discoverManifest` 走 `os.DirFS`，`fs.WalkDir` 不追踪 symlinked dir（标准库行为：`DirEntry.Type()&ModeSymlink != 0` 时 skip） |
| Workspace 多模块 cell ID 冲突 | 同名 cell 被装配两次 | `parser.go` 现有 `duplicate cell ID` 校验保持不变，locator 不旁路此检查 |
| manifest `modules[*].path` 指向不存在目录 | WalkDir 报 `*PathError` | `DetectLocator` 在构造阶段对每个 `path` 调用 `fs.Stat` fail-fast，而非推迟到 `Paths()` 调用时 |
| go.work 中 use 的 module 不在 manifest modules 列表中 | manifest 遗漏子 module，相关 cell 不被扫描 | 本 ADR 不强制 go.work 与 manifest 对齐；M2 codegen module path 注入会在 codegen 阶段捕获不一致；long-term M11 指南补充手动校验步骤 |
| Workspace 多模块均声明 `actors` 字段 | workspace-level singleton 重复声明 | `Locator` 解析 manifest 时 fail-fast，返回 `ErrDuplicateWorkspaceSingleton`，明确指出重复的模块路径 |

---

## 测试 / 验证

**M1 PR 必须提供以下测试，缺失则 PR 阻塞：**

1. `kernel/metadata/locator_test.go`：`fstest.MapFS` 三组 fixture
   - `TestLocatorConventional`：monorepo 标准 layout，`DetectLocator` 返回
     `LocatorConventional`，`Paths(KindCell)` 与现有 parser 结果一致
   - `TestLocatorManifestSingle`：单模块外部 repo（`.gocell/manifest.yaml`
     存在），`Paths` 返回 manifest 配置路径下的文件
   - `TestLocatorManifestWorkspace`：两个 module path 的 workspace，
     `Paths(KindCell)` 返回两个模块合并后的 cell 列表

2. `kernel/metadata/parser_manifest_test.go`：manifest 模式下 `ParseDir`
   结果与同等 conventional layout 下结果等价（cell ID 集合相同）

3. `cmd/gocell/app/validate_test.go`：`--layout=conventional` flag 覆盖
   manifest 自动探测的用例（fake fs 注入）

4. **手测 e2e**（PR description 列出步骤）：在 `examples/ssobff` 根目录临时
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
- 关联 ADR（Medium 上游 + Hard 下游过渡形态论证范式）:
  `docs/architecture/202605271100-adr-probename-sealed-funnel.md` §AI-robust 评级
