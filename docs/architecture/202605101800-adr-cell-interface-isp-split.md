# ADR: Cell Interface ISP Split + Raw-Infra Closure

> Status: Accepted
> Date: 2026-05-10
> ref: docs/plans/202605011500-029-master-roadmap.md #13 PR-A22
> Implementation: PR-V1-CELL-IFACE-ISP-SPLIT（吸收 PR245-F10 + 030 G-17）

## Context

`kernel/cell/interfaces.go` 的 `Cell` 接口在物理合一（K#04/K#05 PR#363/PR#365）后保留了 12 个方法，混合了四类正交职责：

- 静态身份（3）：`ID` / `Type` / `ConsistencyLevel`
- 状态机驱动（3）：`Init` / `Start` / `Stop`
- 运行时探针（2）：`Health` / `Ready`
- 声明态读取（4）：`Metadata` / `OwnedSlices` / `ProducedContracts` / `ConsumedContracts`

各类的消费者群截然不同：路由归属与 metrics label 只读 identity；assembly 编排只调 lifecycle；`/healthz`/`/readyz` 只读 status；contract validator 与 codegen 只读 inventory。聚合到单接口违反 ISP，且让 AI 实施者「拿到 `Cell` 就用 `Cell` 全集」，破坏最小依赖原则。

平行问题：`OUTBOX-CELL-01` 仅禁 platform `cells/*/cell.go` 暴露 `WithPublisher` / `WithOutboxWriter`，但对 `WithTxManager(persistence.TxRunner)` 等其他 raw infra 类型无守护，且整套规则不覆盖 `examples/*/cells/*/cell.go`。已确认 `examples/todoorder/cells/ordercell.WithOutboxWriter` 与 `examples/iotdevice/cells/devicecell.WithPublisher` 在审查时仍暴露 raw infra——AI 抄 example 写新 cell 会复制这一漏洞。

K#04/K#05 已让 `*metadata.CellMeta` 成为数据层单源，但接口形态层未跟进切分；`CELLMETA-SINGLE-SOURCE-03` 还在守顶层 `Cell.Metadata()`。

## Decisions

### D1. Cell 接口按 ISP 切分四子接口

`Cell` 拆为四个正交子接口，命名采用领域名词前缀 + 不加 `-er` 后缀（对齐 K8s `apimachinery/pkg/apis/meta/v1` 的 `Object` / `Type` / `Common` 系列命名）：

| 子接口 | 方法 | 消费者 |
|---|---|---|
| `CellIdentity` | `ID` / `Type` / `ConsistencyLevel` | registry lookup / metrics labels / log correlation / route attribution |
| `CellLifecycle` | `Init` / `Start` / `Stop` | `kernel/cell.Assembly` / bootstrap phases / lifecycle test harnesses |
| `CellStatus` | `Health` / `Ready` | `/healthz` `/readyz` HTTP handlers / runtime supervision |
| `CellInventory` | `Metadata` / `OwnedSlices` / `ProducedContracts` / `ConsumedContracts` | contract validators / metadata inspectors / `gocell validate` / codegen |

每个子接口 godoc 末段写明 `Consumers: <谁>`，引导 AI 实施时按消费者群声明最小子接口依赖（Soft 引导 + Medium archtest 联合，主线由四段式 compile-time check Hard 锁定）。

### D2. Cell 复合形保留（io.ReadWriter 范式）

`Cell` 重定义为 `interface { CellIdentity; CellLifecycle; CellStatus; CellInventory }`。复合接口本身是 ISP 工具，不是兼容 shim：

- 调用方零代码变更（`kernel/assembly` 9 处 `cell.Cell` 引用 / `runtime/bootstrap/*_test.go` / `cells/*/cell_gen.go` 全部继续工作）
- 下游接收 Cell 无需枚举四子接口
- BaseCell 实现完全无需修改（已实现 12 方法，复合 ≡ 全集）
- 命名学习 `io.ReadWriter = interface { Reader; Writer }` 与 K8s `apimachinery` `Object` 嵌入复合范式

### D3. BaseCell compile-time check 四段式分写

`kernel/cell/base.go` 删除单条 `var _ Cell = (*BaseCell)(nil)`，替换为四行独立断言：

```go
var (
    _ CellIdentity  = (*BaseCell)(nil)
    _ CellLifecycle = (*BaseCell)(nil)
    _ CellStatus    = (*BaseCell)(nil)
    _ CellInventory = (*BaseCell)(nil)
    _ Slice         = (*BaseSlice)(nil)
    _ Contract      = (*BaseContract)(nil)
)
```

