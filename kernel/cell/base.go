package cell

import (
	"context"
	"fmt"
	"sync"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
)

// Compile-time interface compliance checks.
//
// PR-A22 ISP split: Cell decomposes into four sub-interfaces. We assert each
// sub-interface independently — a missing method points to the exact subset
// being violated rather than a single "missing methods on Cell" error against
// the 12-method composite. The composite Cell type is automatically satisfied
// when all four sub-interface assertions hold.
//
// e.g. missing Stop() fails exactly: "(*BaseCell) does not implement CellLifecycle (missing method Stop)"
// rather than the diffuse "missing methods on Cell" against the 12-method composite.
//
// ref: docs/architecture/202605101800-adr-cell-interface-isp-split.md D3
var (
	_ CellIdentity  = (*BaseCell)(nil)
	_ CellLifecycle = (*BaseCell)(nil)
	_ CellStatus    = (*BaseCell)(nil)
	_ CellInventory = (*BaseCell)(nil)
	_ Slice         = (*BaseSlice)(nil)
	_ Contract      = (*BaseContract)(nil)
)

// ---------------------------------------------------------------------------
// cellState — lifecycle state machine for BaseCell
// ---------------------------------------------------------------------------

type cellState int

const (
	cellStateNew         cellState = iota // zero-value: not yet initialized
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
//
// meta is owned exclusively by this BaseCell — NewBaseCell deep-copies the
// caller's *metadata.CellMeta so subsequent mutation in the caller does not
// leak into the running cell. cellType / level cache the typed enum view
// computed once at construction (Type / ConsistencyLevel hot-path zero-cost).
type BaseCell struct {
	mu       sync.RWMutex
	meta     *metadata.CellMeta
	cellType cellvocab.CellType
	level    cellvocab.Level
	slices   []Slice
	produced []Contract
	consumed []Contract
	state    cellState

	// shutdownCtx is created in Start and canceled in Stop.
	// Goroutines spawned by the cell should use this context instead of
	// context.Background() so they are properly canceled on shutdown.
	shutdownCtx    context.Context
	shutdownCancel context.CancelFunc
}

// NewBaseCell creates a BaseCell from declarative metadata.
//
// meta is the canonical source — its Type / ConsistencyLevel string fields
// are parsed into typed enums at construction so per-call accessors stay
// allocation-free. Empty Type / ConsistencyLevel are accepted (zero-value
// cellvocab.CellType / cellvocab.L0) — callers needing strict validation must feed metadata
// already validated by gocell governance. A non-empty but unrecognized
// value returns ErrValidationFailed (caller decides: fall back, surface to
// readyz, etc.). The provided pointer is not retained — meta is deep-copied
// via deepCopyMeta so mutation by the caller does not leak into the cell.
//
// Static wiring (cell.go literals, table-driven tests) should use
// MustNewBaseCell, which panics on construction error per the
// PANIC-REGISTERED-01 / ERROR-FIRST-API-01 contract.
func NewBaseCell(meta *metadata.CellMeta) (*BaseCell, error) {
	if meta == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "cell.NewBaseCell: meta is nil")
	}
	if meta.ID == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "cell.NewBaseCell: meta.ID is empty")
	}
	var cellType cellvocab.CellType
	if meta.Type != "" {
		ct, err := cellvocab.ParseCellType(meta.Type)
		if err != nil {
			return nil, fmt.Errorf("cell.NewBaseCell: cell %q: %w", meta.ID, err)
		}
		cellType = ct
	}
	level := cellvocab.Level(0)
	if meta.ConsistencyLevel != "" {
		lv, err := cellvocab.ParseLevel(meta.ConsistencyLevel)
		if err != nil {
			return nil, fmt.Errorf("cell.NewBaseCell: cell %q: %w", meta.ID, err)
		}
		level = lv
	}
	return &BaseCell{
		meta:     meta.Clone(),
		cellType: cellType,
		level:    level,
	}, nil
}

