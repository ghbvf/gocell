package cell

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// BaseCell lifecycle
// ---------------------------------------------------------------------------

func TestBaseCellLifecycle(t *testing.T) {
	meta := CellMetadata{
		ID:               "auth-core",
		Type:             CellTypeCore,
		ConsistencyLevel: L2,
		Owner:            Owner{Team: "platform", Role: "owner"},
		Schema:           SchemaConfig{Primary: "auth"},
		Verify:           CellVerify{Smoke: []string{"smoke_auth"}},
	}

	c := NewBaseCell(meta)

	// Before Init: not ready, unhealthy.
	assert.False(t, c.Ready())
	assert.Equal(t, "unhealthy", c.Health().Status)

	// Init.
	require.NoError(t, c.Init(context.Background(), Dependencies{}))
	assert.False(t, c.Ready(), "after Init, not yet started")
	assert.Equal(t, "unhealthy", c.Health().Status)

	// Start.
	require.NoError(t, c.Start(context.Background()))
	assert.True(t, c.Ready())
	assert.Equal(t, "healthy", c.Health().Status)

	// Stop.
	require.NoError(t, c.Stop(context.Background()))
	assert.False(t, c.Ready())
	assert.Equal(t, "unhealthy", c.Health().Status)
}

func TestBaseCellAccessors(t *testing.T) {
	meta := CellMetadata{
		ID:               "config-core",
		Type:             CellTypeSupport,
		ConsistencyLevel: L1,
		Owner:            Owner{Team: "infra", Role: "maintainer"},
	}
	c := NewBaseCell(meta)

	assert.Equal(t, "config-core", c.ID())
	assert.Equal(t, CellTypeSupport, c.Type())
	assert.Equal(t, L1, c.ConsistencyLevel())
	assert.Equal(t, meta, c.Metadata())
}

func TestBaseCellSlicesAndContracts(t *testing.T) {
	c := NewBaseCell(CellMetadata{ID: "test-cell"})

	// Initially empty.
	assert.Empty(t, c.OwnedSlices())
	assert.Empty(t, c.ProducedContracts())
	assert.Empty(t, c.ConsumedContracts())

	// Add slice.
	s := NewBaseSlice("s1", "test-cell", L0)
	c.AddSlice(s)
	require.Len(t, c.OwnedSlices(), 1)
	assert.Equal(t, "s1", c.OwnedSlices()[0].ID())

	// Add produced contract.
	pc := NewBaseContract("pc1", ContractHTTP, "test-cell", L1)
	c.AddProducedContract(pc)
	require.Len(t, c.ProducedContracts(), 1)
	assert.Equal(t, "pc1", c.ProducedContracts()[0].ID())

	// Add consumed contract.
	cc := NewBaseContract("cc1", ContractEvent, "other-cell", L2)
	c.AddConsumedContract(cc)
	require.Len(t, c.ConsumedContracts(), 1)
	assert.Equal(t, "cc1", c.ConsumedContracts()[0].ID())
}

func TestBaseCellSlicesAndContractsReturnCopy(t *testing.T) {
	c := NewBaseCell(CellMetadata{ID: "copy-test"})
	s := NewBaseSlice("s1", "copy-test", L0)
	c.AddSlice(s)
	pc := NewBaseContract("pc1", ContractHTTP, "copy-test", L1)
	c.AddProducedContract(pc)
	cc := NewBaseContract("cc1", ContractEvent, "other", L2)
	c.AddConsumedContract(cc)

	// Mutating the returned slice must not affect the internal state.
	slices := c.OwnedSlices()
	slices[0] = nil
	assert.NotNil(t, c.OwnedSlices()[0], "OwnedSlices should return a defensive copy")

	produced := c.ProducedContracts()
	produced[0] = nil
	assert.NotNil(t, c.ProducedContracts()[0], "ProducedContracts should return a defensive copy")

	consumed := c.ConsumedContracts()
	consumed[0] = nil
	assert.NotNil(t, c.ConsumedContracts()[0], "ConsumedContracts should return a defensive copy")
}

func TestBaseCellReadyStates(t *testing.T) {
	c := NewBaseCell(CellMetadata{ID: "r"})

	// New: not ready.
	assert.False(t, c.Ready())

	// Init: not ready.
	require.NoError(t, c.Init(context.Background(), Dependencies{}))
	assert.False(t, c.Ready())

	// Start: ready.
	require.NoError(t, c.Start(context.Background()))
	assert.True(t, c.Ready())

	// Stop: not ready.
	require.NoError(t, c.Stop(context.Background()))
	assert.False(t, c.Ready())
}

// ---------------------------------------------------------------------------
// BaseCell state machine — invalid transitions
// ---------------------------------------------------------------------------

func TestBaseCellDoubleInit(t *testing.T) {
	c := NewBaseCell(CellMetadata{ID: "dbl-init"})
	require.NoError(t, c.Init(context.Background(), Dependencies{}))

	err := c.Init(context.Background(), Dependencies{})
	require.Error(t, err)
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrLifecycleInvalid, ecErr.Code)
}

func TestBaseCellStartWithoutInit(t *testing.T) {
	c := NewBaseCell(CellMetadata{ID: "no-init"})

	err := c.Start(context.Background())
	require.Error(t, err)
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrLifecycleInvalid, ecErr.Code)
}

