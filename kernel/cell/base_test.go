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
		DurabilityMode:   "durable",
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
		DurabilityMode:   "demo",
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
	c := MustNewBaseCell(&metadata.CellMeta{ID: "test-cell", DurabilityMode: "demo"})

	// Initially empty.
	assert.Empty(t, c.OwnedSlices())
	assert.Empty(t, c.ProducedContracts())
	assert.Empty(t, c.ConsumedContracts())

	// Add slice.
	s := MustNewBaseSliceFromMeta(&metadata.SliceMeta{ID: "s1", BelongsToCell: "test-cell", ConsistencyLevel: "L0"})
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
	c := MustNewBaseCell(&metadata.CellMeta{ID: "copy-test", DurabilityMode: "demo"})
	s := MustNewBaseSliceFromMeta(&metadata.SliceMeta{ID: "s1", BelongsToCell: "copy-test", ConsistencyLevel: "L0"})
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
	c := MustNewBaseCell(&metadata.CellMeta{ID: "r", DurabilityMode: "durable"})

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
	c := MustNewBaseCell(&metadata.CellMeta{ID: "dbl-init", DurabilityMode: "durable"})
	require.NoError(t, c.Init(context.Background(), NewRegistryRecorder(nil, DurabilityDurable)))

	err := c.Init(context.Background(), NewRegistryRecorder(nil, DurabilityDurable))
	require.Error(t, err)
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrLifecycleInvalid, ecErr.Code)
}

func TestBaseCellStartWithoutInit(t *testing.T) {
	c := MustNewBaseCell(&metadata.CellMeta{ID: "no-init", DurabilityMode: "demo"})

	err := c.Start(context.Background())
	require.Error(t, err)
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrLifecycleInvalid, ecErr.Code)
}

func TestBaseCellDoubleStart(t *testing.T) {
	c := MustNewBaseCell(&metadata.CellMeta{ID: "dbl-start", DurabilityMode: "durable"})
	require.NoError(t, c.Init(context.Background(), NewRegistryRecorder(nil, DurabilityDurable)))
	require.NoError(t, c.Start(context.Background()))

	err := c.Start(context.Background())
	require.Error(t, err)
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrLifecycleInvalid, ecErr.Code)
}

func TestBaseCellStopWithoutStart(t *testing.T) {
	c := MustNewBaseCell(&metadata.CellMeta{ID: "no-start", DurabilityMode: "demo"})

	// Stop on a brand-new cell is a no-op.
	require.NoError(t, c.Stop(context.Background()))
}

func TestBaseCellInitThenStopSkipStart(t *testing.T) {
	c := MustNewBaseCell(&metadata.CellMeta{ID: "init-stop", DurabilityMode: "durable"})
	require.NoError(t, c.Init(context.Background(), NewRegistryRecorder(nil, DurabilityDurable)))

	// Stop from initialized is a no-op.
	require.NoError(t, c.Stop(context.Background()))
}