// MustNewBaseCell is the panic-on-error twin of NewBaseCell, intended for
// composition-root and test sites that build cells from static literals
// where a construction failure is a programmer error and must abort startup.
// Do not call from request handlers, hot paths, or config-reload callbacks —
// use NewBaseCell and propagate the error to /readyz or a 5xx response.
// See ADR docs/architecture/202604270030-architectural-panic-whitelist.md §5.
func MustNewBaseCell(meta *metadata.CellMeta) *BaseCell {
	c, err := NewBaseCell(meta)
	if err != nil {
		panic(panicregister.Approved("cell-base-init", errcode.Assertion("cell.MustNewBaseCell: %v", err)))
	}
	return c
}

func (b *BaseCell) ID() string                        { return b.meta.ID }
func (b *BaseCell) Type() cellvocab.CellType          { return b.cellType }
func (b *BaseCell) ConsistencyLevel() cellvocab.Level { return b.level }

// Metadata returns an independent deep copy of the cell's declarative
// metadata. Callers may freely mutate the returned value without
// affecting the cell's internal state (fail-closed isolation, since the
// previous read-only contract on a shared pointer was unenforceable).
func (b *BaseCell) Metadata() *metadata.CellMeta { return b.meta.Clone() }

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
func (b *BaseCell) Init(_ context.Context, _ Registry) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state != cellStateNew && b.state != cellStateStopped {
		return errcode.New(errcode.KindInvalid, errcode.ErrLifecycleInvalid,
			"cell Init requires state new or stopped",
			errcode.WithInternal(fmt.Sprintf("cell=%q state=%d", b.meta.ID, b.state)))
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
// the initialized state. A shutdownCtx is created that will be canceled
// when Stop is called — goroutines should use ShutdownCtx() instead of
// context.Background().
func (b *BaseCell) Start(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state != cellStateInitialized {
		return errcode.New(errcode.KindInvalid, errcode.ErrLifecycleInvalid,
			"cell Start requires state initialized",
			errcode.WithInternal(fmt.Sprintf("cell=%q state=%d", b.meta.ID, b.state)))
	}
	b.shutdownCtx, b.shutdownCancel = context.WithCancel(context.WithoutCancel(ctx))
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
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.state == cellStateStarted {
		return HealthStatus{Status: "healthy"}
	}
	return HealthStatus{Status: "unhealthy"}
}

// Ready returns true when the cell is in the started state.
func (b *BaseCell) Ready() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.state == cellStateStarted
}

// ShutdownCtx returns a context that is canceled when Stop is called.
// It should be used by goroutines spawned by the cell instead of
// context.Background(). Returns context.Background() if the cell has
// not been started.
func (b *BaseCell) ShutdownCtx() context.Context {
	b.mu.RLock()
	defer b.mu.RUnlock()
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
	level    cellvocab.Level
	verify   VerifySpec
	allowed  []string
	journeys []string
}

// NewBaseSliceFromMeta constructs a BaseSlice from parsed slice.yaml metadata,
// which is the single source of truth for slice identity and consistency level.
//
// The metadata projection lives in `<slicePkg>/slice_gen.go` as `var sliceMeta`
// rendered by `gocell generate cell`; cell composition roots call
// `cell.MustNewBaseSliceFromMeta(<slicePkg>.SliceMetadata())`. Hand-written
// `cell.NewBaseSlice(id, cellID, level)` literals (the prior form) are
// forbidden — see `tools/archtest/baseslice_ctor_funnel_test.go`
// BASESLICE-CTOR-FUNNEL-01 and the codegen funnel in
// `tools/codegen/cellgen/templates/slice.tmpl`.
//
// All four metadata invariants are validated:
//   - meta must be non-nil
//   - meta.ID must be non-empty
//   - meta.BelongsToCell must be non-empty
//   - meta.ConsistencyLevel must be non-empty and parse to a valid Level
//
// There is no fallback inheritance from cell.consistencyLevel — the strict
// parser (`kernel/metadata.Parser`) rejects slice.yaml that omits the field.
//
// ref: kubernetes/kubernetes pkg/apis/core/validation/validation.go —
// schema-driven validation pattern; meta is the typed projection of the
// source-of-truth YAML.
func NewBaseSliceFromMeta(meta *metadata.SliceMeta) (*BaseSlice, error) {
	if meta == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cell.NewBaseSliceFromMeta: meta is nil")
	}
	if meta.ID == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cell.NewBaseSliceFromMeta: meta.ID is empty")
	}
	if meta.BelongsToCell == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cell.NewBaseSliceFromMeta: meta.BelongsToCell is empty",
			errcode.WithInternal(fmt.Sprintf("slice=%q", meta.ID)))
	}
	if meta.ConsistencyLevel == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cell.NewBaseSliceFromMeta: meta.ConsistencyLevel is empty",
			errcode.WithInternal(fmt.Sprintf("slice=%q", meta.ID)))
	}
	level, err := cellvocab.ParseLevel(meta.ConsistencyLevel)
	if err != nil {
		return nil, fmt.Errorf("cell.NewBaseSliceFromMeta: slice %q: %w", meta.ID, err)
	}
	return &BaseSlice{
		id:     meta.ID,
		cellID: meta.BelongsToCell,
		level:  level,
	}, nil
}

