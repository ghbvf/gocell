# Phase 0: 接口设计 + 基础骨架 (Days 1-7)

> 从完整实施计划中提取。Phase 0 是所有后续工作的基础，完成后才能进入 Phase 1 Kernel 核心。

## Gate

`app.Register(cell); app.Start(ctx)` 编译通过并运行。

## 关键约束

- `kernel/` 只依赖 stdlib + `pkg/` + `gopkg.in/yaml.v3`
- 错误用 `pkg/errcode`，日志用 `slog`
- 覆盖率 kernel ≥ 90%，table-driven test

---

## Step 0.1 — 基础包 `pkg/`（Day 1）

**并行开发**：errcode 和 ctxkeys 无依赖关系。

| 文件 | 内容 |
|------|------|
| `src/pkg/errcode/errcode.go` | `Code` 类型、`Error` struct（Code/Message/Details/Cause）、`New()`、`Wrap()` + 哨兵码（ERR_METADATA_INVALID, ERR_CELL_NOT_FOUND 等） |
| `src/pkg/errcode/errcode_test.go` | table-driven: New / Wrap / Error() / Unwrap |
| `src/pkg/ctxkeys/keys.go` | context key 常量（CellID, SliceID, CorrelationID, JourneyID, TraceID, SpanID）+ `WithX(ctx)` / `XFrom(ctx)` 辅助函数 |
| `src/pkg/ctxkeys/keys_test.go` | round-trip 测试 |

### errcode 接口设计

```go
package errcode

type Code string

type Error struct {
    Code    Code
    Message string
    Details map[string]any
    Cause   error
}

func (e *Error) Error() string
func (e *Error) Unwrap() error
func New(code Code, message string) *Error
func Wrap(code Code, message string, cause error) *Error
```

哨兵码：
- `ERR_METADATA_INVALID`, `ERR_METADATA_NOT_FOUND`
- `ERR_CELL_NOT_FOUND`, `ERR_SLICE_NOT_FOUND`
- `ERR_CONTRACT_NOT_FOUND`, `ERR_ASSEMBLY_NOT_FOUND`
- `ERR_LIFECYCLE_INVALID`, `ERR_DEPENDENCY_CYCLE`
- `ERR_VALIDATION_FAILED`, `ERR_REFERENCE_BROKEN`

### ctxkeys 接口设计

```go
package ctxkeys

type ctxKey string

const (
    CellID        ctxKey = "cell_id"
    SliceID       ctxKey = "slice_id"
    CorrelationID ctxKey = "correlation_id"
    JourneyID     ctxKey = "journey_id"
    TraceID       ctxKey = "trace_id"
    SpanID        ctxKey = "span_id"
)

func WithCellID(ctx context.Context, id string) context.Context
func CellIDFrom(ctx context.Context) (string, bool)
// ... 每个 key 一对 With/From
```

---

## Step 0.2 — 核心类型 `kernel/cell/`（Day 1-2）

| 文件 | 内容 |
|------|------|
| `src/kernel/cell/types.go` | `CellType`（core/edge/support）、`Level`（L0-L4）+ ParseLevel/String、`HealthStatus`、`ContractKind`（http/event/command/projection）、`ContractRole`（serve/call/publish/subscribe/handle/invoke/provide/read）、`Lifecycle`（draft/active/deprecated） |
| `src/kernel/cell/interfaces.go` | `Cell`、`Slice`、`Contract`、`Assembly` 四个核心接口 + `Dependencies`、`VerifySpec`、`Waiver`、`CellMetadata`、`Owner`、`SchemaConfig` 等关联类型 |
| `src/kernel/cell/consistency.go` | `ValidRolesForKind(kind) []ContractRole`、`IsProviderRole(role) bool`、`IsConsumerRole(role) bool` |
| `src/kernel/cell/types_test.go` | ParseLevel round-trip、枚举合法性 |
| `src/kernel/cell/consistency_test.go` | kind-role 映射 table-driven |

### types.go

