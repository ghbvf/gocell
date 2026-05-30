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

// TestProjectionReadyProbeName_Format asserts the probe name follows the
// expected format "<cell>_projection_<proj>_ready".
func TestProjectionReadyProbeName_Format(t *testing.T) {
	t.Parallel()
	name, err := healthz.ProjectionReadyProbeName("mycell", "myproj")
	if err != nil {
		t.Fatalf("ProjectionReadyProbeName: %v", err)
	}
	if string(name) != "mycell_projection_myproj_ready" {
		t.Errorf("name = %q, want %q", name, "mycell_projection_myproj_ready")
	}
}

// TestProjectionReadyProbeName_LengthBudget asserts names exceeding the 64-char
// limit fail with an error (budget: fixed=18 chars, so cell+proj must be ≤ 46).
func TestProjectionReadyProbeName_LengthBudget(t *testing.T) {
	t.Parallel()
	// 47-char combination of cell+proj (budget overflow).
	longCell := strings.Repeat("a", 24) // 24 chars
	longProj := strings.Repeat("b", 23) // 23 chars → total = 47 > 46
	// "_projection_" (12) + "_ready" (6) = 18, so total = 47+18 = 65 > 64
	_, err := healthz.ProjectionReadyProbeName(longCell, longProj)
	if err == nil {
		t.Fatal("expected error for name exceeding length budget, got nil")
	}
}

// TestProjectionReadyProbeName_MaxBudget asserts 46-char combination succeeds.
func TestProjectionReadyProbeName_MaxBudget(t *testing.T) {
	t.Parallel()
	// 46 chars for cell+proj: exactly at budget (18 + 46 = 64).
	cellID := strings.Repeat("a", 23)
	projID := strings.Repeat("b", 23)
	name, err := healthz.ProjectionReadyProbeName(cellID, projID)
	if err != nil {
		t.Fatalf("ProjectionReadyProbeName at budget: %v", err)
	}
	if len(string(name)) != 64 {
		t.Errorf("name len = %d, want 64", len(string(name)))
	}
}

// TestProjectionReadyProbeName_EmptyCellID asserts empty cellID returns error.
func TestProjectionReadyProbeName_EmptyCellID(t *testing.T) {
	t.Parallel()
	_, err := healthz.ProjectionReadyProbeName("", "myproj")
	if err == nil {
		t.Fatal("expected error for empty cellID, got nil")
	}
}

// TestProjectionReadyProbeName_EmptyProjID asserts empty projID returns error.
func TestProjectionReadyProbeName_EmptyProjID(t *testing.T) {
	t.Parallel()
	_, err := healthz.ProjectionReadyProbeName("mycell", "")
	if err == nil {
		t.Fatal("expected error for empty projID, got nil")
	}
}

// TestCoordinator_ReadinessProbe_Healthy asserts ReadinessProbe returns healthy
// (nil) when there are no pending events (head == checkpoint).
func TestCoordinator_ReadinessProbe_Healthy(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(time.Now())
	src := NewMemReplaySource()
	cur := newMemCursor(src)
	store := NewMemCheckpointStore()

	c := newCoordinatorFull(t, clk, "myproj", &fakeRegistrar{}, &fakeTxRunner{}, store, cur, src)
	subscribeWithDefaults(t, c, applyNoop)

	probe, err := c.ReadinessProbe()
	if err != nil {
		t.Fatalf("ReadinessProbe: %v", err)
	}

	// Head=0, checkpoint=0, pending=0 → healthy.
	if err := probe.Check(context.Background()); err != nil {
		t.Errorf("Check: expected nil (healthy), got %v", err)
	}
}

// TestCoordinator_ReadinessProbe_ColdStartHealthy asserts ReadinessProbe returns
// healthy (nil) on cold start (nothing applied yet, last==0).
func TestCoordinator_ReadinessProbe_ColdStartHealthy(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(time.Now())
	src := NewMemReplaySource()
	cur := newMemCursor(src)
	store := NewMemCheckpointStore()

	// Seed entries in source but don't apply any.
	clk2 := clockmock.New(time.Now())
	src.Append(mustNewTestEntry(t, clk2, "topic.v1"))
	src.Append(mustNewTestEntry(t, clk2, "topic.v1"))

	c := newCoordinatorFull(t, clk, "myproj", &fakeRegistrar{}, &fakeTxRunner{}, store, cur, src)
	subscribeWithDefaults(t, c, applyNoop)

	probe, err := c.ReadinessProbe()
	if err != nil {
		t.Fatalf("ReadinessProbe: %v", err)
	}

	// head=2, checkpoint=0, pending=2, last==0 → startup grace → healthy.
	if err := probe.Check(context.Background()); err != nil {
		t.Errorf("Check: expected nil (startup grace), got %v", err)
	}
}

