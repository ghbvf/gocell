package cell

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// ---------------------------------------------------------------------------
// BaseCell lifecycle
// ---------------------------------------------------------------------------

func TestBaseCellLifecycle(t *testing.T) {
	meta := &metadata.CellMeta{
		ID:               "auth-core",
		Type:             "core",
		ConsistencyLevel: "L2",
		Owner:            metadata.OwnerMeta{Team: "platform", Role: "owner"},
		Schema:           metadata.SchemaMeta{Primary: "auth"},
		Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke_auth"}},
	}

	c := MustNewBaseCell(meta)

	// Before Init: not ready, unhealthy.
	assert.False(t, c.Ready())
	assert.Equal(t, "unhealthy", c.Health().Status)

	// Init.
	require.NoError(t, c.Init(context.Background(), NewRegistryRecorder(nil, DurabilityDurable)))
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
	meta := &metadata.CellMeta{
		ID:               "configcore",
		Type:             "support",
		ConsistencyLevel: "L1",
		Owner:            metadata.OwnerMeta{Team: "infra", Role: "maintainer"},
	}
	c := MustNewBaseCell(meta)

	assert.Equal(t, "configcore", c.ID())
	assert.Equal(t, cellvocab.CellTypeSupport, c.Type())
	assert.Equal(t, cellvocab.L1, c.ConsistencyLevel())
	// Metadata() returns a pointer to the deep-copied internal snapshot;
	// compare by value (reflect.DeepEqual through assert.Equal handles
	// pointer-target comparison automatically).
	assert.Equal(t, meta, c.Metadata())
}

func TestBaseCellSlicesAndContracts(t *testing.T) {
	c := MustNewBaseCell(&metadata.CellMeta{ID: "test-cell"})

	// Initially empty.
	assert.Empty(t, c.OwnedSlices())
	assert.Empty(t, c.ProducedContracts())
	assert.Empty(t, c.ConsumedContracts())

	// Add slice.
	s := NewBaseSlice("s1", "test-cell", cellvocab.L0)
	c.AddSlice(s)
	require.Len(t, c.OwnedSlices(), 1)
	assert.Equal(t, "s1", c.OwnedSlices()[0].ID())

	// Add produced contract.
	pc := NewBaseContract("pc1", cellvocab.ContractHTTP, "test-cell", cellvocab.L1)
	c.AddProducedContract(pc)
	require.Len(t, c.ProducedContracts(), 1)
	assert.Equal(t, "pc1", c.ProducedContracts()[0].ID())

	// Add consumed contract.
	cc := NewBaseContract("cc1", cellvocab.ContractEvent, "other-cell", cellvocab.L2)
	c.AddConsumedContract(cc)
	require.Len(t, c.ConsumedContracts(), 1)
	assert.Equal(t, "cc1", c.ConsumedContracts()[0].ID())
}

func TestBaseCellSlicesAndContractsReturnCopy(t *testing.T) {
	c := MustNewBaseCell(&metadata.CellMeta{ID: "copy-test"})
	s := NewBaseSlice("s1", "copy-test", cellvocab.L0)
	c.AddSlice(s)
	pc := NewBaseContract("pc1", cellvocab.ContractHTTP, "copy-test", cellvocab.L1)
	c.AddProducedContract(pc)
	cc := NewBaseContract("cc1", cellvocab.ContractEvent, "other", cellvocab.L2)
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
	c := MustNewBaseCell(&metadata.CellMeta{ID: "r"})

	// New: not ready.
	assert.False(t, c.Ready())

	// Init: not ready.
	require.NoError(t, c.Init(context.Background(), NewRegistryRecorder(nil, DurabilityDurable)))
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
	c := MustNewBaseCell(&metadata.CellMeta{ID: "dbl-init"})
	require.NoError(t, c.Init(context.Background(), NewRegistryRecorder(nil, DurabilityDurable)))

	err := c.Init(context.Background(), NewRegistryRecorder(nil, DurabilityDurable))
	require.Error(t, err)
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrLifecycleInvalid, ecErr.Code)
}

