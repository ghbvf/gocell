package devicecmd

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	cmdenqueue "github.com/ghbvf/gocell/generated/contracts/command/devicecommand/enqueue/v1"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/outbox/outboxtest"
	"github.com/ghbvf/gocell/kernel/persistence"
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
	devRepo := mem.NewDeviceRepository()
	if err := devRepo.Create(context.Background(),
		&domain.Device{ID: "dev-1", Name: "sensor-a", Status: "online"}); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	all := append([]Option{WithSliceName("devicecommand")}, opts...)
	svc, err := NewService(clock.Real(), commandtest.NewInMemQueue(), devRepo,
		codec, slog.Default(), query.RunModeProd, all...)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// asyncIDCtx injects a sealed RequestIdentity (caller + fixed fingerprint + key)
// into ctx, as the HTTP idempotency middleware would mint.
func asyncIDCtx(t *testing.T, caller, key string) context.Context {
	t.Helper()
	id, err := idemkey.NewRequestIdentity(caller, "fp-test", key)
	if err != nil {
		t.Fatalf("NewRequestIdentity: %v", err)
	}
	return idemkey.WithRequestIdentity(context.Background(), id)
}

// spyTxRunner records RunInTx invocations to assert EnqueueAsync wraps its emit.
type spyTxRunner struct{ calls int }

func (s *spyTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	s.calls++
	return fn(ctx)
}

// errEmitter is a kout.Emitter that always fails, to assert error propagation out
// of the tx wrapper.
type errEmitter struct{}

func (errEmitter) Emit(context.Context, kout.Entry) error { return errors.New("emit boom") }

// TestEnqueueAsync_EmitsCommandFromIdempotencyKey: the async path derives the
// command_id from the RequestIdentity's composite dedup token and emits cmdenqueue.
func TestEnqueueAsync_EmitsCommandFromIdempotencyKey(t *testing.T) {
	rec := outboxtest.NewRecorder()
	svc := newAsyncTestSvc(t, WithCommandEmitter(rec.CellEmitter()))

	id, err := idemkey.NewRequestIdentity("alice", "fp-test", "idem-77")
	if err != nil {
		t.Fatalf("NewRequestIdentity: %v", err)
	}
	ctx := idemkey.WithRequestIdentity(context.Background(), id)
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
	if got := e.Metadata()[commandruntime.CommandIDMetadataKey]; got != id.CommandDedupToken() {
		t.Errorf("commandID metadata = %q, want composite token %q", got, id.CommandDedupToken())
	}
}

// TestEnqueueAsync_NilEmitterFailsFast: the async path requires a wired emitter.
func TestEnqueueAsync_NilEmitterFailsFast(t *testing.T) {
	svc := newAsyncTestSvc(t) // no WithCommandEmitter
	if err := svc.EnqueueAsync(asyncIDCtx(t, "u", "idem-1"), "dev-1", "reboot", "{}"); err == nil {
		t.Fatal("EnqueueAsync with nil emitter should fail-fast")
	}
}

// TestEnqueueAsync_MissingIdempotencyKey: without a RequestIdentity in ctx the
// bridge fail-closes and nothing is emitted.
func TestEnqueueAsync_MissingIdempotencyKey(t *testing.T) {
	rec := outboxtest.NewRecorder()
	svc := newAsyncTestSvc(t, WithCommandEmitter(rec.CellEmitter()))
	if err := svc.EnqueueAsync(context.Background(), "dev-1", "reboot", "{}"); err == nil {
		t.Fatal("EnqueueAsync without RequestIdentity in ctx should fail-closed")
	}
	if n := len(rec.Entries()); n != 0 {
		t.Fatalf("no entry should be emitted on fail-closed, got %d", n)
	}
}

// TestEnqueueAsync_UsesTxManager: the emit is wrapped in txRunner.RunInTx so the
// durable PG outbox writer gets a transaction (#1610 F5).
func TestEnqueueAsync_UsesTxManager(t *testing.T) {
	spy := &spyTxRunner{}
	rec := outboxtest.NewRecorder()
	svc := newAsyncTestSvc(t,
		WithCommandEmitter(rec.CellEmitter()),
		WithCommandTxManager(persistence.WrapForCell(spy)))

	if err := svc.EnqueueAsync(asyncIDCtx(t, "u", "idem-tx"), "dev-1", "reboot", "{}"); err != nil {
		t.Fatalf("EnqueueAsync: %v", err)
	}
	if spy.calls != 1 {
		t.Fatalf("RunInTx called %d times, want 1 (emit must be wrapped in the tx)", spy.calls)
	}
	if n := len(rec.Entries()); n != 1 {
		t.Fatalf("emit must run inside the tx; entries=%d", n)
	}
}

// TestEnqueueAsync_PropagatesEmitError: an emit failure inside the tx closure
// propagates out of EnqueueAsync (no success log, error returned).
func TestEnqueueAsync_PropagatesEmitError(t *testing.T) {
	svc := newAsyncTestSvc(t, WithCommandEmitter(kout.WrapEmitterForCell(errEmitter{})))
	if err := svc.EnqueueAsync(asyncIDCtx(t, "u", "idem-err"), "dev-1", "reboot", "{}"); err == nil {
		t.Fatal("EnqueueAsync must propagate the emit error from the tx closure")
	}
}
