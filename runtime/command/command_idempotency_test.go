package command

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/kernel/clock"
	kout "github.com/ghbvf/gocell/kernel/outbox"
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
		dispatchID = CommandID("command.devicecommand.enqueue.v1")
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
		dispatchID = CommandID("command.devicecommand.enqueue.v1")
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
		dispatchID = CommandID("command.devicecommand.enqueue.v1")
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
