package command

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	kout "github.com/ghbvf/gocell/framework/kernel/outbox"
)

// captureEmitter is a minimal kout.Emitter that records the last entry it was
// asked to emit, for asserting EmitAsync's entry shape.
type captureEmitter struct {
	entry kout.Entry
	calls int
	err   error
}

func (c *captureEmitter) Emit(_ context.Context, e kout.Entry) error {
	c.entry = e
	c.calls++
	return c.err
}

func TestEmitAsync_EntryShape(t *testing.T) {
	t.Parallel()

	em := &captureEmitter{}
	clk := clock.Real()
	const (
		dispatchID = CommandID("command.remotecommand.v1")
		subject    = "device-7"
		commandID  = "instance-abc"
	)

	payload := struct {
		Foo string `json:"foo"`
	}{Foo: "bar"}

	if err := EmitAsync(context.Background(), clk, em, dispatchID, subject, commandID, payload); err != nil {
		t.Fatalf("EmitAsync returned error: %v", err)
	}
	if em.calls != 1 {
		t.Fatalf("emitter.Emit called %d times, want 1", em.calls)
	}

	got := em.entry
	if got.RoutingTopic() != string(dispatchID) {
		t.Errorf("RoutingTopic() = %q, want %q", got.RoutingTopic(), string(dispatchID))
	}
	if got.AggregateID() != subject {
		t.Errorf("AggregateID() = %q, want %q", got.AggregateID(), subject)
	}
	md := got.Metadata()
	if md[CommandIDMetadataKey] != commandID {
		t.Errorf("Metadata[%q] = %q, want %q", CommandIDMetadataKey, md[CommandIDMetadataKey], commandID)
	}
}

func TestEmitAsync_PropagatesEmitterError(t *testing.T) {
	t.Parallel()

	em := &captureEmitter{err: context.Canceled}
	err := EmitAsync(context.Background(), clock.Real(), em,
		CommandID("command.x.v1"), "sub", "cmd", struct{}{})
	if err == nil {
		t.Fatal("EmitAsync should propagate emitter error, got nil")
	}
}

func TestClaimKeyFromEntry_RoundTrip(t *testing.T) {
	t.Parallel()

	em := &captureEmitter{}
	const (
		dispatchID = CommandID("command.remotecommand.v1")
		subject    = "device-7"
		commandID  = "instance-abc"
	)
	if err := EmitAsync(context.Background(), clock.Real(), em, dispatchID, subject, commandID, struct{}{}); err != nil {
		t.Fatalf("EmitAsync: %v", err)
	}

	key, ok := ClaimKeyFromEntry(em.entry)
	if !ok {
		t.Fatal("ClaimKeyFromEntry ok=false for entry with full identity, want true")
	}
	if key == "" {
		t.Fatal("ClaimKeyFromEntry returned empty key")
	}

	// Stable: same identity → same key.
	key2, ok2 := ClaimKeyFromEntry(em.entry)
	if !ok2 || key2 != key {
		t.Errorf("ClaimKeyFromEntry not stable: (%q,%v) vs (%q,%v)", key, ok, key2, ok2)
	}
}

func TestClaimKeyFromEntry_TenantlessUsesNoTenantSentinel(t *testing.T) {
	t.Parallel()

	em := &captureEmitter{}
	const (
		dispatchID = CommandID("command.remotecommand.v1")
		subject    = "device-7"
		commandID  = "source-event-1"
	)
	if err := EmitAsync(context.Background(), clock.Real(), em, dispatchID, subject, commandID, struct{}{}); err != nil {
		t.Fatalf("EmitAsync: %v", err)
	}

	key, ok := ClaimKeyFromEntry(em.entry)
	if !ok {
		t.Fatal("ClaimKeyFromEntry ok=false for tenantless entry with subject and commandID")
	}
	if want := "_notenant\x00device-7\x00source-event-1"; key != want {
		t.Fatalf("ClaimKeyFromEntry key = %q, want %q", key, want)
	}
}

func TestClaimKeyFromEntry_MissingIdentity(t *testing.T) {
	t.Parallel()

	clk := clock.Real()

	// Missing commandID metadata: aggregateID set, no metadata key.
	missingCmd, err := kout.NewEntry(clk, context.Background(), "command.x.v1", []byte(`{}`),
		kout.WithAggregateID("sub"))
	if err != nil {
		t.Fatalf("NewEntry: %v", err)
	}
	if _, ok := ClaimKeyFromEntry(missingCmd); ok {
		t.Error("ClaimKeyFromEntry ok=true for entry missing commandID, want false")
	}

	// Missing subject: metadata key set, no aggregateID.
	missingSub, err := kout.NewEntry(clk, context.Background(), "command.x.v1", []byte(`{}`),
		kout.WithMetadata(map[string]string{CommandIDMetadataKey: "cmd"}))
	if err != nil {
		t.Fatalf("NewEntry: %v", err)
	}
	if _, ok := ClaimKeyFromEntry(missingSub); ok {
		t.Error("ClaimKeyFromEntry ok=true for entry missing subject, want false")
	}
}

// TestCommandIDMetadataKey_NotReserved asserts the command idempotency metadata
// key does not collide with a kernel-reserved metadata key (which Entry.Validate
// would reject at construction).
func TestCommandIDMetadataKey_NotReserved(t *testing.T) {
	t.Parallel()
	for _, k := range kout.ReservedMetadataKeys {
		if k == CommandIDMetadataKey {
			t.Fatalf("CommandIDMetadataKey %q collides with a reserved metadata key", CommandIDMetadataKey)
		}
	}
}

