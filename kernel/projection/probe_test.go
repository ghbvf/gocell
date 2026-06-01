package projection

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// probeTestRecentLagOffset is a site-specific deadline offset for the
// "lag below threshold" probe test: an event applied this far in the past is
// well under projectionLagThresholdSeconds (300s), so the probe is healthy.
// Extracted to a package-level const per TEST-TIME-LITERAL-01.
const probeTestRecentLagOffset = 10 * time.Second

// ---------------------------------------------------------------------------
// Probe name constructor tests (F12)
// ---------------------------------------------------------------------------

// TestProjectionStoreReadyProbeName_Format asserts the store-ready probe name
// follows "<cell>_projection_<proj>_store_ready".
func TestProjectionStoreReadyProbeName_Format(t *testing.T) {
	t.Parallel()
	name, err := healthz.ProjectionStoreReadyProbeName("mycell", "myproj")
	if err != nil {
		t.Fatalf("ProjectionStoreReadyProbeName: %v", err)
	}
	if string(name) != "mycell_projection_myproj_store_ready" {
		t.Errorf("name = %q, want %q", name, "mycell_projection_myproj_store_ready")
	}
}

// TestProjectionLagProbeName_Format asserts the lag probe name follows
// "<cell>_projection_<proj>_lag".
func TestProjectionLagProbeName_Format(t *testing.T) {
	t.Parallel()
	name, err := healthz.ProjectionLagProbeName("mycell", "myproj")
	if err != nil {
		t.Fatalf("ProjectionLagProbeName: %v", err)
	}
	if string(name) != "mycell_projection_myproj_lag" {
		t.Errorf("name = %q, want %q", name, "mycell_projection_myproj_lag")
	}
}

// TestProjectionStoreReadyProbeName_LengthBudget asserts names exceeding 64
// chars fail. Budget: 24 fixed chars, so cell+proj must be ≤ 40.
func TestProjectionStoreReadyProbeName_LengthBudget(t *testing.T) {
	t.Parallel()
	longCell := strings.Repeat("a", 21) // 21 chars
	longProj := strings.Repeat("b", 20) // 20 chars → total = 41 > 40
	// "_projection_" (12) + "_store_ready" (12) = 24, so total = 41+24 = 65 > 64
	_, err := healthz.ProjectionStoreReadyProbeName(longCell, longProj)
	if err == nil {
		t.Fatal("expected error for name exceeding length budget, got nil")
	}
}

// TestProjectionStoreReadyProbeName_MaxBudget asserts 40-char cell+proj succeeds.
func TestProjectionStoreReadyProbeName_MaxBudget(t *testing.T) {
	t.Parallel()
	// 40 chars for cell+proj: exactly at budget (24 + 40 = 64).
	cellID := strings.Repeat("a", 20)
	projID := strings.Repeat("b", 20)
	name, err := healthz.ProjectionStoreReadyProbeName(cellID, projID)
	if err != nil {
		t.Fatalf("ProjectionStoreReadyProbeName at budget: %v", err)
	}
	if len(string(name)) != 64 {
		t.Errorf("name len = %d, want 64", len(string(name)))
	}
}

// TestProjectionLagProbeName_LengthBudget asserts names exceeding 64 chars
// fail. Budget: 16 fixed chars, so cell+proj must be ≤ 48.
func TestProjectionLagProbeName_LengthBudget(t *testing.T) {
	t.Parallel()
	longCell := strings.Repeat("a", 25) // 25 chars
	longProj := strings.Repeat("b", 24) // 24 chars → total = 49 > 48
	// "_projection_" (12) + "_lag" (4) = 16, so total = 49+16 = 65 > 64
	_, err := healthz.ProjectionLagProbeName(longCell, longProj)
	if err == nil {
		t.Fatal("expected error for name exceeding length budget, got nil")
	}
}

// TestProjectionLagProbeName_MaxBudget asserts 48-char cell+proj succeeds.
func TestProjectionLagProbeName_MaxBudget(t *testing.T) {
	t.Parallel()
	// 48 chars for cell+proj: exactly at budget (16 + 48 = 64).
	cellID := strings.Repeat("a", 24)
	projID := strings.Repeat("b", 24)
	name, err := healthz.ProjectionLagProbeName(cellID, projID)
	if err != nil {
		t.Fatalf("ProjectionLagProbeName at budget: %v", err)
	}
	if len(string(name)) != 64 {
		t.Errorf("name len = %d, want 64", len(string(name)))
	}
}

// TestProjectionStoreReadyProbeName_EmptyCellID asserts empty cellID returns error.
func TestProjectionStoreReadyProbeName_EmptyCellID(t *testing.T) {
	t.Parallel()
	_, err := healthz.ProjectionStoreReadyProbeName("", "myproj")
	if err == nil {
		t.Fatal("expected error for empty cellID, got nil")
	}
}