func TestBaseCellStartWithoutInit(t *testing.T) {
	c := MustNewBaseCell(&metadata.CellMeta{ID: "no-init"})

	err := c.Start(context.Background())
	require.Error(t, err)
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrLifecycleInvalid, ecErr.Code)
}

func TestBaseCellDoubleStart(t *testing.T) {
	c := MustNewBaseCell(&metadata.CellMeta{ID: "dbl-start"})
	require.NoError(t, c.Init(context.Background(), NewRegistryRecorder(nil, DurabilityDurable)))
	require.NoError(t, c.Start(context.Background()))

	err := c.Start(context.Background())
	require.Error(t, err)
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrLifecycleInvalid, ecErr.Code)
}

func TestBaseCellStopWithoutStart(t *testing.T) {
	c := MustNewBaseCell(&metadata.CellMeta{ID: "no-start"})

	// Stop on a brand-new cell is a no-op.
	require.NoError(t, c.Stop(context.Background()))
}

func TestBaseCellInitThenStopSkipStart(t *testing.T) {
	c := MustNewBaseCell(&metadata.CellMeta{ID: "init-stop"})
	require.NoError(t, c.Init(context.Background(), NewRegistryRecorder(nil, DurabilityDurable)))

	// Stop from initialized is a no-op.
	require.NoError(t, c.Stop(context.Background()))
}

func TestBaseCellRestart(t *testing.T) {
	c := MustNewBaseCell(&metadata.CellMeta{ID: "restart"})

	// Full lifecycle.
	require.NoError(t, c.Init(context.Background(), NewRegistryRecorder(nil, DurabilityDurable)))
	require.NoError(t, c.Start(context.Background()))
	require.NoError(t, c.Stop(context.Background()))

	// Re-init from stopped state should succeed.
	require.NoError(t, c.Init(context.Background(), NewRegistryRecorder(nil, DurabilityDurable)))
	require.NoError(t, c.Start(context.Background()))
	assert.True(t, c.Ready())
}

// ---------------------------------------------------------------------------
// BaseCell shutdownCtx
// ---------------------------------------------------------------------------

func TestBaseCellShutdownCtx(t *testing.T) {
	c := MustNewBaseCell(&metadata.CellMeta{ID: "ctx-test"})

	// Before Start, ShutdownCtx should return context.Background().
	ctx := c.ShutdownCtx()
	require.NotNil(t, ctx)
	assert.Nil(t, ctx.Err(), "context should not be canceled before Start")

	// Start: shutdownCtx is created.
	require.NoError(t, c.Init(context.Background(), NewRegistryRecorder(nil, DurabilityDurable)))
	require.NoError(t, c.Start(context.Background()))

	ctx = c.ShutdownCtx()
	require.NotNil(t, ctx)
	assert.Nil(t, ctx.Err(), "context should not be canceled while running")

	// Stop: shutdownCtx is canceled.
	require.NoError(t, c.Stop(context.Background()))
	assert.Error(t, ctx.Err(), "context should be canceled after Stop")
}

func TestBaseCellConcurrentHealthReady(t *testing.T) {
	c := MustNewBaseCell(&metadata.CellMeta{ID: "concurrent"})
	require.NoError(t, c.Init(context.Background(), NewRegistryRecorder(nil, DurabilityDurable)))
	require.NoError(t, c.Start(context.Background()))

	// Concurrent Health and Ready calls should not race.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 100 {
			_ = c.Health()
			_ = c.Ready()
		}
	}()

	for range 100 {
		_ = c.Health()
		_ = c.Ready()
	}
	<-done

	require.NoError(t, c.Stop(context.Background()))
}