func TestBaseCellDoubleStart(t *testing.T) {
	c := NewBaseCell(CellMetadata{ID: "dbl-start"})
	require.NoError(t, c.Init(context.Background(), Dependencies{}))
	require.NoError(t, c.Start(context.Background()))

	err := c.Start(context.Background())
	require.Error(t, err)
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrLifecycleInvalid, ecErr.Code)
}

func TestBaseCellStopWithoutStart(t *testing.T) {
	c := NewBaseCell(CellMetadata{ID: "no-start"})

	// Stop on a brand-new cell is a no-op.
	require.NoError(t, c.Stop(context.Background()))
}

func TestBaseCellInitThenStopSkipStart(t *testing.T) {
	c := NewBaseCell(CellMetadata{ID: "init-stop"})
	require.NoError(t, c.Init(context.Background(), Dependencies{}))

	// Stop from initialized is a no-op.
	require.NoError(t, c.Stop(context.Background()))
}

func TestBaseCellRestart(t *testing.T) {
	c := NewBaseCell(CellMetadata{ID: "restart"})

	// Full lifecycle.
	require.NoError(t, c.Init(context.Background(), Dependencies{}))
	require.NoError(t, c.Start(context.Background()))
	require.NoError(t, c.Stop(context.Background()))

	// Re-init from stopped state should succeed.
	require.NoError(t, c.Init(context.Background(), Dependencies{}))
	require.NoError(t, c.Start(context.Background()))
	assert.True(t, c.Ready())
}

// ---------------------------------------------------------------------------
// BaseSlice
// ---------------------------------------------------------------------------

func TestBaseSliceAccessors(t *testing.T) {
	s := NewBaseSlice("login-slice", "access-core", L1)

	assert.Equal(t, "login-slice", s.ID())
	assert.Equal(t, "access-core", s.BelongsToCell())
	assert.Equal(t, L1, s.ConsistencyLevel())
}

func TestBaseSliceInit(t *testing.T) {
	s := NewBaseSlice("s", "c", L0)
	require.NoError(t, s.Init(context.Background()))
}

func TestBaseSliceVerify(t *testing.T) {
	s := NewBaseSlice("s", "c", L0)

	// Default empty.
	assert.Empty(t, s.Verify().Unit)
	assert.Empty(t, s.Verify().Contract)

	// Set verify.
	v := VerifySpec{
		Unit:     []string{"test_unit"},
		Contract: []string{"test_contract"},
		Waivers:  []Waiver{{Contract: "c1", Owner: "team", Reason: "wip", ExpiresAt: "2026-06-01"}},
	}
	s.SetVerify(v)
	assert.Equal(t, v, s.Verify())
}

func TestBaseSliceAllowedFilesDefault(t *testing.T) {
	s := NewBaseSlice("login", "access-core", L1)
	assert.Equal(t, []string{"cells/access-core/slices/login/**"}, s.AllowedFiles())
}

func TestBaseSliceAllowedFilesCustom(t *testing.T) {
	s := NewBaseSlice("login", "access-core", L1)
	custom := []string{"custom/path/**"}
	s.SetAllowedFiles(custom)
	assert.Equal(t, custom, s.AllowedFiles())
}

func TestBaseSliceAffectedJourneys(t *testing.T) {
	s := NewBaseSlice("s", "c", L0)

	// Default empty.
	assert.Empty(t, s.AffectedJourneys())

	// Set.
	s.SetAffectedJourneys([]string{"J-001", "J-002"})
	assert.Equal(t, []string{"J-001", "J-002"}, s.AffectedJourneys())
}

// ---------------------------------------------------------------------------
// BaseContract
// ---------------------------------------------------------------------------

func TestBaseContractAccessors(t *testing.T) {
	c := NewBaseContract("session-api", ContractHTTP, "access-core", L2)

	assert.Equal(t, "session-api", c.ID())
	assert.Equal(t, ContractHTTP, c.Kind())
	assert.Equal(t, "access-core", c.OwnerCell())
	assert.Equal(t, L2, c.ConsistencyLevel())
	assert.Equal(t, LifecycleActive, c.Lifecycle(), "default lifecycle should be active")
}

func TestBaseContractSetLifecycle(t *testing.T) {
	c := NewBaseContract("api-v1", ContractHTTP, "access-core", L1)
	assert.Equal(t, LifecycleActive, c.Lifecycle(), "default should be active")

	tests := []struct {
		name string
		lc   Lifecycle
	}{
		{"draft", LifecycleDraft},
		{"deprecated", LifecycleDeprecated},
		{"back to active", LifecycleActive},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c.SetLifecycle(tt.lc)
			assert.Equal(t, tt.lc, c.Lifecycle())
		})
	}
}

func TestBaseContractAllKinds(t *testing.T) {
	tests := []struct {
		kind ContractKind
	}{
		{ContractHTTP},
		{ContractEvent},
		{ContractCommand},
		{ContractProjection},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			c := NewBaseContract("id", tt.kind, "owner", L0)
			assert.Equal(t, tt.kind, c.Kind())
		})
	}
}
