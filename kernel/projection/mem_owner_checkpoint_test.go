package projection_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/kernel/projection"
	"github.com/ghbvf/gocell/kernel/projection/projectiontest"
)

// TestMemOwnerCheckpointStore_CheckpointConformance enrolls MemOwnerCheckpointStore
// in the base CheckpointStore conformance suite (it implements CheckpointStore via
// LoadOffset + SaveOffset). Required by PROJECTION-CHECKPOINT-CONFORMANCE-ENROLL-01.
func TestMemOwnerCheckpointStore_CheckpointConformance(t *testing.T) {
	projectiontest.RunCheckpointConformance(t, projection.NewMemOwnerCheckpointStore())
}

// TestMemOwnerCheckpointStore_OwnerConformance enrolls MemOwnerCheckpointStore in
// the fenced AdvanceIfOwner conformance suite. Required by
// SAGA-OWNER-CHECKPOINT-CONFORMANCE-ENROLL-01.
func TestMemOwnerCheckpointStore_OwnerConformance(t *testing.T) {
	projectiontest.RunOwnerCheckpointConformance(t, projection.NewMemOwnerCheckpointStore())
}

// TestMemOwnerCheckpointStore_FencingTruthTable exercises the semantics-B fencing
// predicate directly (token==owner accept | token!=owner && offset>stored accept |
// otherwise ErrStaleOwner), beyond what the shared conformance covers.
func TestMemOwnerCheckpointStore_FencingTruthTable(t *testing.T) {
	const tokA, tokB = "tok-a", "tok-b"
	tests := []struct {
		name        string
		seedToken   string // "" = cold (no prior advance)
		seedOffset  int64
		token       string
		offset      int64
		wantErr     bool // true => expect ErrStaleOwner
		wantOffset  int64
		wantOwnerOK bool // after the call, token should be able to advance forward
	}{
		{name: "cold claim ahead", seedToken: "", token: tokA, offset: 5, wantOffset: 5, wantOwnerOK: true},
		{name: "same owner forward", seedToken: tokA, seedOffset: 5, token: tokA, offset: 9, wantOffset: 9, wantOwnerOK: true},
		{name: "same owner idempotent same offset", seedToken: tokA, seedOffset: 9, token: tokA, offset: 9, wantOffset: 9, wantOwnerOK: true},
		{name: "same owner backward accepted", seedToken: tokA, seedOffset: 9, token: tokA, offset: 4, wantOffset: 4, wantOwnerOK: true},
		{name: "new leader ahead claims", seedToken: tokA, seedOffset: 9, token: tokB, offset: 12, wantOffset: 12, wantOwnerOK: true},
		{name: "stale owner behind rejected", seedToken: tokB, seedOffset: 12, token: tokA, offset: 10, wantErr: true, wantOffset: 12},
		{name: "stale owner equal rejected", seedToken: tokB, seedOffset: 12, token: tokA, offset: 12, wantErr: true, wantOffset: 12},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := projection.NewMemOwnerCheckpointStore()
			ctx := context.Background()
			const cell, proj = "c", "p"
			if tt.seedToken != "" {
				if err := store.AdvanceIfOwner(ctx, cell, proj, tt.seedToken, tt.seedOffset); err != nil {
					t.Fatalf("seed AdvanceIfOwner(%s, %d): %v", tt.seedToken, tt.seedOffset, err)
				}
			}
			err := store.AdvanceIfOwner(ctx, cell, proj, tt.token, tt.offset)
			switch {
			case tt.wantErr && !errors.Is(err, projection.ErrStaleOwner):
				t.Fatalf("AdvanceIfOwner = %v, want ErrStaleOwner", err)
			case !tt.wantErr && err != nil:
				t.Fatalf("AdvanceIfOwner = %v, want nil", err)
			}
			got, err := store.LoadOffset(ctx, cell, proj)
			if err != nil {
				t.Fatalf("LoadOffset: %v", err)
			}
			if got != tt.wantOffset {
				t.Errorf("LoadOffset = %d, want %d", got, tt.wantOffset)
			}
		})
	}
}

// TestMemOwnerCheckpointStore_EmptyTokenRejected confirms an empty owner token is
// rejected fail-closed (KindInvalid), never silently claiming a cold row.
func TestMemOwnerCheckpointStore_EmptyTokenRejected(t *testing.T) {
	store := projection.NewMemOwnerCheckpointStore()
	err := store.AdvanceIfOwner(context.Background(), "c", "p", "", 5)
	if err == nil {
		t.Fatal("AdvanceIfOwner with empty token = nil, want error")
	}
	if errors.Is(err, projection.ErrStaleOwner) {
		t.Fatalf("empty-token error should be a validation error, not ErrStaleOwner: %v", err)
	}
	got, _ := store.LoadOffset(context.Background(), "c", "p")
	if got != 0 {
		t.Errorf("offset advanced despite empty token: got %d, want 0", got)
	}
}

// TestMemOwnerCheckpointStore_SaveOffsetUnconditional confirms the base
// SaveOffset path is unconditional (the Coordinator-owned contract), independent
// of any owner token — only AdvanceIfOwner is fenced.
func TestMemOwnerCheckpointStore_SaveOffsetUnconditional(t *testing.T) {
	store := projection.NewMemOwnerCheckpointStore()
	ctx := context.Background()
	if err := store.SaveOffset(ctx, "c", "p", 7); err != nil {
		t.Fatalf("SaveOffset(7): %v", err)
	}
	if err := store.SaveOffset(ctx, "c", "p", 3); err != nil {
		t.Fatalf("SaveOffset(3) backward must be accepted: %v", err)
	}
	got, err := store.LoadOffset(ctx, "c", "p")
	if err != nil {
		t.Fatalf("LoadOffset: %v", err)
	}
	if got != 3 {
		t.Errorf("LoadOffset = %d, want 3", got)
	}
}

// compile-time: MemOwnerCheckpointStore satisfies both contracts.
var (
	_ projection.OwnerCheckpointStore = (*projection.MemOwnerCheckpointStore)(nil)
	_ projection.CheckpointStore      = (*projection.MemOwnerCheckpointStore)(nil)
)