func TestBaseCellRestart(t *testing.T) {
	c := MustNewBaseCell(&metadata.CellMeta{ID: "restart", DurabilityMode: "durable"})

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
	c := MustNewBaseCell(&metadata.CellMeta{ID: "ctx-test", DurabilityMode: "durable"})

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
	c := MustNewBaseCell(&metadata.CellMeta{ID: "concurrent", DurabilityMode: "durable"})
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
	c := MustNewBaseCell(&metadata.CellMeta{ID: "race-add", DurabilityMode: "demo"})

	const n = 100
	done := make(chan struct{})

	// Writer goroutine: adds slices, produced, and consumed contracts.
	go func() {
		defer close(done)
		for range n {
			c.AddSlice(MustNewBaseSliceFromMeta(&metadata.SliceMeta{ID: "s", BelongsToCell: "race-add", ConsistencyLevel: "L0"}))
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
	s := MustNewBaseSliceFromMeta(&metadata.SliceMeta{ID: "login-slice", BelongsToCell: "accesscore", ConsistencyLevel: "L1"})

	assert.Equal(t, "login-slice", s.ID())
	assert.Equal(t, "accesscore", s.BelongsToCell())
	assert.Equal(t, cellvocab.L1, s.ConsistencyLevel())
}

func TestBaseSliceInit(t *testing.T) {
	s := MustNewBaseSliceFromMeta(&metadata.SliceMeta{ID: "s", BelongsToCell: "c", ConsistencyLevel: "L0"})
	require.NoError(t, s.Init(context.Background()))
}

func TestBaseSliceVerify(t *testing.T) {
	s := MustNewBaseSliceFromMeta(&metadata.SliceMeta{ID: "s", BelongsToCell: "c", ConsistencyLevel: "L0"})

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
	s := MustNewBaseSliceFromMeta(&metadata.SliceMeta{ID: "login", BelongsToCell: "accesscore", ConsistencyLevel: "L1"})
	assert.Nil(t, s.AllowedFiles(), "unset AllowedFiles returns nil — convention defaults are metadata-only (FMT-14)")
}

func TestBaseSliceAllowedFilesExplicit(t *testing.T) {
	s := MustNewBaseSliceFromMeta(&metadata.SliceMeta{ID: "login", BelongsToCell: "accesscore", ConsistencyLevel: "L1"})
	custom := []string{"cells/accesscore/slices/login/**"}
	s.SetAllowedFiles(custom)
	assert.Equal(t, custom, s.AllowedFiles())
}

func TestBaseSliceAllowedFilesCopiesSlice(t *testing.T) {
	s := MustNewBaseSliceFromMeta(&metadata.SliceMeta{ID: "login", BelongsToCell: "accesscore", ConsistencyLevel: "L1"})
	s.SetAllowedFiles([]string{"a/**", "b/**"})
	got := s.AllowedFiles()
	got[0] = "mutated"
	assert.Equal(t, "a/**", s.AllowedFiles()[0], "AllowedFiles returns a defensive copy")
}

func TestBaseSliceAffectedJourneys(t *testing.T) {
	s := MustNewBaseSliceFromMeta(&metadata.SliceMeta{ID: "s", BelongsToCell: "c", ConsistencyLevel: "L0"})

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
	assertPanicsWithAssertionMessage(
		t,
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
		DurabilityMode:   "demo",
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

// TestNewBaseSliceFromMeta_ErrorPaths covers all error returns of
// NewBaseSliceFromMeta. Reads slice.yaml metadata (the SoR) into a typed
// BaseSlice; refuses any missing/invalid input (no inheritance fallback).
func TestNewBaseSliceFromMeta_ErrorPaths(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		meta       *metadata.SliceMeta
		wantSubstr string
	}{
		{"nil meta", nil, "meta is nil"},
		{"empty id", &metadata.SliceMeta{BelongsToCell: "c", ConsistencyLevel: "L0"}, "meta.ID is empty"},
		{"empty belongsToCell", &metadata.SliceMeta{ID: "s", ConsistencyLevel: "L0"}, "meta.BelongsToCell is empty"},
		{"empty consistencyLevel", &metadata.SliceMeta{ID: "s", BelongsToCell: "c"}, "meta.ConsistencyLevel is empty"},
		{"invalid level", &metadata.SliceMeta{ID: "s", BelongsToCell: "c", ConsistencyLevel: "L9"}, "invalid consistency level"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := NewBaseSliceFromMeta(tt.meta)
			require.Error(t, err)
			require.Nil(t, s)
			var ec *errcode.Error
			require.True(t, errors.As(err, &ec))
			require.Contains(t, ec.Message, tt.wantSubstr)
		})
	}
}

// TestNewBaseSliceFromMeta_HappyPath covers L0–L4 round-trip.
func TestNewBaseSliceFromMeta_HappyPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		level     string
		wantLevel cellvocab.Level
	}{
		{"L0", "L0", cellvocab.L0},
		{"L1", "L1", cellvocab.L1},
		{"L2", "L2", cellvocab.L2},
		{"L3", "L3", cellvocab.L3},
		{"L4", "L4", cellvocab.L4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := NewBaseSliceFromMeta(&metadata.SliceMeta{
				ID:               "login",
				BelongsToCell:    "accesscore",
				ConsistencyLevel: tt.level,
			})
			require.NoError(t, err)
			require.NotNil(t, s)
			assert.Equal(t, "login", s.ID())
			assert.Equal(t, "accesscore", s.BelongsToCell())
			assert.Equal(t, tt.wantLevel, s.ConsistencyLevel())
		})
	}
}