// TestCommandDeadlineMetadataKey_NotReserved asserts the deadline metadata key
// does not collide with any kernel-reserved metadata key.
func TestCommandDeadlineMetadataKey_NotReserved(t *testing.T) {
	t.Parallel()
	for _, k := range kout.ReservedMetadataKeys {
		if k == CommandDeadlineMetadataKey {
			t.Fatalf("CommandDeadlineMetadataKey %q collides with a reserved metadata key", CommandDeadlineMetadataKey)
		}
	}
}

// activeUniquenessDeadline is a fixed future deadline used across
// WithActiveUniqueness tests (TEST-TIME-LITERAL-01: no inline duration literals).
var activeUniquenessDeadline = time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)

// TestEmitAsync_WithActiveUniqueness_WritesDeadlineMetadata asserts that calling
// EmitAsync with WithActiveUniqueness writes both CommandIDMetadataKey and
// CommandDeadlineMetadataKey into the entry metadata, with the deadline as
// RFC3339Nano UTC.
func TestEmitAsync_WithActiveUniqueness_WritesDeadlineMetadata(t *testing.T) {
	t.Parallel()

	em := &captureEmitter{}
	const (
		dispatchID = CommandID("command.remotecommand.v1")
		subject    = "device-8"
		commandID  = "instance-xyz"
	)

	if err := EmitAsync(context.Background(), clock.Real(), em, dispatchID, subject, commandID, struct{}{},
		WithActiveUniqueness(activeUniquenessDeadline)); err != nil {
		t.Fatalf("EmitAsync with WithActiveUniqueness returned error: %v", err)
	}
	if em.calls != 1 {
		t.Fatalf("emitter.Emit called %d times, want 1", em.calls)
	}

	md := em.entry.Metadata()
	if md[CommandIDMetadataKey] != commandID {
		t.Errorf("Metadata[CommandIDMetadataKey] = %q, want %q", md[CommandIDMetadataKey], commandID)
	}
	wantDL := activeUniquenessDeadline.UTC().Format(time.RFC3339Nano)
	if md[CommandDeadlineMetadataKey] != wantDL {
		t.Errorf("Metadata[CommandDeadlineMetadataKey] = %q, want %q", md[CommandDeadlineMetadataKey], wantDL)
	}
}

// TestEmitAsync_WithActiveUniqueness_ZeroDeadline_ReturnsError asserts that
// WithActiveUniqueness with a zero deadline causes EmitAsync to return an error
// before reaching the emitter (coupling guard).
func TestEmitAsync_WithActiveUniqueness_ZeroDeadline_ReturnsError(t *testing.T) {
	t.Parallel()

	em := &captureEmitter{}
	err := EmitAsync(context.Background(), clock.Real(), em,
		CommandID("command.x.v1"), "sub", "cmd", struct{}{},
		WithActiveUniqueness(time.Time{}))
	if err == nil {
		t.Fatal("EmitAsync with zero deadline must return an error, got nil")
	}
	if em.calls != 0 {
		t.Errorf("emitter.Emit called %d times, want 0 (error must be returned before emit)", em.calls)
	}
}

// TestWithDispatchedUniqueness_RoundTrip asserts that WithDispatchedUniqueness
// injects (key, deadline) into ctx and DispatchedUniqueness retrieves them with
// ok=true and the same values.
func TestWithDispatchedUniqueness_RoundTrip(t *testing.T) {
	t.Parallel()

	const wantKey = "_notenant\x00device-9\x00cmd-99"
	wantDL := activeUniquenessDeadline

	ctx := WithDispatchedUniqueness(context.Background(), wantKey, wantDL)
	gotKey, gotDL, ok := DispatchedUniqueness(ctx)
	if !ok {
		t.Fatal("DispatchedUniqueness ok=false after WithDispatchedUniqueness, want true")
	}
	if gotKey != wantKey {
		t.Errorf("DispatchedUniqueness key = %q, want %q", gotKey, wantKey)
	}
	if !gotDL.Equal(wantDL) {
		t.Errorf("DispatchedUniqueness deadline = %v, want %v", gotDL, wantDL)
	}
}

// TestDispatchedUniqueness_AbsentOnBareContext asserts DispatchedUniqueness
// returns ok=false on a plain context that has not had WithDispatchedUniqueness
// called on it.
func TestDispatchedUniqueness_AbsentOnBareContext(t *testing.T) {
	t.Parallel()

	key, dl, ok := DispatchedUniqueness(context.Background())
	if ok {
		t.Errorf("DispatchedUniqueness ok=true on bare context, want false (key=%q, dl=%v)", key, dl)
	}
}

// TestEmitAsync_NoOpts_AbsentsDeadlineMetadata asserts that EmitAsync without
// opts does NOT write CommandDeadlineMetadataKey (backward-compatibility).
func TestEmitAsync_NoOpts_AbsentsDeadlineMetadata(t *testing.T) {
	t.Parallel()

	em := &captureEmitter{}
	if err := EmitAsync(context.Background(), clock.Real(), em,
		CommandID("command.x.v1"), "sub", "cmd", struct{}{}); err != nil {
		t.Fatalf("EmitAsync returned error: %v", err)
	}
	md := em.entry.Metadata()
	if _, found := md[CommandDeadlineMetadataKey]; found {
		t.Errorf("CommandDeadlineMetadataKey present in metadata without WithActiveUniqueness, want absent")
	}
}
