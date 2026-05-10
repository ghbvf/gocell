package cell

import (
	"context"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// VerifySpec describes the verification requirements for a Slice.
type VerifySpec struct {
	Unit     []string
	Contract []string
	Waivers  []Waiver
}

// Waiver records a temporary exemption from a contract verification.
type Waiver struct {
	Contract  string
	Owner     string
	Reason    string
	ExpiresAt string
}

// Cell-level metadata types live exclusively in kernel/metadata.
// See ADR docs/architecture/202605051300-adr-kernel-cellmeta-single-source.md.

// --- Core Interfaces ---

// CellIdentity exposes the cell's stable architectural identity. Returned
// values are immutable for the lifetime of the cell.
//
// Consumers: registry lookup, metrics labels, log correlation, route attribution.
//
// ref: docs/architecture/202605101800-adr-cell-interface-isp-split.md D1
type CellIdentity interface {
	// ID returns the cell's stable identifier (matches cell.yaml id).
	ID() string
	// Type classifies the cell's architectural role (core / edge / support).
	Type() CellType
	// ConsistencyLevel reports the cell's declared consistency tier (L0–L4).
	ConsistencyLevel() Level
}

// CellLifecycle drives the cell through Init → Start → Stop transitions. All
// methods accept a context and must honor cancellation.
//
// Resource acquisition rule: Init must NOT acquire resources requiring explicit
// release (open connections, spawn goroutines, lease distributed locks); defer
// such acquisition to Start so Start/Stop remain symmetric. Stop must release
// every resource Start acquired and must be idempotent against repeated invocation.
//
// Consumers: Assembly orchestrator (kernel/cell.Assembly), bootstrap phases,
// lifecycle test harnesses.
type CellLifecycle interface {
	// Init registers cell capabilities via the provided Registry; called once
	// before Start. Init failure aborts assembly start without invoking Stop.
	// Init may validate config, build in-memory state, and register slices.
	Init(ctx context.Context, reg Registry) error
	// Start brings the cell to a running state. Returns nil only when the cell
	// is fully ready to serve traffic. Canceling ctx must abort startup.
	// Resources acquired here must be released by the matching Stop call.
	Start(ctx context.Context) error
	// Stop performs graceful shutdown. The assembly invokes Stop with the
	// caller-supplied ctx; no per-cell stop timeout is enforced by the kernel,
	// so implementations are solely responsible for honoring ctx deadlines
	// and returning promptly when ctx is canceled.
	Stop(ctx context.Context) error
}

// CellStatus reports runtime probe state. Both methods are safe for concurrent
// calls and reflect derived state from the cell's lifecycle state machine
// (not declared metadata).
//
// Consumers: /healthz / /readyz HTTP handlers, runtime supervision.
type CellStatus interface {
	// Health returns the current health snapshot for diagnostic surfaces.
	Health() HealthStatus
	// Ready reports whether the cell is currently serving (post-Start, pre-Stop).
	Ready() bool
}

// CellInventory exposes the cell's declarative metadata + slice/contract
// inventories as defensive copies (callers may freely mutate returned values).
// The single source of truth is *metadata.CellMeta in kernel/metadata; this
// interface is the read path. CELLMETA-SINGLE-SOURCE-03 archtest pins
// Metadata() here.
//
// Consumers: contract validators, metadata inspectors, gocell validate, codegen.
type CellInventory interface {
	// Metadata returns an independent deep copy of the cell's declarative
	// metadata; callers may freely read and modify the returned value.
	Metadata() *metadata.CellMeta
	// OwnedSlices returns the slices this cell registered.
	OwnedSlices() []Slice
	// ProducedContracts returns contracts whose authoritative source is this cell.
	ProducedContracts() []Contract
	// ConsumedContracts returns contracts this cell calls or subscribes to.
	ConsumedContracts() []Contract
}

// Cell is the fundamental building block of a GoCell application: a vertically
// integrated unit of capability that owns its data, slices, and contracts.
// Cells are registered in an Assembly which drives the Init → Start → Stop
// lifecycle.
//
// Cell is the composite of the four sub-interfaces above; any type satisfying
// CellIdentity, CellLifecycle, CellStatus, and CellInventory is a Cell.
// Implementations should embed BaseCell rather than implement these from scratch.
// Prefer the narrower sub-interfaces when the caller only needs a subset.
//
// ref: docs/architecture/202605101800-adr-cell-interface-isp-split.md D2
// ref: io.ReadWriter / kubernetes/apimachinery meta/v1.Object composition
type Cell interface {
	CellIdentity
	CellLifecycle
	CellStatus
	CellInventory
}

// Slice is a cohesive sub-unit within a Cell — typically a single feature
// (handler + service + repo + tests) that maps 1:1 to a slice.yaml entry.
type Slice interface {
	// ID returns the slice's stable identifier (matches slice.yaml id).
	ID() string
	// BelongsToCell returns the parent cell's ID.
	BelongsToCell() string
	// ConsistencyLevel reports the slice's consistency tier; falls back to the
	// owning cell's level when not declared explicitly.
	ConsistencyLevel() Level
	// Init runs slice-local initialisation (no dependencies; that lives on Cell.Init).
	Init(ctx context.Context) error
	// Verify returns the verification spec parsed from slice.yaml.
	Verify() VerifySpec
	// AllowedFiles returns the file ownership paths. Returns nil when unset;
	// callers should treat nil as a configuration error (FMT-14 requires this field).
	AllowedFiles() []string
	// AffectedJourneys returns the journey IDs this slice participates in.
	AffectedJourneys() []string
}

// Contract defines a communication boundary between Cells. Contracts are
// authoritative metadata loaded from contracts/**/contract.yaml; in-process
// values implementing this interface are derivative views only.
type Contract interface {
	// ID returns the contract's stable identifier (matches contract.yaml id).
	ID() string
	// Kind classifies the communication pattern (http / event / command / projection).
	Kind() ContractKind
	// OwnerCell returns the cell ID that owns the contract's authoritative side.
	OwnerCell() string
	// ConsistencyLevel reports the contract's consistency tier (L0–L4).
	ConsistencyLevel() Level
	// Lifecycle returns the contract's governance state (draft / active / deprecated).
	Lifecycle() Lifecycle
}

// Assembly orchestrates a set of Cells into a runnable application: registers
// cells, sequences Init/Start/Stop in dependency order, and aggregates health.
type Assembly interface {
	// Register adds a Cell to the assembly. Subsequent Register calls before
	// Start are accumulated; calls after Start return an error.
	Register(cell Cell) error
	// Start initializes and starts every registered cell in dependency order.
	// On any cell's failure, previously-started cells are Stopped in reverse.
	Start(ctx context.Context) error
	// Stop drives graceful shutdown of every started cell in reverse order.
	Stop(ctx context.Context) error
	// Health returns each cell's current health snapshot, keyed by cell ID.
	Health() map[string]HealthStatus
}
