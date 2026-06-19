package command

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	idemkey "github.com/ghbvf/gocell/framework/runtime/http/idempotency"
)

// TestEmitAsyncFromIdempotencyKey_SourcesCommandIDFromIdentity asserts the
// HTTP→command bridge (#1610) derives the per-instance commandID from the sealed
// RequestIdentity's composite token — not from a caller parameter — so the
// subject/commandID transpose footgun is inexpressible on this path.
func TestEmitAsyncFromIdempotencyKey_SourcesCommandIDFromIdentity(t *testing.T) {
	t.Parallel()

	em := &captureEmitter{}
	id, err := idemkey.NewRequestIdentity("alice", "fp1", "idem-key-9")
	if err != nil {
		t.Fatalf("NewRequestIdentity: %v", err)
	}
	ctx := idemkey.WithRequestIdentity(context.Background(), id)
	if err := EmitAsyncFromIdempotencyKey(ctx, clock.Real(), em,
		CommandID("command.remotecommand.v1"), "device-7", struct{}{}); err != nil {
		t.Fatalf("EmitAsyncFromIdempotencyKey: %v", err)
	}
	if em.calls != 1 {
		t.Fatalf("emitter.Emit called %d times, want 1", em.calls)
	}
	if got := em.entry.Metadata()[CommandIDMetadataKey]; got != id.CommandDedupToken() {
		t.Errorf("commandID metadata = %q, want composite dedup token %q", got, id.CommandDedupToken())
	}
	if got := em.entry.AggregateID(); got != "device-7" {
		t.Errorf("AggregateID = %q, want device-7", got)
	}
}

// TestEmitAsyncFromIdempotencyKey_FailClosedNoIdentity: absent RequestIdentity
// fail-closes as KindInvalid (400) with no emit. (Malformed-key fail-closure is
// enforced upstream by the sealed NewRequestIdentity constructor — see
// runtime/http/idempotency request_identity_test.go.)
func TestEmitAsyncFromIdempotencyKey_FailClosedNoIdentity(t *testing.T) {
	t.Parallel()

	em := &captureEmitter{}
	err := EmitAsyncFromIdempotencyKey(context.Background(), clock.Real(), em,
		CommandID("command.x.v1"), "sub", struct{}{})
	if err == nil {
		t.Fatal("expected fail-closed error when ctx carries no RequestIdentity")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) || ec.Kind != errcode.KindInvalid {
		t.Fatalf("fail-closed error must be *errcode.Error KindInvalid (400), got %#v", err)
	}
	if em.calls != 0 {
		t.Fatalf("emitter must not be called on fail-closed; calls=%d", em.calls)
	}
}