// TestProjectionLagProbeName_EmptyProjID asserts empty projID returns error.
func TestProjectionLagProbeName_EmptyProjID(t *testing.T) {
	t.Parallel()
	_, err := healthz.ProjectionLagProbeName("mycell", "")
	if err == nil {
		t.Fatal("expected error for empty projID, got nil")
	}
}

// ---------------------------------------------------------------------------
// Coordinator.Probes — probe names and counts
// ---------------------------------------------------------------------------

// TestCoordinator_Probes_Names asserts Probes() returns 2 probes with the
// expected names.
func TestCoordinator_Probes_Names(t *testing.T) {
	t.Parallel()
	src := NewMemReplaySource()
	clk := clockmock.New(time.Now())
	c := newCoordinatorFull(t, coordinatorFullParams{clk: clk, projectionID: "myproj", cursor: newMemCursor(src), replay: src})

	probes, err := c.Probes()
	if err != nil {
		t.Fatalf("Probes: %v", err)
	}
	if len(probes) != 2 {
		t.Fatalf("Probes() returned %d probes, want 2", len(probes))
	}
	wantStore := "testcell_projection_myproj_store_ready"
	wantLag := "testcell_projection_myproj_lag"
	if string(probes[0].Name()) != wantStore {
		t.Errorf("probes[0].Name() = %q, want %q", probes[0].Name(), wantStore)
	}
	if string(probes[1].Name()) != wantLag {
		t.Errorf("probes[1].Name() = %q, want %q", probes[1].Name(), wantLag)
	}
}

// ---------------------------------------------------------------------------
// checkStoreReady tests
// ---------------------------------------------------------------------------

// TestCoordinator_StoreReady_Healthy asserts store-ready probe is healthy
// when Head and LoadOffset both succeed.
func TestCoordinator_StoreReady_Healthy(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(time.Now())
	src := NewMemReplaySource()
	cur := newMemCursor(src)
	store := NewMemCheckpointStore()

	c := newCoordinatorFull(t, coordinatorFullParams{clk: clk, projectionID: "myproj", store: store, cursor: cur, replay: src})
	subscribeWithDefaults(t, c, applyNoop)

	probes, err := c.Probes()
	if err != nil {
		t.Fatalf("Probes: %v", err)
	}
	storeReady := probes[0]

	// Head=0, LoadOffset=0 — both succeed → healthy.
	if err := storeReady.Check(context.Background()); err != nil {
		t.Errorf("storeReady.Check: expected nil (healthy), got %v", err)
	}
}

// TestCoordinator_StoreReady_HeadError asserts store-ready probe is unhealthy
// when Head returns an error.
func TestCoordinator_StoreReady_HeadError(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(time.Now())
	errSrc := &errHeadReplaySource{headErr: errors.New("head unavailable")}
	store := NewMemCheckpointStore()
	cur := &fakeCursor{pos: 1}

	c := newCoordinatorFull(t, coordinatorFullParams{clk: clk, projectionID: "myproj", store: store, cursor: cur, replay: errSrc})
	subscribeWithDefaults(t, c, applyNoop)

	probes, err := c.Probes()
	if err != nil {
		t.Fatalf("Probes: %v", err)
	}
	storeReady := probes[0]

	checkErr := storeReady.Check(context.Background())
	if checkErr == nil {
		t.Fatal("storeReady.Check: expected error (Head unavailable), got nil")
	}
	var ec *errcode.Error
	if !errors.As(checkErr, &ec) {
		t.Errorf("error is not *errcode.Error: %T %v", checkErr, checkErr)
	}
}

// ---------------------------------------------------------------------------
// checkLag tests
// ---------------------------------------------------------------------------

// TestCoordinator_Lag_Idle asserts lag probe is healthy when pending = 0.
func TestCoordinator_Lag_Idle(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(time.Now())
	src := NewMemReplaySource()
	cur := newMemCursor(src)
	store := NewMemCheckpointStore()

	c := newCoordinatorFull(t, coordinatorFullParams{clk: clk, projectionID: "myproj", store: store, cursor: cur, replay: src})
	subscribeWithDefaults(t, c, applyNoop)

	probes, err := c.Probes()
	if err != nil {
		t.Fatalf("Probes: %v", err)
	}
	lagProbe := probes[1]

	// Head=0, checkpoint=0, pending=0 → idle → healthy.
	if err := lagProbe.Check(context.Background()); err != nil {
		t.Errorf("lagProbe.Check: expected nil (idle), got %v", err)
	}
}

