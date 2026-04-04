package cell

import (
	"context"
	"fmt"
)

// ---------------------------------------------------------------------------
// BaseCell
// ---------------------------------------------------------------------------

// BaseCell is the default implementation of the Cell interface.
// Embed or compose it to get a working Cell with minimal boilerplate.
type BaseCell struct {
	meta     CellMetadata
	slices   []Slice
	produced []Contract
	consumed []Contract
	started  bool
	healthy  bool
}

// NewBaseCell creates a BaseCell from declarative metadata.
func NewBaseCell(meta CellMetadata) *BaseCell {
	return &BaseCell{meta: meta}
}

func (b *BaseCell) ID() string               { return b.meta.ID }
func (b *BaseCell) Type() CellType            { return b.meta.Type }
func (b *BaseCell) ConsistencyLevel() Level   { return b.meta.ConsistencyLevel }
func (b *BaseCell) Metadata() CellMetadata    { return b.meta }
func (b *BaseCell) OwnedSlices() []Slice      { return b.slices }
func (b *BaseCell) ProducedContracts() []Contract { return b.produced }
func (b *BaseCell) ConsumedContracts() []Contract { return b.consumed }

// Init prepares the cell. After Init the cell is not yet started.
func (b *BaseCell) Init(_ context.Context, _ Dependencies) error {
	b.healthy = false
	return nil
}

// Start transitions the cell to the running state.
func (b *BaseCell) Start(_ context.Context) error {
	b.started = true
	b.healthy = true
	return nil
}

// Stop transitions the cell to the stopped state.
func (b *BaseCell) Stop(_ context.Context) error {
	b.started = false
	b.healthy = false
	return nil
}

// Health returns the current HealthStatus.
func (b *BaseCell) Health() HealthStatus {
	if b.healthy {
		return HealthStatus{Status: "healthy"}
	}
	return HealthStatus{Status: "unhealthy"}
}

// Ready returns true when the cell is both started and healthy.
func (b *BaseCell) Ready() bool {
	return b.started && b.healthy
}

// AddSlice appends a Slice to this cell's owned slice list.
func (b *BaseCell) AddSlice(s Slice) { b.slices = append(b.slices, s) }

// AddProducedContract appends a Contract this cell produces.
func (b *BaseCell) AddProducedContract(c Contract) { b.produced = append(b.produced, c) }

// AddConsumedContract appends a Contract this cell consumes.
func (b *BaseCell) AddConsumedContract(c Contract) { b.consumed = append(b.consumed, c) }

// ---------------------------------------------------------------------------
// BaseSlice
// ---------------------------------------------------------------------------

// BaseSlice is the default implementation of the Slice interface.
type BaseSlice struct {
	id       string
	cellID   string
	level    Level
	verify   VerifySpec
	allowed  []string
	journeys []string
}

// NewBaseSlice creates a BaseSlice with the given identity and consistency level.
func NewBaseSlice(id, cellID string, level Level) *BaseSlice {
	return &BaseSlice{
		id:     id,
		cellID: cellID,
		level:  level,
	}
}

func (s *BaseSlice) ID() string             { return s.id }
func (s *BaseSlice) BelongsToCell() string   { return s.cellID }
func (s *BaseSlice) ConsistencyLevel() Level { return s.level }

// Init is a no-op for BaseSlice.
func (s *BaseSlice) Init(_ context.Context) error { return nil }

// Verify returns the verification spec for this slice.
func (s *BaseSlice) Verify() VerifySpec { return s.verify }

// AllowedFiles returns the file ownership paths. If none have been set
// explicitly, it returns the default convention path.
func (s *BaseSlice) AllowedFiles() []string {
	if len(s.allowed) > 0 {
		return s.allowed
	}
	return []string{fmt.Sprintf("cells/%s/slices/%s/**", s.cellID, s.id)}
}

// AffectedJourneys returns the journey IDs this slice participates in.
func (s *BaseSlice) AffectedJourneys() []string { return s.journeys }

// SetVerify sets the verification spec.
func (s *BaseSlice) SetVerify(v VerifySpec) { s.verify = v }

// SetAllowedFiles overrides the default file ownership paths.
func (s *BaseSlice) SetAllowedFiles(files []string) { s.allowed = files }

// SetAffectedJourneys sets the journey IDs.
func (s *BaseSlice) SetAffectedJourneys(ids []string) { s.journeys = ids }

// ---------------------------------------------------------------------------
// BaseContract
// ---------------------------------------------------------------------------

// BaseContract is the default implementation of the Contract interface.
type BaseContract struct {
	id    string
	kind  ContractKind
	owner string
	level Level
	lc    Lifecycle
}

// NewBaseContract creates a BaseContract with Lifecycle defaulting to LifecycleActive.
func NewBaseContract(id string, kind ContractKind, owner string, level Level) *BaseContract {
	return &BaseContract{
		id:    id,
		kind:  kind,
		owner: owner,
		level: level,
		lc:    LifecycleActive,
	}
}

func (c *BaseContract) ID() string               { return c.id }
func (c *BaseContract) Kind() ContractKind        { return c.kind }
func (c *BaseContract) OwnerCell() string          { return c.owner }
func (c *BaseContract) ConsistencyLevel() Level   { return c.level }
func (c *BaseContract) Lifecycle() Lifecycle       { return c.lc }

// SetLifecycle updates the governance lifecycle state of the contract.
func (c *BaseContract) SetLifecycle(lc Lifecycle) { c.lc = lc }
