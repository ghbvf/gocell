package devicebootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/outbox/outboxtest"
	rtcommand "github.com/ghbvf/gocell/framework/runtime/command"
	cmdremote "github.com/ghbvf/gocell/generated/contracts/command/remotecommand/v1"
)

const (
	testDeviceID = "dev-7f3a"
	testEntryID  = "evt-device-registered-1"
)

// failingEmitter is an outbox.Emitter that always fails Emit, used to drive the
// transient (Requeue) branch of HandleDeviceRegistered.
type failingEmitter struct{ err error }

func (f failingEmitter) Emit(context.Context, outbox.Entry) error { return f.err }

// errorWriter is an outbox.Writer that always fails Write. Used to test that
// an error propagated inside txRunner.RunInTx (via WriterEmitter) produces
// a Requeue disposition — verifying the RunInTx wrapper is wired correctly.
type errorWriter struct{ err error }

func (w errorWriter) Write(context.Context, outbox.Entry) error { return w.err }

// registeredEntry builds a deterministic event.device-registered.v1 outbox entry
// with a fixed ID so the test can assert command_id == source entry.ID().
func registeredEntry(t *testing.T, clk clock.Clock, payload []byte) outbox.Entry {
	t.Helper()
	entry, err := outbox.NewEntry(clk, context.Background(),
		"event.device-registered.v1", payload, outbox.WithID(testEntryID))
	if err != nil {
		t.Fatalf("build source entry: %v", err)
	}
	return entry
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestHandleDeviceRegistered(t *testing.T) {
	clk := clock.Real()
	validPayload := mustMarshal(t, deviceRegisteredEvent{
		ID:       testDeviceID,
		Name:     "edge-sensor",
		Status:   "online",
		LastSeen: time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
	})

	t.Run("ack emits enqueue command", func(t *testing.T) {
		assertHandleDeviceRegisteredAck(t, clk, validPayload)
	})

	t.Run("reject on undecodable payload", func(t *testing.T) {
		assertHandleDeviceRegisteredRejectsBadPayload(t, clk)
	})

	t.Run("with logger and explicit emitter still acks", func(t *testing.T) {
		assertHandleDeviceRegisteredLoggerAndEmitterAck(t, clk, validPayload)
	})

	t.Run("requeue on emit failure", func(t *testing.T) {
		assertHandleDeviceRegisteredRequeuesOnEmitFailure(t, clk, validPayload)
	})

	// txRunner wraps EmitAsync: an error inside the RunInTx closure (i.e. a
	// failing emitter) must propagate out of RunInTx and produce Requeue.
	// CellTxManager is sealed (cannot be implemented outside kernel packages) so
	// we exercise the error path via a failing writer: WriterEmitter.Emit calls
	// writer.Write(ctx) which fails, RunInTx returns the error, handler Requeues.
	t.Run("requeue when emit fails inside txRunner", func(t *testing.T) {
		assertHandleDeviceRegisteredRequeuesInsideTxRunner(t, clk, validPayload)
	})
}

func assertHandleDeviceRegisteredAck(t *testing.T, clk clock.Clock, validPayload []byte) {
	t.Helper()

	rec := outboxtest.NewRecorder()
	svc, err := NewService(clk, WithEmitter(rec.CellEmitter()))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	res := svc.HandleDeviceRegistered(context.Background(), registeredEntry(t, clk, validPayload))
	if res.Disposition != outbox.DispositionAck {
		t.Fatalf("Disposition = %v, want Ack; err=%v", res.Disposition, res.Err)
	}

	assertBootstrapCommandEmitted(t, rec)
}

func assertBootstrapCommandEmitted(t *testing.T, rec *outboxtest.Recorder) {
	t.Helper()

	entries := rec.Entries()
	if len(entries) != 1 {
		t.Fatalf("emitted %d entries, want 1", len(entries))
	}
	got := entries[0]
	if got.RoutingTopic() != string(cmdremote.DispatchID) {
		t.Errorf("RoutingTopic = %q, want %q", got.RoutingTopic(), cmdremote.DispatchID)
	}
	if got.AggregateID() != testDeviceID {
		t.Errorf("AggregateID = %q, want %q", got.AggregateID(), testDeviceID)
	}
	if cid := got.Metadata()[rtcommand.CommandIDMetadataKey]; cid != testEntryID {
		t.Errorf("command_id metadata = %q, want source entry ID %q", cid, testEntryID)
	}

	var req cmdremote.Request
	if err := json.Unmarshal(got.Payload(), &req); err != nil {
		t.Fatalf("decode emitted command payload: %v", err)
	}
	if req.DeviceID != testDeviceID {
		t.Errorf("req.DeviceID = %q, want %q", req.DeviceID, testDeviceID)
	}
	if req.CommandType != bootstrapCommandType {
		t.Errorf("req.CommandType = %q, want %q", req.CommandType, bootstrapCommandType)
	}
}

func assertHandleDeviceRegisteredRejectsBadPayload(t *testing.T, clk clock.Clock) {
	t.Helper()

	rec := outboxtest.NewRecorder()
	svc, err := NewService(clk, WithEmitter(rec.CellEmitter()))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	bad := registeredEntry(t, clk, []byte("{not json"))
	res := svc.HandleDeviceRegistered(context.Background(), bad)
	if res.Disposition != outbox.DispositionReject {
		t.Fatalf("Disposition = %v, want Reject", res.Disposition)
	}
	var perm *outbox.PermanentError
	if !errors.As(res.Err, &perm) {
		t.Errorf("err = %v, want a *outbox.PermanentError", res.Err)
	}
	if n := len(rec.Entries()); n != 0 {
		t.Errorf("emitted %d entries on decode failure, want 0", n)
	}
}

func assertHandleDeviceRegisteredLoggerAndEmitterAck(t *testing.T, clk clock.Clock, validPayload []byte) {
	t.Helper()

	rec := outboxtest.NewRecorder()
	svc, err := NewService(clk,
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		WithEmitter(rec.CellEmitter()))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	res := svc.HandleDeviceRegistered(context.Background(), registeredEntry(t, clk, validPayload))
	if res.Disposition != outbox.DispositionAck {
		t.Fatalf("Disposition = %v, want Ack", res.Disposition)
	}
	if n := len(rec.Entries()); n != 1 {
		t.Fatalf("emitted %d entries, want 1", n)
	}
}

func assertHandleDeviceRegisteredRequeuesOnEmitFailure(t *testing.T, clk clock.Clock, validPayload []byte) {
	t.Helper()

	emitErr := errors.New("broker unavailable")
	svc, err := NewService(clk, WithEmitter(outbox.WrapEmitterForCell(failingEmitter{err: emitErr})))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	res := svc.HandleDeviceRegistered(context.Background(), registeredEntry(t, clk, validPayload))
	assertRequeueWraps(t, res, emitErr)
}

func assertHandleDeviceRegisteredRequeuesInsideTxRunner(t *testing.T, clk clock.Clock, validPayload []byte) {
	t.Helper()

	writeErr := errors.New("outbox write: no tx in context")
	writerEmitter, werr := outbox.NewWriterEmitter(errorWriter{err: writeErr})
	if werr != nil {
		t.Fatalf("NewWriterEmitter: %v", werr)
	}
	svc, err := NewService(clk, WithEmitter(outbox.WrapEmitterForCell(writerEmitter)))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	res := svc.HandleDeviceRegistered(context.Background(), registeredEntry(t, clk, validPayload))
	assertRequeueWraps(t, res, writeErr)
}

func assertRequeueWraps(t *testing.T, res outbox.HandleResult, want error) {
	t.Helper()

	if res.Disposition != outbox.DispositionRequeue {
		t.Fatalf("Disposition = %v, want Requeue", res.Disposition)
	}
	if !errors.Is(res.Err, want) {
		t.Errorf("err = %v, want it to wrap %v", res.Err, want)
	}
}