func TestBaseCellConcurrentAddAndRead(t *testing.T) {
	c := MustNewBaseCell(&metadata.CellMeta{ID: "race-add"})

	const n = 100
	done := make(chan struct{})

	// Writer goroutine: adds slices, produced, and consumed contracts.
	go func() {
		defer close(done)
		for range n {
			c.AddSlice(NewBaseSlice("s", "race-add", cellvocab.L0))
			c.AddProducedContract(NewBaseContract("pc", cellvocab.ContractHTTP, "race-add", cellvocab.L1))
			c.AddConsumedContract(NewBaseContract("cc", cellvocab.ContractEvent, "other", cellvocab.L2))
		}
	}()

	// Reader goroutine (main): reads all three lists concurrently.
	for range n {
		_ = c.OwnedSlices()
		_ = c.ProducedContracts()
		_ = c.ConsumedContracts()
	}
	<-done

	assert.Len(t, c.OwnedSlices(), n)
	assert.Len(t, c.ProducedContracts(), n)
	assert.Len(t, c.ConsumedContracts(), n)
}

// ---------------------------------------------------------------------------
// BaseSlice
// ---------------------------------------------------------------------------

func TestBaseSliceAccessors(t *testing.T) {
	s := NewBaseSlice("login-slice", "accesscore", cellvocab.L1)

	assert.Equal(t, "login-slice", s.ID())
	assert.Equal(t, "accesscore", s.BelongsToCell())
	assert.Equal(t, cellvocab.L1, s.ConsistencyLevel())
}

func TestBaseSliceInit(t *testing.T) {
	s := NewBaseSlice("s", "c", cellvocab.L0)
	require.NoError(t, s.Init(context.Background()))
}

func TestBaseSliceVerify(t *testing.T) {
	s := NewBaseSlice("s", "c", cellvocab.L0)

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

func TestBaseSliceAllowedFilesNilWhenUnset(t *testing.T) {
	s := NewBaseSlice("login", "accesscore", cellvocab.L1)
	assert.Nil(t, s.AllowedFiles(), "unset AllowedFiles returns nil — convention defaults are metadata-only (FMT-14)")
}

func TestBaseSliceAllowedFilesExplicit(t *testing.T) {
	s := NewBaseSlice("login", "accesscore", cellvocab.L1)
	custom := []string{"cells/accesscore/slices/login/**"}
	s.SetAllowedFiles(custom)
	assert.Equal(t, custom, s.AllowedFiles())
}

func TestBaseSliceAllowedFilesCopiesSlice(t *testing.T) {
	s := NewBaseSlice("login", "accesscore", cellvocab.L1)
	s.SetAllowedFiles([]string{"a/**", "b/**"})
	got := s.AllowedFiles()
	got[0] = "mutated"
	assert.Equal(t, "a/**", s.AllowedFiles()[0], "AllowedFiles returns a defensive copy")
}

func TestBaseSliceAffectedJourneys(t *testing.T) {
	s := NewBaseSlice("s", "c", cellvocab.L0)

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
	c := NewBaseContract("session-api", cellvocab.ContractHTTP, "accesscore", cellvocab.L2)

	assert.Equal(t, "session-api", c.ID())
	assert.Equal(t, cellvocab.ContractHTTP, c.Kind())
	assert.Equal(t, "accesscore", c.OwnerCell())
	assert.Equal(t, cellvocab.L2, c.ConsistencyLevel())
	assert.Equal(t, cellvocab.LifecycleActive, c.Lifecycle(), "default lifecycle should be active")
}

func TestBaseContractSetLifecycle(t *testing.T) {
	c := NewBaseContract("api-v1", cellvocab.ContractHTTP, "accesscore", cellvocab.L1)
	assert.Equal(t, cellvocab.LifecycleActive, c.Lifecycle(), "default should be active")

	tests := []struct {
		name string
		lc   cellvocab.Lifecycle
	}{
		{"draft", cellvocab.LifecycleDraft},
		{"deprecated", cellvocab.LifecycleDeprecated},
		{"back to active", cellvocab.LifecycleActive},
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
		kind cellvocab.ContractKind
	}{
		{cellvocab.ContractHTTP},
		{cellvocab.ContractEvent},
		{cellvocab.ContractCommand},
		{cellvocab.ContractProjection},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			c := NewBaseContract("id", tt.kind, "owner", cellvocab.L0)
			assert.Equal(t, tt.kind, c.Kind())
		})
	}
}