// TestCoordinator_Lag_ColdStartHealthy asserts lag probe is healthy on cold
// start (nothing applied yet, last==0).
func TestCoordinator_Lag_ColdStartHealthy(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(time.Now())
	src := NewMemReplaySource()
	cur := newMemCursor(src)
	store := NewMemCheckpointStore()

	clk2 := clockmock.New(time.Now())
	src.Append(mustNewTestEntry(t, clk2, "topic.v1"))
	src.Append(mustNewTestEntry(t, clk2, "topic.v1"))

	c := newCoordinatorFull(t, coordinatorFullParams{clk: clk, projectionID: "myproj", store: store, cursor: cur, replay: src})
	subscribeWithDefaults(t, c, applyNoop)

	probes, err := c.Probes()
	if err != nil {
		t.Fatalf("Probes: %v", err)
	}
	lagProbe := probes[1]

	// head=2, checkpoint=0, pending=2, last==0 → startup grace → healthy.
	if err := lagProbe.Check(context.Background()); err != nil {
		t.Errorf("lagProbe.Check: expected nil (startup grace), got %v", err)
	}
}

// TestCoordinator_Lag_NegativePending_Warn asserts lag probe is healthy when
// checkpoint > head (anomaly) and does NOT write a negative gauge (F13).
func TestCoordinator_Lag_NegativePending_Warn(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(time.Now())
	src := NewMemReplaySource()
	cur := newMemCursor(src)
	store := NewMemCheckpointStore()

	c := newCoordinatorFull(t, coordinatorFullParams{clk: clk, projectionID: "myproj", store: store, cursor: cur, replay: src})
	subscribeWithDefaults(t, c, applyNoop)

	// Set checkpoint > head (anomaly: head=0, checkpoint=5).
	if err := store.SaveOffset(context.Background(), "testcell", "myproj", 5); err != nil {
		t.Fatalf("SaveOffset: %v", err)
	}

	probes, err := c.Probes()
	if err != nil {
		t.Fatalf("Probes: %v", err)
	}
	lagProbe := probes[1]

	// pending < 0 → should be healthy (F13: no negative gauge, just warn+healthy).
	if err := lagProbe.Check(context.Background()); err != nil {
		t.Errorf("lagProbe.Check: expected nil (negative-pending anomaly → healthy), got %v", err)
	}
}

// TestCoordinator_Lag_Unhealthy asserts lag probe is unhealthy when
// lag > projectionLagThresholdSeconds.
func TestCoordinator_Lag_Unhealthy(t *testing.T) {
	t.Parallel()
	farPast := time.Now().Add(-(projectionLagThresholdSeconds + 60) * time.Second)
	clk := clockmock.New(time.Now())
	src := NewMemReplaySource()
	cur := newMemCursor(src)
	store := NewMemCheckpointStore()

	clk2 := clockmock.New(time.Now())
	src.Append(mustNewTestEntry(t, clk2, "topic.v1"))
	src.Append(mustNewTestEntry(t, clk2, "topic.v1"))

	c := newCoordinatorFull(t, coordinatorFullParams{clk: clk, projectionID: "myproj", store: store, cursor: cur, replay: src})
	subscribeWithDefaults(t, c, applyNoop)

	// pending=1, last=farPast → lag > threshold → unhealthy.
	c.lastAppliedUnixNano.Store(farPast.UnixNano())
	if err := store.SaveOffset(context.Background(), "testcell", "myproj", 1); err != nil {
		t.Fatalf("SaveOffset: %v", err)
	}

	probes, err := c.Probes()
	if err != nil {
		t.Fatalf("Probes: %v", err)
	}
	lagProbe := probes[1]

	checkErr := lagProbe.Check(context.Background())
	if checkErr == nil {
		t.Fatal("lagProbe.Check: expected error (lag > threshold), got nil")
	}
	var ec *errcode.Error
	if !errors.As(checkErr, &ec) {
		t.Errorf("error is not *errcode.Error: %T %v", checkErr, checkErr)
	}
}

// TestCoordinator_Lag_BelowThreshold asserts lag probe is healthy when
// lag ≤ threshold (F14: cell/projection in Internal, not Details).
func TestCoordinator_Lag_BelowThreshold(t *testing.T) {
	t.Parallel()
	recentPast := time.Now().Add(-probeTestRecentLagOffset)
	clk := clockmock.New(time.Now())
	src := NewMemReplaySource()
	cur := newMemCursor(src)
	store := NewMemCheckpointStore()

	clk2 := clockmock.New(time.Now())
	src.Append(mustNewTestEntry(t, clk2, "topic.v1"))
	src.Append(mustNewTestEntry(t, clk2, "topic.v1"))

	c := newCoordinatorFull(t, coordinatorFullParams{clk: clk, projectionID: "myproj", store: store, cursor: cur, replay: src})
	subscribeWithDefaults(t, c, applyNoop)

	c.lastAppliedUnixNano.Store(recentPast.UnixNano())
	if err := store.SaveOffset(context.Background(), "testcell", "myproj", 1); err != nil {
		t.Fatalf("SaveOffset: %v", err)
	}

	probes, err := c.Probes()
	if err != nil {
		t.Fatalf("Probes: %v", err)
	}
	lagProbe := probes[1]

	// lag ≈ 10s < 300s → healthy.
	if err := lagProbe.Check(context.Background()); err != nil {
		t.Errorf("lagProbe.Check: expected nil (lag below threshold), got %v", err)
	}
}
