package command

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	idemkey "github.com/ghbvf/gocell/runtime/http/idempotency"
)

// TestEmitAsyncFromIdempotencyKey_SourcesCommandIDFromCtx asserts the HTTP→command
// bridge (#1610) sources the per-instance commandID from the validated
// Idempotency-Key in ctx — not from a caller parameter — so the
// subject/commandID transpose footgun is inexpressible on this path.
func TestEmitAsyncFromIdempotencyKey_SourcesCommandIDFromCtx(t *testing.T) {
	t.Parallel()

	em := &captureEmitter{}
	ctx := idemkey.WithKey(context.Background(), "idem-key-9")
	err := EmitAsyncFromIdempotencyKey(ctx, clock.Real(), em,
		CommandID("command.devicecommand.enqueue.v1"), "device-7", struct{}{})
	if err != nil {
		t.Fatalf("EmitAsyncFromIdempotencyKey: %v", err)
	}
	if em.calls != 1 {
		t.Fatalf("emitter.Emit called %d times, want 1", em.calls)
	}
	if got := em.entry.Metadata()[CommandIDMetadataKey]; got != "idem-key-9" {
		t.Errorf("commandID metadata = %q, want idem-key-9 (sourced from ctx idem key)", got)
	}
	if got := em.entry.AggregateID(); got != "device-7" {
		t.Errorf("AggregateID = %q, want device-7", got)
	}
}

// TestEmitAsyncFromIdempotencyKey_FailClosed asserts the bridge fail-closes (no
// emit) when the ctx carries no usable Idempotency-Key.
func TestEmitAsyncFromIdempotencyKey_FailClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ctx  context.Context
	}{
		{"no key in ctx", context.Background()},
		{"empty key", idemkey.WithKey(context.Background(), "")},
		{"brace in key", idemkey.WithKey(context.Background(), "ab{c}")},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			em := &captureEmitter{}
			err := EmitAsyncFromIdempotencyKey(tc.ctx, clock.Real(), em,
				CommandID("command.x.v1"), "sub", struct{}{})
			if err == nil {
				t.Fatal("expected fail-closed error, got nil")
			}
			// Must be KindInvalid so the handler renders it as a 400 (not 5xx).
			var ec *errcode.Error
			if !errors.As(err, &ec) || ec.Kind != errcode.KindInvalid {
				t.Fatalf("fail-closed error must be *errcode.Error KindInvalid (400), got %#v", err)
			}
			if em.calls != 0 {
				t.Fatalf("emitter must not be called on fail-closed; calls=%d", em.calls)
			}
		})
	}
}
