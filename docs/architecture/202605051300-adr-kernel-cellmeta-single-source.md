# ADR: kernel/cell.CellMetadata 物理合一到 kernel/metadata.CellMeta

> Status: Accepted
> Date: 2026-05-05
> ref: `docs/plans/202605011500-029-master-roadmap.md` Lane K #05（PR-V1-CODEGEN-MARKER-MIGRATE）拆分后的 PR-A1
> Implementation: PR-A1 KERNEL-CELLMETA-SINGLE-SOURCE（refactor/509-kernel-cellmeta-single-source）

## Context

K#05 markergen 范围审查发现 cell metadata 在 kernel/ 内部存在**类型层双源公理违例**：

`kernel/cell.CellMetadata`（kernel/cell/interfaces.go:23）与 `kernel/metadata.CellMeta`（kernel/metadata/types.go:19）两个类型并存且**字段已漂移 5 项**：

> 表分两节：第 1-2 行是字段存在性差异；第 6 行是同名字段的类型差异（Type）。

| 字段 | metadata.CellMeta | cell.CellMetadata |
|---|---|---|
| ID / Type / ConsistencyLevel / Owner / Schema / Verify / L0Dependencies | ✅ | ✅ |
| DurabilityMode | ✅ | ❌ |
| Listeners | ✅ | ❌ |
| GoStructName（K#04 codegen 用） | ✅ | ❌ |
| Dir / File（parser 注入） | ✅ | ❌ |
| Type 类型 | string（yaml-friendly） | cell.CellType enum |

K#04 PR-1（PR#360）加 GoStructName 时只同步到 `metadata.CellMeta`，没同步到 `cell.CellMetadata`——典型的「双源恶化」案例。K#05 markergen 即将再加一批字段（marker-derived listeners/routeMounts/subscribes），如不消除双源、漂移成本会进一步爆炸。

伴随 5 个并行衍生类型：`cell.{Owner,SchemaConfig,CellVerify,L0Dep}` 与 `metadata.{OwnerMeta,SchemaMeta,CellVerifyMeta,L0DepMeta}` 一一对应。

## Decision

### 1. 物理合一：删除 `kernel/cell` 内 5 个并行类型

`kernel/cell` 不再定义 cell-level metadata 类型。`kernel/metadata` 是唯一真相源。

```go
// 已删除（不留 typealias、不留 deprecation 注释）：
//   kernel/cell.CellMetadata
//   kernel/cell.Owner
//   kernel/cell.SchemaConfig
//   kernel/cell.CellVerify
//   kernel/cell.L0Dep
```

由 `tools/archtest/cellmeta_single_source_test.go` 的 `CELLMETA-SINGLE-SOURCE-01` 静态守卫该决定（kernel/cell 包内任一 TypeSpec 命中 5 类型名 → fail）。

### 2. BaseCell 持 `*metadata.CellMeta` 单一指针

```go
// kernel/cell/base.go (after)
type BaseCell struct {
    meta     *metadata.CellMeta
    cellType CellType  // cached: parsed at construction
    level    Level     // cached: parsed at construction
    // ... lifecycle / health fields
}
```

构造期 `deepCopyMeta(src)` 克隆 slice / nested struct，BaseCell 持独立 snapshot——caller 后续 mutation 不污染运行时 cell。`Type()` / `ConsistencyLevel()` 返回缓存的 typed enum，accessor 零分配。

### 3. NewBaseCell 改 error-first + 提供 MustNewBaseCell wrapper

```go
NewBaseCell(*metadata.CellMeta) (*BaseCell, error)    // canonical
MustNewBaseCell(*metadata.CellMeta) *BaseCell         // panic-on-error twin
```

NewBaseCell 在三种情况返回 `errcode.ErrValidationFailed`：
- nil meta
- meta.Type 非空但不是 `core` / `edge` / `support`
- meta.ConsistencyLevel 非空但不是 `L0`-`L4`

空字符串接受为 zero value（保持现有测试构造惯例）。

所有 27 个 caller（5 production cells + 22 test files）使用 `MustNewBaseCell`——每处都是静态字面量，构造失败 = programmer bug。该模式遵循 ADR `202604270030-architectural-panic-whitelist.md` §1（error-first + Must wrapper）+ §5（Must* 前缀自动豁免 PANIC-REGISTERED-01）。

由 `CELLMETA-SINGLE-SOURCE-02` 静态守卫 NewBaseCell 接收单一 `*metadata.CellMeta` 参数。

### 4. Cell 接口 Metadata() 返回 `*metadata.CellMeta`

```go
// kernel/cell/interfaces.go (after)
type Cell interface {
    // ...
    Metadata() *metadata.CellMeta  // 指针；指向独立 deep copy；caller 可读可改不污染 cell 内部
}
```

返回指针避免大 struct 拷贝；每次调用返回独立 deep copy，caller mutation 不影响 cell 内部状态。由 `CELLMETA-SINGLE-SOURCE-03` 守卫。

## Rationale

### 为什么物理合一而不是 spec 子包拆分

备选方案 C（kernel/metadata/spec/ 子包，把 yaml-free 的 type 移过去）可以让 kernel/cell 不引 yaml.v3 transitive 依赖，但代价：
- 全仓 ~10 个 metadata.X consumer 改 import 路径
- ProjectMeta.fileNodes 字段（持 *yaml.Node）必须从 ProjectMeta 移到 parser 内部
- 工时 +8h（24h 而非 16h）

**接受的代价**（物理合一方案）：
- `kernel/cell` 通过 transitive 依赖 yaml.v3（编译期 import graph 多一条边）
- `go list -deps ./kernel/cell` 输出会包含 `gopkg.in/yaml.v3`
- 哲学上：kernel/cell（运行时核心）现在与 yaml 解析概念关联

