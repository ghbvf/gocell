package devicecmd

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	cmdenqueue "github.com/ghbvf/gocell/generated/contracts/command/devicecommand/enqueue/v1"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
	"github.com/ghbvf/gocell/kernel/outbox/outboxtest"
	"github.com/ghbvf/gocell/pkg/query"
	commandruntime "github.com/ghbvf/gocell/runtime/command"
	idemkey "github.com/ghbvf/gocell/runtime/http/idempotency"
)

func newAsyncTestSvc(t *testing.T, opts ...Option) *Service {
	t.Helper()
	codec, err := query.NewCursorCodec(bytes.Repeat([]byte("k"), 32))
	if err != nil {
		t.Fatalf("NewCursorCodec: %v", err)
	}
	all := append([]Option{WithSliceName("devicecommand")}, opts...)
	svc, err := NewService(clock.Real(), commandtest.NewInMemQueue(), mem.NewDeviceRepository(),
		codec, slog.Default(), query.RunModeProd, all...)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// TestEnqueueAsync_EmitsCommandFromIdempotencyKey: the async path sources the
// command_id from the request's Idempotency-Key (ctx) and emits the cmdenqueue
// command for cross-cell same-slot dedup.
func TestEnqueueAsync_EmitsCommandFromIdempotencyKey(t *testing.T) {
	rec := outboxtest.NewRecorder()
	svc := newAsyncTestSvc(t, WithCommandEmitter(rec.CellEmitter()))

	ctx := idemkey.WithKey(context.Background(), "idem-77")
	if err := svc.EnqueueAsync(ctx, "dev-1", "reboot", "{}"); err != nil {
		t.Fatalf("EnqueueAsync: %v", err)
	}

	entries := rec.Entries()
	if len(entries) != 1 {
		t.Fatalf("emitted %d entries, want 1", len(entries))
	}
	e := entries[0]
	if e.RoutingTopic() != string(cmdenqueue.DispatchID) {
		t.Errorf("RoutingTopic = %q, want %q", e.RoutingTopic(), cmdenqueue.DispatchID)
	}
	if e.AggregateID() != "dev-1" {
		t.Errorf("AggregateID = %q, want dev-1", e.AggregateID())
	}
	if got := e.Metadata()[commandruntime.CommandIDMetadataKey]; got != "idem-77" {
		t.Errorf("commandID metadata = %q, want idem-77 (from Idempotency-Key)", got)
	}
}

// TestEnqueueAsync_NilEmitterFailsFast: the async path requires a wired emitter.
func TestEnqueueAsync_NilEmitterFailsFast(t *testing.T) {
	svc := newAsyncTestSvc(t) // no WithCommandEmitter
	ctx := idemkey.WithKey(context.Background(), "idem-1")
	if err := svc.EnqueueAsync(ctx, "dev-1", "reboot", "{}"); err == nil {
		t.Fatal("EnqueueAsync with nil emitter should fail-fast")
	}
}

// TestEnqueueAsync_MissingIdempotencyKey: without an Idempotency-Key in ctx the
// bridge fail-closes and nothing is emitted.
func TestEnqueueAsync_MissingIdempotencyKey(t *testing.T) {
	rec := outboxtest.NewRecorder()
	svc := newAsyncTestSvc(t, WithCommandEmitter(rec.CellEmitter()))
	if err := svc.EnqueueAsync(context.Background(), "dev-1", "reboot", "{}"); err == nil {
		t.Fatal("EnqueueAsync without Idempotency-Key in ctx should fail-closed")
	}
	if n := len(rec.Entries()); n != 0 {
		t.Fatalf("no entry should be emitted on fail-closed, got %d", n)
	}
}