```go
package cell

type CellType string
const (
    CellTypeCore    CellType = "core"
    CellTypeEdge    CellType = "edge"
    CellTypeSupport CellType = "support"
)

type Level int
const (
    L0 Level = iota  // LocalOnly
    L1               // LocalTx
    L2               // OutboxFact
    L3               // WorkflowEventual
    L4               // DeviceLatent
)
func ParseLevel(s string) (Level, error)
func (l Level) String() string

type HealthStatus struct {
    Status  string            // "healthy" | "degraded" | "unhealthy"
    Details map[string]string
}

type ContractKind string
const (
    ContractHTTP       ContractKind = "http"
    ContractEvent      ContractKind = "event"
    ContractCommand    ContractKind = "command"
    ContractProjection ContractKind = "projection"
)

type ContractRole string
const (
    RoleServe     ContractRole = "serve"
    RoleCall      ContractRole = "call"
    RolePublish   ContractRole = "publish"
    RoleSubscribe ContractRole = "subscribe"
    RoleHandle    ContractRole = "handle"
    RoleInvoke    ContractRole = "invoke"
    RoleProvide   ContractRole = "provide"
    RoleRead      ContractRole = "read"
)

type Lifecycle string
const (
    LifecycleDraft      Lifecycle = "draft"
    LifecycleActive     Lifecycle = "active"
    LifecycleDeprecated Lifecycle = "deprecated"
)
```

### interfaces.go

```go
package cell

import "context"

type Dependencies struct {
    Cells     map[string]Cell
    Contracts map[string]Contract
    Config    map[string]any
}

type VerifySpec struct {
    Unit     []string
    Contract []string
    Waivers  []Waiver
}

type Waiver struct {
    Contract  string
    Owner     string
    Reason    string
    ExpiresAt string
}

type CellMetadata struct {
    ID               string
    Type             CellType
    ConsistencyLevel Level
    Owner            Owner
    Schema           SchemaConfig
    Verify           CellVerify
    L0Dependencies   []L0Dep
}

type Owner struct {
    Team string
    Role string
}

type SchemaConfig struct {
    Primary string
}

type CellVerify struct {
    Smoke []string
}

type L0Dep struct {
    Cell   string
    Reason string
}

// --- Core Interfaces ---

type Cell interface {
    ID() string
    Type() CellType
    ConsistencyLevel() Level
    Init(ctx context.Context, deps Dependencies) error
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
    Health() HealthStatus
    Ready() bool
    Metadata() CellMetadata
    OwnedSlices() []Slice
    ProducedContracts() []Contract
    ConsumedContracts() []Contract
}

type Slice interface {
    ID() string
    BelongsToCell() string
    ConsistencyLevel() Level
    Init(ctx context.Context) error
    Verify() VerifySpec
    AllowedFiles() []string
    AffectedJourneys() []string
}

type Contract interface {
    ID() string
    Kind() ContractKind
    OwnerCell() string
    ConsistencyLevel() Level
    Lifecycle() Lifecycle
}

type Assembly interface {
    Register(cell Cell) error
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
    Health() map[string]HealthStatus
}
```

### consistency.go

```go
// ValidRolesForKind returns legal roles for a given contract kind.
//   http:       serve, call
//   event:      publish, subscribe
//   command:    handle, invoke
//   projection: provide, read
func ValidRolesForKind(kind ContractKind) []ContractRole

// IsProviderRole returns true for serve/publish/handle/provide.
func IsProviderRole(role ContractRole) bool

// IsConsumerRole returns true for call/subscribe/invoke/read.
func IsConsumerRole(role ContractRole) bool
```

---

## Step 0.3 — 基础实现 `kernel/cell/base.go`（Day 2-3）

| 文件 | 内容 |
|------|------|
| `src/kernel/cell/base.go` | `BaseCell`、`BaseSlice`、`BaseContract` 默认实现 |
| `src/kernel/cell/base_test.go` | 生命周期 + accessor 测试 |

### BaseCell