// TestCoordinator_ReadinessProbe_LagUnhealthy asserts ReadinessProbe returns
// unhealthy when lag > projectionLagThresholdSeconds.
func TestCoordinator_ReadinessProbe_LagUnhealthy(t *testing.T) {
	t.Parallel()
	// Clock starts far in the past (> threshold ago).
	farPast := time.Now().Add(-(projectionLagThresholdSeconds + 60) * time.Second)
	clk := clockmock.New(time.Now())
	src := NewMemReplaySource()
	cur := newMemCursor(src)
	store := NewMemCheckpointStore()

	// Append one entry with OccurredAt = far past.
	pastClk := clockmock.New(farPast)
	entry := mustNewTestEntry(t, pastClk, "topic.v1")
	src.Append(entry)

	c := newCoordinatorFull(t, clk, "myproj", &fakeRegistrar{}, &fakeTxRunner{}, store, cur, src)
	subscribeWithDefaults(t, c, applyNoop)

	// Simulate having applied entry 1 (checkpoint=1) by recording its OccurredAt.
	// We do this by storing the nanotime directly.
	c.lastAppliedUnixNano.Store(farPast.UnixNano())
	// Set checkpoint to 1 so pending = 0 but lag > threshold.
	if err := store.SaveOffset(context.Background(), "testcell", "myproj", 1); err != nil {
		t.Fatalf("SaveOffset: %v", err)
	}

	probe, err := c.ReadinessProbe()
	if err != nil {
		t.Fatalf("ReadinessProbe: %v", err)
	}

	// lag > threshold → unhealthy.
	checkErr := probe.Check(context.Background())
	if checkErr == nil {
		t.Fatal("Check: expected error (lag > threshold), got nil")
	}
	var ec *errcode.Error
	if !errors.As(checkErr, &ec) {
		t.Errorf("error is not *errcode.Error: %T %v", checkErr, checkErr)
	}
}

// TestCoordinator_ReadinessProbe_LagBelowThresholdHealthy asserts probe is healthy
// when lag ≤ threshold.
func TestCoordinator_ReadinessProbe_LagBelowThresholdHealthy(t *testing.T) {
	t.Parallel()
	// OccurredAt = 10 seconds ago (well below 300s threshold).
	recentPast := time.Now().Add(-10 * time.Second)
	clk := clockmock.New(time.Now())
	src := NewMemReplaySource()
	cur := newMemCursor(src)
	store := NewMemCheckpointStore()

	// Apply entry with recent OccurredAt.
	c := newCoordinatorFull(t, clk, "myproj", &fakeRegistrar{}, &fakeTxRunner{}, store, cur, src)
	subscribeWithDefaults(t, c, applyNoop)
	c.lastAppliedUnixNano.Store(recentPast.UnixNano())
	if err := store.SaveOffset(context.Background(), "testcell", "myproj", 1); err != nil {
		t.Fatalf("SaveOffset: %v", err)
	}

	probe, err := c.ReadinessProbe()
	if err != nil {
		t.Fatalf("ReadinessProbe: %v", err)
	}

	// lag ≈ 10s < 300s → healthy.
	if err := probe.Check(context.Background()); err != nil {
		t.Errorf("Check: expected nil (lag below threshold), got %v", err)
	}
}

// TestCoordinator_ReadinessProbe_ProbeName asserts the probe name follows the
// expected format "<cell>_projection_<proj>_ready".
func TestCoordinator_ReadinessProbe_ProbeName(t *testing.T) {
	t.Parallel()
	src := NewMemReplaySource()
	clk := clockmock.New(time.Now())
	c := newCoordinatorFull(t, clk, "myproj", &fakeRegistrar{}, &fakeTxRunner{}, NewMemCheckpointStore(), newMemCursor(src), src)

	probe, err := c.ReadinessProbe()
	if err != nil {
		t.Fatalf("ReadinessProbe: %v", err)
	}

	wantName := "testcell_projection_myproj_ready"
	if string(probe.Name()) != wantName {
		t.Errorf("probe.Name() = %q, want %q", probe.Name(), wantName)
	}
}
