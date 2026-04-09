package cell

import (
	"context"
	"fmt"
	"sync"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// Compile-time interface compliance checks.
var (
	_ Cell     = (*BaseCell)(nil)
	_ Slice    = (*BaseSlice)(nil)
	_ Contract = (*BaseContract)(nil)
)

// ---------------------------------------------------------------------------
// cellState — lifecycle state machine for BaseCell
// ---------------------------------------------------------------------------

type cellState int

const (
	cellStateNew         cellState = iota // zero-value: not yet initialised
	cellStateInitialized                  // Init completed
	cellStateStarted                      // Start completed
	cellStateStopped                      // Stop completed
)

// ---------------------------------------------------------------------------
// BaseCell
// ---------------------------------------------------------------------------

// BaseCell is the default implementation of the Cell interface.
// Embed or compose it to get a working Cell with minimal boilerplate.
// All state-accessing methods are protected by a mutex for safe concurrent use.
type BaseCell struct {
	mu       sync.RWMutex
	meta     CellMetadata
	slices   []Slice
	produced []Contract
	consumed []Contract
	state    cellState

	// shutdownCtx is created in Start and cancelled in Stop.
	// Goroutines spawned by the cell should use this context instead of
	// context.Background() so they are properly cancelled on shutdown.
	shutdownCtx    context.Context
	shutdownCancel context.CancelFunc
}

// NewBaseCell creates a BaseCell from declarative metadata.
func NewBaseCell(meta CellMetadata) *BaseCell {
	return &BaseCell{meta: meta}
}

func (b *BaseCell) ID() string             { return b.meta.ID }
func (b *BaseCell) Type() CellType         { return b.meta.Type }
func (b *BaseCell) ConsistencyLevel() Level { return b.meta.ConsistencyLevel }
func (b *BaseCell) Metadata() CellMetadata { return b.meta }

// OwnedSlices returns a copy of the owned slice list.
func (b *BaseCell) OwnedSlices() []Slice {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]Slice, len(b.slices))
	copy(out, b.slices)
	return out
}

// ProducedContracts returns a copy of the produced contract list.
func (b *BaseCell) ProducedContracts() []Contract {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]Contract, len(b.produced))
	copy(out, b.produced)
	return out
}

// ConsumedContracts returns a copy of the consumed contract list.
func (b *BaseCell) ConsumedContracts() []Contract {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]Contract, len(b.consumed))
	copy(out, b.consumed)
	return out
}

// Init prepares the cell. Only allowed from the new or stopped state.
func (b *BaseCell) Init(_ context.Context, _ Dependencies) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state != cellStateNew && b.state != cellStateStopped {
		return errcode.New(errcode.ErrLifecycleInvalid,
			fmt.Sprintf("cell %q: Init requires state new or stopped, current state: %d", b.meta.ID, b.state))
	}
	// Reset shutdown context from previous lifecycle to avoid stale cancellation.
	if b.shutdownCancel != nil {
		b.shutdownCancel()
	}
	b.shutdownCtx, b.shutdownCancel = nil, nil
	b.state = cellStateInitialized
	return nil
}

// Start transitions the cell to the running state. Only allowed from
// the initialized state. A shutdownCtx is created that will be cancelled
// when Stop is called — goroutines should use ShutdownCtx() instead of
// context.Background().
func (b *BaseCell) Start(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state != cellStateInitialized {
		return errcode.New(errcode.ErrLifecycleInvalid,
			fmt.Sprintf("cell %q: Start requires state initialized, current state: %d", b.meta.ID, b.state))
	}
	b.shutdownCtx, b.shutdownCancel = context.WithCancel(ctx)
	b.state = cellStateStarted
	return nil
}

// Stop transitions the cell to the stopped state. Only allowed from the
// started state; calling Stop from new or initialized is a no-op.
// Cancels the shutdownCtx to signal goroutines to exit.
func (b *BaseCell) Stop(_ context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case cellStateStarted:
		if b.shutdownCancel != nil {
			b.shutdownCancel()
		}
		b.state = cellStateStopped
		return nil
	case cellStateNew, cellStateInitialized:
		// no-op: nothing to tear down
		return nil
	default:
		// already stopped — also a no-op
		return nil
	}
}

// Health returns the current HealthStatus.
func (b *BaseCell) Health() HealthStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == cellStateStarted {
		return HealthStatus{Status: "healthy"}
	}
	return HealthStatus{Status: "unhealthy"}
}

// Ready returns true when the cell is in the started state.
func (b *BaseCell) Ready() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state == cellStateStarted
}

// ShutdownCtx returns a context that is cancelled when Stop is called.
// It should be used by goroutines spawned by the cell instead of
// context.Background(). Returns context.Background() if the cell has
// not been started.
func (b *BaseCell) ShutdownCtx() context.Context {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.shutdownCtx != nil {
		return b.shutdownCtx
	}
	return context.Background()
}

// AddSlice appends a Slice to this cell's owned slice list.
func (b *BaseCell) AddSlice(s Slice) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.slices = append(b.slices, s)
}

// AddProducedContract appends a Contract this cell produces.
func (b *BaseCell) AddProducedContract(c Contract) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.produced = append(b.produced, c)
}

// AddConsumedContract appends a Contract this cell consumes.
func (b *BaseCell) AddConsumedContract(c Contract) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.consumed = append(b.consumed, c)
}

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

// AllowedFiles returns a copy of the file ownership paths. If none have been
// set explicitly, it returns the default convention path.
func (s *BaseSlice) AllowedFiles() []string {
	if len(s.allowed) > 0 {
		out := make([]string, len(s.allowed))
		copy(out, s.allowed)
		return out
	}
	return []string{fmt.Sprintf("cells/%s/slices/%s/**", s.cellID, s.id)}
}

// AffectedJourneys returns a copy of the journey IDs this slice participates in.
func (s *BaseSlice) AffectedJourneys() []string {
	out := make([]string, len(s.journeys))
	copy(out, s.journeys)
	return out
}

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