// TestMustNewBaseSliceFromMeta_PanicsOnError verifies the Must* wrapper panics
// with a "cell.MustNewBaseSliceFromMeta:" prefixed message.
func TestMustNewBaseSliceFromMeta_PanicsOnError(t *testing.T) {
	t.Parallel()
	assertPanicsWithAssertionMessage(
		t,
		"cell.MustNewBaseSliceFromMeta: [ERR_VALIDATION_FAILED] cell.NewBaseSliceFromMeta: meta is nil",
		func() { MustNewBaseSliceFromMeta(nil) },
	)
	// Also verify the prefix on a non-nil but invalid case.
	assert.Panics(t, func() {
		MustNewBaseSliceFromMeta(&metadata.SliceMeta{ID: "bad"})
	})
	// Verify that an invalid consistencyLevel (not L0-L4) also panics.
	assert.Panics(t, func() {
		MustNewBaseSliceFromMeta(&metadata.SliceMeta{ID: "s", BelongsToCell: "c", ConsistencyLevel: "L9"})
	})
}

// TestNewBaseSliceFromMeta_ProjectsVerifyAndAllowedFiles asserts that
// NewBaseSliceFromMeta projects Verify and AllowedFiles from metadata, and
// that the projected values are defensively copied — caller mutation of the
// original meta does not affect the constructed BaseSlice.
func TestNewBaseSliceFromMeta_ProjectsVerifyAndAllowedFiles(t *testing.T) {
	t.Parallel()

	meta := &metadata.SliceMeta{
		ID:               "sessionlogin",
		BelongsToCell:    "accesscore",
		ConsistencyLevel: "L2",
		Verify: metadata.SliceVerifyMeta{
			Unit:     []string{"unit.sessionlogin.service"},
			Contract: []string{"contract.http.auth.login.v1.serve"},
			Waivers: []metadata.WaiverMeta{
				{
					Contract: "http.config.get.v1", Owner: "platform-team",
					Reason: "read-only config call", ExpiresAt: "2026-06-01",
				},
			},
		},
		AllowedFiles: []string{"cells/accesscore/slices/sessionlogin/**"},
	}

	s, err := NewBaseSliceFromMeta(meta)
	require.NoError(t, err)
	require.NotNil(t, s)

	// --- Fields projected ---
	v := s.Verify()
	require.Equal(t, []string{"unit.sessionlogin.service"}, v.Unit)
	require.Equal(t, []string{"contract.http.auth.login.v1.serve"}, v.Contract)
	require.Len(t, v.Waivers, 1)
	assert.Equal(t, "http.config.get.v1", v.Waivers[0].Contract)
	assert.Equal(t, "platform-team", v.Waivers[0].Owner)

	af := s.AllowedFiles()
	require.Equal(t, []string{"cells/accesscore/slices/sessionlogin/**"}, af)

	// --- Defensive copy: mutate meta, BaseSlice must be unaffected ---
	meta.Verify.Unit[0] = "MUTATED"
	meta.Verify.Contract[0] = "MUTATED"
	meta.Verify.Waivers[0].Owner = "MUTATED"
	meta.AllowedFiles[0] = "MUTATED"

	v2 := s.Verify()
	assert.Equal(t, "unit.sessionlogin.service", v2.Unit[0], "meta mutation must not affect BaseSlice.Verify.Unit")
	assert.Equal(t, "contract.http.auth.login.v1.serve", v2.Contract[0], "meta mutation must not affect BaseSlice.Verify.Contract")
	assert.Equal(t, "platform-team", v2.Waivers[0].Owner, "meta mutation must not affect BaseSlice.Verify.Waivers")
	af2 := s.AllowedFiles()
	assert.Equal(t, "cells/accesscore/slices/sessionlogin/**", af2[0], "meta mutation must not affect BaseSlice.AllowedFiles")
}