// MustNewBaseSliceFromMeta is the panic-on-error twin of NewBaseSliceFromMeta,
// intended for composition-root and test sites that build slices from static
// metadata literals where a construction failure is a programmer error and
// must abort startup. Mirrors MustNewBaseCell semantics.
//
// Do not call from request handlers, hot paths, or config-reload callbacks —
// use NewBaseSliceFromMeta and propagate the error.
func MustNewBaseSliceFromMeta(meta *metadata.SliceMeta) *BaseSlice {
	s, err := NewBaseSliceFromMeta(meta)
	if err != nil {
		panic(panicregister.Approved("slice-base-init",
			errcode.Assertion("cell.MustNewBaseSliceFromMeta: %v", err)))
	}
	return s
}

func (s *BaseSlice) ID() string                        { return s.id }
func (s *BaseSlice) BelongsToCell() string             { return s.cellID }
func (s *BaseSlice) ConsistencyLevel() cellvocab.Level { return s.level }

// Init is a no-op for BaseSlice.
func (s *BaseSlice) Init(_ context.Context) error { return nil }

// Verify returns the verification spec for this slice.
func (s *BaseSlice) Verify() VerifySpec { return s.verify }

// AllowedFiles returns a copy of the file ownership paths.
// Returns nil if no paths have been set. Callers that need convention
// defaults should use gocell scaffold, which generates the initial paths.
func (s *BaseSlice) AllowedFiles() []string {
	if len(s.allowed) == 0 {
		return nil
	}
	out := make([]string, len(s.allowed))
	copy(out, s.allowed)
	return out
}

// AffectedJourneys returns a copy of the journey IDs this slice participates in.
func (s *BaseSlice) AffectedJourneys() []string {
	out := make([]string, len(s.journeys))
	copy(out, s.journeys)
	return out
}

// SetVerify sets the verification spec.
func (s *BaseSlice) SetVerify(v VerifySpec) { s.verify = v }

// SetAllowedFiles sets the file ownership paths for this slice.
func (s *BaseSlice) SetAllowedFiles(files []string) {
	s.allowed = append([]string(nil), files...)
}

// SetAffectedJourneys sets the journey IDs.
func (s *BaseSlice) SetAffectedJourneys(ids []string) {
	s.journeys = append([]string(nil), ids...)
}

// ---------------------------------------------------------------------------
// BaseContract
// ---------------------------------------------------------------------------

// BaseContract is the default implementation of the Contract interface.
type BaseContract struct {
	id    string
	kind  cellvocab.ContractKind
	owner string
	level cellvocab.Level
	lc    cellvocab.Lifecycle
}

// NewBaseContract creates a BaseContract with cellvocab.Lifecycle defaulting to cellvocab.LifecycleActive.
func NewBaseContract(id string, kind cellvocab.ContractKind, owner string, level cellvocab.Level) *BaseContract {
	return &BaseContract{
		id:    id,
		kind:  kind,
		owner: owner,
		level: level,
		lc:    cellvocab.LifecycleActive,
	}
}

func (c *BaseContract) ID() string                        { return c.id }
func (c *BaseContract) Kind() cellvocab.ContractKind      { return c.kind }
func (c *BaseContract) OwnerCell() string                 { return c.owner }
func (c *BaseContract) ConsistencyLevel() cellvocab.Level { return c.level }
func (c *BaseContract) Lifecycle() cellvocab.Lifecycle    { return c.lc }

// SetLifecycle updates the governance lifecycle state of the contract.
func (c *BaseContract) SetLifecycle(lc cellvocab.Lifecycle) { c.lc = lc }