// TestNewBaseCell_ErrorPaths covers all three NewBaseCell error returns.
func TestNewBaseCell_ErrorPaths(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		meta       *metadata.CellMeta
		wantSubstr string
	}{
		{"nil meta", nil, "meta is nil"},
		{"empty id", &metadata.CellMeta{Type: "core"}, "meta.ID is empty"},
		{"invalid type", &metadata.CellMeta{ID: "x", Type: "wrong"}, "invalid cell type"},
		{"invalid level", &metadata.CellMeta{ID: "x", Type: "core", ConsistencyLevel: "L9"}, "invalid consistency level"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewBaseCell(tt.meta)
			require.Error(t, err)
			require.Nil(t, c)
			var ec *errcode.Error
			require.True(t, errors.As(err, &ec))
			require.Contains(t, ec.Message, tt.wantSubstr)
		})
	}
}

// TestMustNewBaseCell_PanicsOnError verifies the Must* wrapper panics
// with a "cell.MustNewBaseCell:" prefixed message.
func TestMustNewBaseCell_PanicsOnError(t *testing.T) {
	t.Parallel()
	assertPanicsWithAssertionMessage(t,
		"cell.MustNewBaseCell: [ERR_VALIDATION_FAILED] cell.NewBaseCell: meta is nil",
		func() { MustNewBaseCell(nil) },
	)
	// Also verify the prefix on a non-nil but invalid case.
	assert.Panics(t, func() {
		MustNewBaseCell(&metadata.CellMeta{ID: "bad", Type: "wrong"})
	})
}

// TestBaseCell_Metadata_Isolation verifies Metadata() returns an
// independent deep copy: caller mutation must not affect cell state,
// and constructor input mutation must not affect the cell.
func TestBaseCell_Metadata_Isolation(t *testing.T) {
	t.Parallel()
	src := &metadata.CellMeta{
		ID:               "iso",
		Type:             "core",
		ConsistencyLevel: "L1",
		Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.iso.startup"}},
		L0Dependencies:   []metadata.L0DepMeta{{Cell: "shared", Reason: "ok"}},
	}
	c := MustNewBaseCell(src)

	// Constructor input mutation MUST NOT leak into cell.
	src.ID = "mutated"
	src.Verify.Smoke[0] = "tampered"
	src.L0Dependencies[0].Cell = "evil"
	assert.Equal(t, "iso", c.ID(), "constructor mutation leaked into cell ID")
	assert.Equal(t, "smoke.iso.startup", c.Metadata().Verify.Smoke[0])
	assert.Equal(t, "shared", c.Metadata().L0Dependencies[0].Cell)

	// Metadata() return-value mutation MUST NOT leak into cell.
	got := c.Metadata()
	got.ID = "evil"
	got.Verify.Smoke[0] = "tampered2"
	got.L0Dependencies[0].Cell = "evil2"
	assert.Equal(t, "iso", c.ID(), "Metadata() mutation leaked into cell")
	assert.Equal(t, "smoke.iso.startup", c.Metadata().Verify.Smoke[0])
	assert.Equal(t, "shared", c.Metadata().L0Dependencies[0].Cell)
}

// assertPanicsWithAssertionMessage verifies that fn panics with an *errcode.Error
// whose Message field equals wantMsg. Used to test Must* wrappers that now panic
// with errcode.Assertion(...) instead of bare strings.
func assertPanicsWithAssertionMessage(t *testing.T, wantMsg string, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Errorf("expected panic but did not panic")
			return
		}
		e, ok := r.(*errcode.Error)
		if !ok {
			t.Errorf("panic value is %T, want *errcode.Error; value: %v", r, r)
			return
		}
		if e.Message != wantMsg {
			t.Errorf("panic message = %q, want %q", e.Message, wantMsg)
		}
	}()
	fn()
}
