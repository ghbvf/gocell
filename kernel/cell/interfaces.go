package cell

import "context"

// Dependencies is the set of collaborators injected into a Cell during Init.
//
// ADR: Frozen — fields intentionally minimal.
//
// Status: Accepted (2026-04-11, CS-AR-2)
//
// Decision: Dependencies carries only Config. Cross-cell access MUST go
// through contracts, not through a shared cell graph. All concrete
// dependencies (repos, outbox writers, publishers) are injected via
// functional options at cell construction time, not via Dependencies.
//
// Previously this struct also carried Cells map[string]Cell and
// Contracts map[string]Contract. Analysis showed zero callers read
// either field — exposing the full cell graph violated least-privilege.
//
// The struct wrapper is retained (rather than passing map[string]any
// directly) for forward compatibility: future fields (e.g. Secrets,
// ServiceLocator) can be added without changing the Cell.Init signature.
type Dependencies struct {
	Config         map[string]any
	DurabilityMode DurabilityMode // Required: Demo or Durable (zero value rejected); see durability.go
}

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

// CellMetadata carries the declarative metadata of a Cell (mirrors cell.yaml).
type CellMetadata struct {
	ID               string
	Type             CellType
	ConsistencyLevel Level
	Owner            Owner
	Schema           SchemaConfig
	Verify           CellVerify
	L0Dependencies   []L0Dep
}

// Owner identifies the team responsible for a Cell.
type Owner struct {
	Team string
	Role string
}

// SchemaConfig holds the primary data schema reference for a Cell.
type SchemaConfig struct {
	Primary string
}

// CellVerify holds structured verify refs for a Cell.
// Smoke refs use the format: smoke.{cellID}.{suffix}
type CellVerify struct {
	Smoke []string
}

// L0Dep declares a direct dependency on an L0 (LocalOnly) Cell.
type L0Dep struct {
	Cell   string
	Reason string
}

// --- Core Interfaces ---

// Cell is the fundamental building block of a GoCell application: a vertically
// integrated unit of capability that owns its data, slices, and contracts.
// Cells are registered in an Assembly which drives the Init → Start → Stop
// lifecycle. Implementations should embed BaseCell for the common metadata
// fields and add the behavioural methods (Init/Start/Stop) themselves.
type Cell interface {
	// ID returns the cell's stable identifier (matches cell.yaml id).
	ID() string
	// Type classifies the cell's architectural role (core / edge / support).
	Type() CellType
	// ConsistencyLevel reports the cell's declared consistency tier (L0–L4).
	ConsistencyLevel() Level
	// Init wires injected dependencies; called once before Start. Init failure
	// aborts assembly start without invoking Stop.
	Init(ctx context.Context, deps Dependencies) error
	// Start brings the cell to a running state. Returns nil only when the cell
	// is fully ready to serve traffic. Cancelling ctx must abort startup.
	Start(ctx context.Context) error
	// Stop performs graceful shutdown. Implementations must respect ctx
	// deadlines and return promptly when ctx is cancelled.
	Stop(ctx context.Context) error
	// Health returns the current health snapshot for diagnostic surfaces.
	Health() HealthStatus
	// Ready reports whether the cell is currently serving (post-Start, pre-Stop).
	Ready() bool
	// Metadata returns the declarative metadata loaded from cell.yaml.
	Metadata() CellMetadata
	// OwnedSlices returns the slices this cell registered.
	OwnedSlices() []Slice
	// ProducedContracts returns contracts whose authoritative source is this cell.
	ProducedContracts() []Contract
	// ConsumedContracts returns contracts this cell calls or subscribes to.
	ConsumedContracts() []Contract
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
	// Start initialises and starts every registered cell in dependency order.
	// On any cell's failure, previously-started cells are Stopped in reverse.
	Start(ctx context.Context) error
	// Stop drives graceful shutdown of every started cell in reverse order.
	Stop(ctx context.Context) error
	// Health returns each cell's current health snapshot, keyed by cell ID.
	Health() map[string]HealthStatus
}