```go
type BaseCell struct {
    meta     CellMetadata
    slices   []Slice
    produced []Contract
    consumed []Contract
    started  bool
    healthy  bool
}

func NewBaseCell(meta CellMetadata) *BaseCell

// 实现 Cell 接口全部方法
func (b *BaseCell) ID() string
func (b *BaseCell) Type() CellType
func (b *BaseCell) ConsistencyLevel() Level
func (b *BaseCell) Init(ctx context.Context, deps Dependencies) error
func (b *BaseCell) Start(ctx context.Context) error
func (b *BaseCell) Stop(ctx context.Context) error
func (b *BaseCell) Health() HealthStatus
func (b *BaseCell) Ready() bool
func (b *BaseCell) Metadata() CellMetadata
func (b *BaseCell) OwnedSlices() []Slice
func (b *BaseCell) ProducedContracts() []Contract
func (b *BaseCell) ConsumedContracts() []Contract

// 构建方法
func (b *BaseCell) AddSlice(s Slice)
func (b *BaseCell) AddProducedContract(c Contract)
func (b *BaseCell) AddConsumedContract(c Contract)
```

### BaseSlice / BaseContract

```go
type BaseSlice struct {
    id, cellID   string
    level        Level
    verify       VerifySpec
    allowedFiles []string
    journeys     []string
}

func NewBaseSlice(id, cellID string, level Level) *BaseSlice
// 实现 Slice 接口

type BaseContract struct {
    id    string
    kind  ContractKind
    owner string
    level Level
    lc    Lifecycle
}

func NewBaseContract(id string, kind ContractKind, owner string, level Level) *BaseContract
// 实现 Contract 接口
```

---

## Step 0.4 — Assembly 运行时 `kernel/assembly/`（Day 3-4）

| 文件 | 内容 |
|------|------|
| `src/kernel/assembly/assembly.go` | `CoreAssembly` — Assembly 接口实现 |
| `src/kernel/assembly/assembly_test.go` | 生命周期 + 错误场景测试 |

```go
package assembly

type Config struct {
    ID string
}

type CoreAssembly struct {
    id      string
    cells   []cell.Cell
    started bool
}

func New(cfg Config) *CoreAssembly

// Register 注册 Cell，校验无重复 ID
func (a *CoreAssembly) Register(c cell.Cell) error

// Start 按注册顺序 Init → Start 每个 Cell
func (a *CoreAssembly) Start(ctx context.Context) error

// Stop 按反序 Stop 每个 Cell
func (a *CoreAssembly) Stop(ctx context.Context) error

// Health 返回所有 Cell 健康状态
func (a *CoreAssembly) Health() map[string]cell.HealthStatus
```

测试场景：
- 注册 2 个 mock cell → Start → Health 全 healthy → Stop 反序验证
- 重复 Cell ID → 报错
- Init 失败 → 已 Init 的 cell 不 Start

---

## Step 0.5 — 顶层入口 + Gate 测试（Day 4）

| 文件 | 内容 |
|------|------|
| `src/gocell.go` | 顶层便利函数 |
| `src/gocell_test.go` | Phase 0 Gate 测试 |

```go
// src/gocell.go
package gocell

import "github.com/ghbvf/gocell/kernel/assembly"

func NewAssembly(id string) *assembly.CoreAssembly {
    return assembly.New(assembly.Config{ID: id})
}
```

```go
// src/gocell_test.go — Phase 0 Gate
func TestPhase0Gate(t *testing.T) {
    app := gocell.NewAssembly("test-bundle")
    myCell := cell.NewBaseCell(cell.CellMetadata{
        ID:               "test-cell",
        Type:             cell.CellTypeCore,
        ConsistencyLevel: cell.L1,
    })
    require.NoError(t, app.Register(myCell))
    require.NoError(t, app.Start(context.Background()))
    health := app.Health()
    require.Equal(t, "healthy", health["test-cell"].Status)
    require.NoError(t, app.Stop(context.Background()))
}
```

---

## Step 0.6 — 元数据 JSON Schema `kernel/metadata/schemas/`（Day 5-7）

master-plan Phase 0 第 4 项明确要求产出 JSON Schema。这些 schema 是 validate-meta 的格式校验基础，也支撑 IDE YAML 自动补全。

