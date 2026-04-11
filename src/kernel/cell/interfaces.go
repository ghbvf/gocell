package cell

import "context"

// Dependencies is the set of collaborators injected into a Cell during Init.
type Dependencies struct {
	Cells     map[string]Cell
	Contracts map[string]Contract
	Config    map[string]any
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

// Cell is the fundamental building block of a GoCell application.
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

// Slice is a cohesive sub-unit within a Cell.
type Slice interface {
	ID() string
	BelongsToCell() string
	ConsistencyLevel() Level
	Init(ctx context.Context) error
	Verify() VerifySpec
	AllowedFiles() []string
	AffectedJourneys() []string
}

// Contract defines a communication boundary between Cells.
type Contract interface {
	ID() string
	Kind() ContractKind
	OwnerCell() string
	ConsistencyLevel() Level
	Lifecycle() Lifecycle
}

// Assembly orchestrates a set of Cells into a runnable application.
type Assembly interface {
	Register(cell Cell) error
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Health() map[string]HealthStatus
}