// TestNewBaseSliceFromMeta_EmptyVerifyAndAllowedFiles asserts that a meta with
// empty Verify and AllowedFiles results in a valid BaseSlice with nil AllowedFiles.
func TestNewBaseSliceFromMeta_EmptyVerifyAndAllowedFiles(t *testing.T) {
	t.Parallel()

	s, err := NewBaseSliceFromMeta(&metadata.SliceMeta{
		ID:               "bare",
		BelongsToCell:    "testcell",
		ConsistencyLevel: "L0",
	})
	require.NoError(t, err)
	require.NotNil(t, s)

	v := s.Verify()
	assert.Empty(t, v.Unit)
	assert.Empty(t, v.Contract)
	assert.Empty(t, v.Waivers)
	assert.Nil(t, s.AllowedFiles(), "AllowedFiles must be nil when not set in meta")
}

// ---------------------------------------------------------------------------
// BaseCell.Init durability alignment
// ---------------------------------------------------------------------------

func TestBaseCell_Init_DurabilityAlignment(t *testing.T) {
	tests := []struct {
		name           string
		metaDurability string // empty triggers construction error
		regMode        DurabilityMode
		wantNewErr     bool // expect NewBaseCell to fail
		wantInitErr    bool // expect Init to fail
		wantDeclared   string
		wantRuntime    string
	}{
		{
			name:           "durable+Durable ok",
			metaDurability: "durable",
			regMode:        DurabilityDurable,
			wantNewErr:     false,
			wantInitErr:    false,
		},
		{
			name:           "demo+Demo ok",
			metaDurability: "demo",
			regMode:        DurabilityDemo,
			wantNewErr:     false,
			wantInitErr:    false,
		},
		{
			name:           "durable+Demo mismatch",
			metaDurability: "durable",
			regMode:        DurabilityDemo,
			wantNewErr:     false,
			wantInitErr:    true,
			wantDeclared:   "durable",
			wantRuntime:    "demo",
		},
		{
			name:           "demo+Durable mismatch",
			metaDurability: "demo",
			regMode:        DurabilityDurable,
			wantNewErr:     false,
			wantInitErr:    true,
			wantDeclared:   "demo",
			wantRuntime:    "durable",
		},
		{
			name:           "empty durabilityMode construction error",
			metaDurability: "",
			regMode:        DurabilityDurable,
			wantNewErr:     true,
		},
		{
			name:           "invalid durabilityMode construction error",
			metaDurability: "banana",
			regMode:        DurabilityDurable,
			wantNewErr:     true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta := &metadata.CellMeta{
				ID:             "alignment-test",
				DurabilityMode: tt.metaDurability,
			}
			c, err := NewBaseCell(meta)
			if tt.wantNewErr {
				require.Error(t, err, "expected NewBaseCell to fail for meta.DurabilityMode=%q", tt.metaDurability)
				return
			}
			require.NoError(t, err)

			reg := NewRegistryRecorder(nil, tt.regMode)
			initErr := c.Init(context.Background(), reg)
			if tt.wantInitErr {
				require.Error(t, initErr)
				var ecErr *errcode.Error
				require.True(t, errors.As(initErr, &ecErr))
				assert.Equal(t, errcode.ErrCellInvalidConfig, ecErr.Code)
				if tt.wantDeclared != "" {
					attr, ok := ecErr.FindAttr("declared")
					assert.True(t, ok, "expected 'declared' detail attr in errcode.Error")
					assert.Equal(t, tt.wantDeclared, attr.Value.String())
				}
				if tt.wantRuntime != "" {
					attr, ok := ecErr.FindAttr("runtime")
					assert.True(t, ok, "expected 'runtime' detail attr in errcode.Error")
					assert.Equal(t, tt.wantRuntime, attr.Value.String())
				}
				// Confirm 'cell' detail is present.
				_, ok := ecErr.FindAttr("cell")
				assert.True(t, ok, "expected 'cell' detail attr in errcode.Error")
			} else {
				require.NoError(t, initErr)
			}
		})
	}
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