| Schema 文件 | 定义对象 | 关键字段 |
|---|---|---|
| `cell.schema.json` | cell.yaml | id, type(enum), consistencyLevel(enum), owner, schema.primary, verify.smoke, l0Dependencies |
| `slice.schema.json` | slice.yaml | id, belongsToCell, contractUsages[]{contract,role(enum)}, verify{unit,contract,waivers} |
| `contract.schema.json` | contract.yaml | id, kind(enum), ownerCell, consistencyLevel, lifecycle(enum), endpoints(oneOf per kind), schemaRefs; event 时 required: replayable, idempotencyKey, deliverySemantics |
| `assembly.schema.json` | assembly.yaml | id, cells[], build{entrypoint,binary,deployTemplate} |
| `journey.schema.json` | J-*.yaml | id, goal, owner, cells[], contracts[], passCriteria[]{text,mode(enum),checkRef} |
| `status-board.schema.json` | status-board.yaml | array of {journeyId, state(enum), risk(enum), blocker, updatedAt} |
| `actors.schema.json` | actors.yaml | array of {id, type(enum), maxConsistencyLevel(enum)} |

每个 schema 使用 JSON Schema Draft 2020-12。YAML 文件可通过 `# yaml-language-server: $schema=...` 注释获得 IDE 支持。

```go
// src/kernel/metadata/schemas/embed.go
package schemas

import "embed"

//go:embed *.json
var FS embed.FS
```

Schema 三重用途：
1. **validate-meta 格式校验** — FMT 规则第一层直接用 schema 驱动
2. **IDE 自动补全** — VS Code YAML 扩展支持 `$schema` 引用
3. **文档即代码** — schema 本身就是字段定义的 single source of truth

---

## Step 0.7 — 验证 + 构建（Day 7）

```bash
cd src && go build ./... && go test ./... -cover
```

全部编译通过，Gate 测试绿，覆盖率 ≥ 90%。

---

## 依赖图

```
pkg/errcode             ← stdlib
pkg/ctxkeys             ← stdlib
kernel/cell             ← stdlib + pkg/errcode + pkg/ctxkeys
kernel/metadata/schemas ← 纯 JSON 文件 + embed.go（无 Go 依赖）
kernel/assembly         ← kernel/cell + pkg/errcode
gocell.go               ← kernel/assembly
```

---

## 产出文件清单

| # | 文件路径 | 类型 |
|---|---------|------|
| 1 | `src/pkg/errcode/errcode.go` | Go |
| 2 | `src/pkg/errcode/errcode_test.go` | Go test |
| 3 | `src/pkg/ctxkeys/keys.go` | Go |
| 4 | `src/pkg/ctxkeys/keys_test.go` | Go test |
| 5 | `src/kernel/cell/types.go` | Go |
| 6 | `src/kernel/cell/interfaces.go` | Go |
| 7 | `src/kernel/cell/consistency.go` | Go |
| 8 | `src/kernel/cell/types_test.go` | Go test |
| 9 | `src/kernel/cell/consistency_test.go` | Go test |
| 10 | `src/kernel/cell/base.go` | Go |
| 11 | `src/kernel/cell/base_test.go` | Go test |
| 12 | `src/kernel/assembly/assembly.go` | Go |
| 13 | `src/kernel/assembly/assembly_test.go` | Go test |
| 14 | `src/gocell.go` | Go |
| 15 | `src/gocell_test.go` | Go test |
| 16 | `src/kernel/metadata/schemas/cell.schema.json` | JSON Schema |
| 17 | `src/kernel/metadata/schemas/slice.schema.json` | JSON Schema |
| 18 | `src/kernel/metadata/schemas/contract.schema.json` | JSON Schema |
| 19 | `src/kernel/metadata/schemas/assembly.schema.json` | JSON Schema |
| 20 | `src/kernel/metadata/schemas/journey.schema.json` | JSON Schema |
| 21 | `src/kernel/metadata/schemas/status-board.schema.json` | JSON Schema |
| 22 | `src/kernel/metadata/schemas/actors.schema.json` | JSON Schema |
| 23 | `src/kernel/metadata/schemas/embed.go` | Go |

**预估**: 23 个文件（15 Go + 1 embed + 7 JSON Schema），~1600 行

---

## 验证标准

| 检查项 | 标准 |
|--------|------|
| 编译 | `go build ./...` 零错误 |
| 测试 | `go test ./...` 全绿 |
| 覆盖率 | kernel/ 包 ≥ 90% |
| Gate | `NewAssembly → Register → Start → Health → Stop` 测试通过 |
| Schema | 7 个 JSON Schema 文件均为合法 JSON Schema Draft 2020-12 |
| 依赖 | `go.mod` 无新增外部依赖（schema embed 用 stdlib `embed`） |