缺方法时编译错误精确定位到子接口；单条 `_ Cell` 的 12-方法笼统报错被替换为 3 / 3 / 2 / 4 方法粒度的精确定位。复合 `Cell` 由四子接口同时满足时自动满足，无需冗余声明。

### D4. Slice 接口默认不拆 + 触发条件登记 backlog

`Slice` 7 方法（`ID` / `BelongsToCell` / `ConsistencyLevel` / `Init` / `Verify` / `AllowedFiles` / `AffectedJourneys`）形态与 Cell 类似，但本 PR **默认不拆**：

- cells/* 全部嵌 `BaseSlice`，实现疲劳为 0；无第三方 Slice 实现
- Slice 字段为简单值，无 `metadata.SliceMeta` 单源化数据层驱动（与 `metadata.CellMeta` 不对称）
- 激进自审「避免预设未来需求」原则：拆 Slice 只有形态对称收益，无 ISP 实际收益、无单事实源加强

触发条件 = 首次出现需替换 `BaseSlice` 7 方法之一的第三方 Slice 实现。届时按本 ADR 同精神拆分（SliceIdentity / SliceLifecycle / SliceMetadata），登记到 `docs/backlog.md cap-14` 同槽位（Source: ADR 202605101800 §D4）。

**消费者侧 ISP 收益评估**：当前 in-tree Slice 消费者枚举：
- `kernel/governance` Validator → Verify / AllowedFiles / AffectedJourneys（3 方法）
- `kernel/assembly` → Init（1 方法）
- `BaseCell.OwnedSlices()` → ID / BelongsToCell（2 方法）

每个消费者声明的 sub-interface 与 Slice 全集（7 方法）几乎等同（差距 ≤ 4），ISP 收益接近 0。Slice 不拆基于消费者侧 ISP 评估同样成立——不止"无第三方实现"。

### D5. CELLMETA-SINGLE-SOURCE-03 升级到子接口

`tools/archtest/cellmeta_single_source_test.go::TestCellmetaSingleSource03_MetadataInterfaceReturn` 原扫顶层 `Cell.Metadata()`，PR-A22 后 `Metadata()` 落入 `CellInventory` 子接口，gate 同步升级（AST `ts.Name.Name == "CellInventory"`）。配套 `cellmeta_single_source_test.go` 顶部 `Known limits` 删除已落地的"嵌入子接口可绕过"条目。

注：`CELLMETA-SINGLE-SOURCE-01` forbidden 列表保留 `CellMetadata` 旧名（防止历史 struct 复活），新接口选用 `CellInventory` 而非 `CellMetadata`，与该护栏不冲突——本决议同时解决了名字层面的"接口与 struct 同名混淆"。

### D6. CellInventory 接口名命名约定

`CellInventory` 选名理由：

- **避开 SOURCE-01 forbidden 历史 struct 名 `CellMetadata`**——这是命名层冲突的现实约束
- **方法集语义匹配**：`Metadata()` + `OwnedSlices()` + `ProducedContracts()` + `ConsumedContracts()` 共同构成 cell 的 declarative inventory，而不是单一 metadata accessor
- **与 `metadata.CellMeta` 数据层关系**：`CellInventory.Metadata() returns *metadata.CellMeta`——接口承载读路径，类型名不必同名（消费者通过 `cell.CellInventory` 与 `metadata.CellMeta` 前缀清晰区分）

cell 包内 `CellInventory`（type）与 `metadata` 包名（imported）共存无歧义；不引入别名，不重命名包。

**验证新接口名是否与历史 forbidden 集冲突**：
```
grep -n "forbidden" tools/archtest/cellmeta_single_source_test.go
```
查 `forbidden` map 的 5 个历史 struct 名（CellMetadata / Owner / SchemaConfig / CellVerify / L0Dep）。新接口名不得与该列表重叠。

## Consequences

### 正向
- ✅ ISP 严格满足，AI 实施按消费者声明子接口依赖
- ✅ `kernel/cell.Cell` 接口的 consumers 零代码变更（复合接口形态保留，BaseCell 实现 12 方法不动；`kernel/assembly` 9 处 `cell.Cell` 引用 / `runtime/bootstrap/*_test.go` / `cells/*/cell_gen.go` 全部继续工作）
- ✅ examples raw infra 漏洞堵住（AI-HARD ↑：AI 抄 example 路径锁住）
- ✅ `ordercell.resolveOutboxDeps` 私有逻辑（~30 行）删除，统一走 `cell.ResolveCellEmitter`，与 platform 三 cell 的 emitter 解析路径完全对齐
- ✅ `CELLMETA-SINGLE-SOURCE-03` 升级使 SOURCE-01..03 三 gate 共同守 metadata 单源化的接口形态层

### 范围澄清

「调用方零代码变更」**仅适用于 `kernel/cell.Cell` 接口的 consumers**（kernel/assembly / runtime/bootstrap / cells/*/cell_gen.go 等）。本 PR 同步改造 example cell 的注入接口，所有调用点（`examples/{todoorder,iotdevice}/main.go` + 各 cell `*_test.go`）在本 PR 内已更新：

- `ordercell`：`WithOutboxDeps(nil, w)` → `WithOutboxWriter(w)`（删除无用 pub 参数）
- `devicecell`：`WithOutboxDeps(eb, nil)` → `WithDirectPublisher(eb)`（删除无用 writer 参数）

项目无外部消费方（CLAUDE.md 「Review 和重构时不考虑向后兼容」），无 source-compat 责任。

**R1 round-2 升级（type-aware + cell-specific Options）**：删除了 `MustHaveNilOrderCellPublisher` 和 `MustHaveNilDeviceCellWriter` 两个运行时 panic guard——这些 guard 是在 `WithOutboxDeps` 统一签名下的补丁，archtest `CELL-RAW-DEPS-01` 升级为 type-aware 后通过三元组 allowlist 静态强制，运行时 panic guard 不再需要：

- ordercell 的 `WithOutboxWriter(w outbox.Writer)` 签名在类型层面不接受 Publisher，消除了「接受 pub 参数但 panic 掉」的矛盾
- devicecell 的 `WithDirectPublisher(p outbox.Publisher)` 签名不接受 Writer，同上

CELL-RAW-DEPS-01 archtest 从字符串比对（ai-collab.md §L5，实测 Soft）升级到 canonical type path（§L4，Hard 级）。

### 负向 / 风险
- ⚠️ 测试桩需声明四子接口而非单 `Cell`——探索阶段确认无现存子接口 mock，本 PR 无此类破坏
- ⚠️ 后续如需 `CellIdentity` 加字段（如 `Tier`），需新 ADR 决定加 `CellIdentity` 还是新 `CellTier` 子接口
- ⚠️ Slice 接口 D4 决议「默认不拆」与 review 可能的「对称切分」诉求存在张力——以单事实源驱动而非形态对称为准，触发条件清晰可追

### AI-HARD 三档分级一览
- **Hard**：四段式 compile-time check（缺方法编译失败 = 违反不可表达）；allowlist SHA-256 hash guard（修改不可静默）；`TestCellIfaceISP00_MethodSetsHashGuard`（PR441-FU A12 改造为 source-driven 后是真 Hard）
- **Medium**：
  - `CELLMETA-SINGLE-SOURCE-03` 升级（AST 扫子接口）
  - `TestCellIfaceISP01_CellComposesFourSubInterfaces` — AST type-aware 守 Cell 复合接口必须嵌入 4 子接口（CELL-IFACE-ISP-COMPOSITE-01）
  - `TestCellIfaceISP02_SubInterfaceMethodSets` — AST 守每个子接口方法集与 ADR §D1 一致（METHODSETS-01）
  - `TestCellIfaceISP03_BaseCellFourSegmentCheck` — AST 守 base.go 必须有 4 段 `var _ X = (*BaseCell)(nil)`（BASECELL-CHECK-01）
- **Soft**（非 mandatory，仅作引导）：godoc `Consumers: <谁>` 段；本 ADR §D4 Slice 默认不拆决议（文本决策 + backlog 触发条件）

满足 CLAUDE.md `.claude/rules/gocell/ai-collab.md` 「新增约束 ≥ Medium 立项硬门槛」——所有新增 mandatory 约束均 ≥ Medium。

## References

- 前置 ADR `docs/architecture/202605051300-adr-kernel-cellmeta-single-source.md`（K#04/K#05 数据层单源）
- 同精神 ADR `docs/architecture/202605031900-adr-handler-vocabulary-collapse.md`（领域名词统一收敛）
- K8s `apimachinery/pkg/apis/meta/v1.Object` ISP 拆分 + 复合接口范式
- `io.ReadWriter` / `io.ReadCloser` 同文件嵌入复合范式
- Uber fx `lifecycle.go` / controller-runtime `Runnable` ISP 极致单方法
- `docs/backlog.md` cap-14 PR245-F10（吸收）