**为什么可接受**：
1. CLAUDE.md 明确允许 kernel/* 依赖 yaml.v3（kernel/metadata 已是先例）
2. 运行时二进制：BaseCell 代码不调用任何 yaml.* API，Go linker dead-code elimination 后 yaml.v3 函数不进 binary
3. kernel/cell 已 import kernel/outbox / kernel/ctxkeys 等，不是「独立运行时核心」（依赖图早已不平凡）
4. 工时差距明显（A: +18h 完成 vs C: +24h），架构纯粹度的 +6h 投入边际收益低
5. 未来若真有「kernel/cell 必须独立编译且不引 yaml」的需求，可独立 PR 做 spec 子包拆分（增量重构，不阻碍当前方案）

### 为什么 enum 转换内置 BaseCell 而不是放 metadata 包

`metadata.CellMeta.Type / ConsistencyLevel` 是 yaml-friendly 字符串（`"core"` / `"L2"`）。运行时调用方需要 typed enum（`CellType` / `Level`）。三个备选位置：

1. **metadata 包内置 typed view**：与 yaml 层职责混杂；引入 cell.CellType import 反向依赖
2. **每次 Type() 调用 ParseCellType**：accessor 不再零分配；hot path 浪费
3. **BaseCell 构造期一次性 parse + 缓存**（采纳）：accessor 零成本；invalid string 构造期 fail-fast；metadata 层保持纯 yaml view

### 为什么 NewBaseCell 接受空字符串 fallback 到 zero value

许多测试用 `MustNewBaseCell(&metadata.CellMeta{ID: "foo"})` 简化构造，依赖 zero-value Type/ConsistencyLevel 通过。强制非空字段会破坏 ~50 处测试构造，且 zero value 在 governance 链条（gocell validate 校验 cell.yaml）已被拦截，构造期 strict 是冗余。

### 为什么 Metadata() 返回独立 deep copy

CellMeta 含 ~12 字段（slice + nested struct），值返回每次复制 ~200 字节。先前方案是返回指针 + read-only 契约，但 6 角色 review 指出该契约无机制保障——caller 可静默修改 `b.meta` 字段，破坏 cellType / level 缓存与 meta 字段的一致性。最终采纳 deep copy fail-closed：每次调用 `Metadata()` 返回独立 clone（`metadata.CellMeta.Clone()`），分配开销可忽略（cell.Metadata() 不在 hot path——主要消费方是 catalog 导出 / debug log / governance 校验）。

### Trade-offs

- **API 表面增长**：新增 1 个 `MustNewBaseCell` wrapper（与 `MustNewAuthJWT` 等其他 ~12 个 Must* 形态一致）
- **caller 改动量**：~95 处 `cell.NewBaseCell(cell.CellMetadata{...})` → `cell.MustNewBaseCell(&metadata.CellMeta{...})`，可机器化批改（W3 sub-agent 完成）
- **测试 mutation 风险**：BaseCell.Clone 防护构造期 mutation 污染；`Metadata()` 返回独立 deep copy（每次调用一次 ~200 字节分配），caller 任意 mutation 不影响 cell 状态。fail-closed 替代不可强制的 read-only 契约。
- **一次性突破**：所有 caller 一次改完，无 deprecation 周期、无 typealias 兼容层

## Alternatives considered

### A. typealias 软合并（rejected）

```go
// kernel/cell/interfaces.go
type CellMetadata = metadata.CellMeta
type Owner = metadata.OwnerMeta
// ...
```

不向后兼容原则禁止——typealias 留下「kernel/cell 仍是 metadata 类型的另一入口」假象，archtest 也无法静态守卫单一源（typealias 在 ast 层与原类型同名）。

### B. kernel/metadata/spec 子包拆分（deferred won't-do）

技术可行但工时翻倍（+8h 包拆分 + 全仓 import 路径切换），架构纯粹度的边际收益低于对应工时。永久 won't-do until「kernel/cell 必须独立编译且不依赖 yaml」的真实需求出现。

### C. kernel/cell.Cell.Metadata() 返回 value（rejected）

每次调用复制 ~200 字节 CellMeta；catalog 导出 / governance 校验高频读取场景不优雅。指针 + read-only 契约更简洁。

## Roadmap

PR-A1 是 K#05 PR-V1-CODEGEN-MARKER-MIGRATE 拆分后的第一步（3 PR 串行）：

- **PR-A1（本 ADR）**：类型层双源消除（kernel/cell.CellMetadata → metadata.CellMeta）
- **PR-A2**（follow-up）：内容层双源消除（cell.go literal 删除，cell_gen.go loadCellMetadata() helper 接管，4 cell K#04 opt-in）
- **PR-B**（K#05 主体）：wire 层双源消除（markergen 框架 + 5 cell marker 化 + cell.yaml 删 wire 字段 + catalog augment）

详见 backlog 条目 `PR-A2-CELLGO-LITERAL-ELIMINATE` / `PR-B-CODEGEN-MARKER-MIGRATE`。

## References

- 父 plan: `docs/plans/202605011500-029-master-roadmap.md` Lane K #05
- 同源 ADR：`docs/architecture/202604270030-architectural-panic-whitelist.md` ERROR-FIRST-API-01（NewBaseCell 走 error-first + MustNewBaseCell wrapper 的政策来源）
- BaseCell deep-copy 范式：`kernel/registry/cell.go:70` deepCopyCell
- 实施 worktree: `worktrees/509-kernel-cellmeta-single-source`
- archtest gates: `tools/archtest/cellmeta_single_source_test.go` (CELLMETA-SINGLE-SOURCE-01..03)
