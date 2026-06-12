package devicecertcompletion

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	rotationresolved "github.com/ghbvf/gocell/generated/contracts/event/devicecert-rotation-resolved/v1"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	kcommand "github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/outbox/outboxtest"
)

var testBase = time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)

func newTestService(t *testing.T, repo domain.DeviceRepository) (*Service, *outboxtest.Recorder) {
	t.Helper()
	rec := outboxtest.NewRecorder()
	svc, err := NewService(clockmock.New(testBase), repo, WithEmitter(rec.CellEmitter()))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc, rec
}

const (
	testDeviceID     = "dev-1"
	testRotatedEpoch = int64(2)
)

// rotateCmdEntry builds a terminal rotate-cert command entry (epoch
// testRotatedEpoch) as the devicecmd Service would hand to the OnCommandResolved
// hook (payload = the producer's {epoch, notAfter} shape).
func rotateCmdEntry(t *testing.T) kcommand.Entry {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"epoch": testRotatedEpoch, "notAfter": "2026-09-08T00:00:00Z"})
	if err != nil {
		t.Fatalf("marshal cmd payload: %v", err)
	}
	return kcommand.Entry{
		ID:          "cmd-1",
		DeviceID:    testDeviceID,
		CommandType: domain.RotateCertCommandType,
		Payload:     payload,
	}
}

// resolvedEntry builds an event.devicecert-rotation-resolved.v1 outbox entry.
func resolvedEntry(t *testing.T, deviceID string, epoch int64, outcome string, resolvedAt time.Time) outbox.Entry {
	t.Helper()
	payload, err := json.Marshal(rotationresolved.Payload{
		DeviceID:   deviceID,
		Epoch:      epoch,
		Outcome:    outcome,
		ResolvedAt: resolvedAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	entry, err := outbox.NewEntry(clockmock.New(testBase), context.Background(), topicRotationResolved, payload)
	if err != nil {
		t.Fatalf("build entry: %v", err)
	}
	return entry
}

func rawEntry(t *testing.T, raw []byte) outbox.Entry {
	t.Helper()
	entry, err := outbox.NewEntry(clockmock.New(testBase), context.Background(), topicRotationResolved, raw)
	if err != nil {
		t.Fatalf("build raw entry: %v", err)
	}
	return entry
}

// resolvedEntryRawTime builds a resolved entry with an arbitrary (possibly
// invalid) resolvedAt string, to exercise the consumer's time-parse rejection.
func resolvedEntryRawTime(t *testing.T, resolvedAt string) outbox.Entry {
	t.Helper()
	return rawEntry(t, mustJSON(t, rotationresolved.Payload{
		DeviceID: testDeviceID, Epoch: 2, Outcome: outcomeSucceeded, ResolvedAt: resolvedAt,
	}))
}

// ---------------------------------------------------------------------------
// OnCommandResolved (publish side)
// ---------------------------------------------------------------------------

func TestOnCommandResolved_RotateCertSuccess_EmitsResolvedEvent(t *testing.T) {
	svc, rec := newTestService(t, mem.NewDeviceRepository())

	svc.OnCommandResolved(context.Background(), rotateCmdEntry(t), kcommand.AckSuccess)

	entries := rec.Entries()
	if len(entries) != 1 {
		t.Fatalf("emitted %d entries, want 1", len(entries))
	}
	var got rotationresolved.Payload
	if err := json.Unmarshal(entries[0].Payload(), &got); err != nil {
		t.Fatalf("decode emitted payload: %v", err)
	}
	if got.DeviceID != "dev-1" || got.Epoch != 2 || got.Outcome != outcomeSucceeded {
		t.Fatalf("payload = %+v, want {dev-1, 2, succeeded}", got)
	}
	if got.ResolvedAt != testBase.Format(time.RFC3339Nano) {
		t.Fatalf("ResolvedAt = %q, want %q", got.ResolvedAt, testBase.Format(time.RFC3339Nano))
	}
}

func TestOnCommandResolved_NonRotateCert_NoEmit(t *testing.T) {
	svc, rec := newTestService(t, mem.NewDeviceRepository())

	entry := rotateCmdEntry(t)
	entry.CommandType = "reboot" // not a rotate-cert command
	svc.OnCommandResolved(context.Background(), entry, kcommand.AckSuccess)

	if n := len(rec.Entries()); n != 0 {
		t.Fatalf("emitted %d entries for non-rotate-cert command, want 0", n)
	}
}

func TestOnCommandResolved_OutcomeMapping(t *testing.T) {
	cases := []struct {
		reason kcommand.AckReason
		want   string
	}{
		{kcommand.AckSuccess, outcomeSucceeded},
		{kcommand.AckFailed, outcomeFailed},
		{kcommand.AckRejected, outcomeRejected},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			svc, rec := newTestService(t, mem.NewDeviceRepository())
			svc.OnCommandResolved(context.Background(), rotateCmdEntry(t), tc.reason)
			entries := rec.Entries()
			if len(entries) != 1 {
				t.Fatalf("emitted %d entries, want 1", len(entries))
			}
			var got rotationresolved.Payload
			if err := json.Unmarshal(entries[0].Payload(), &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got.Outcome != tc.want {
				t.Fatalf("outcome = %q, want %q", got.Outcome, tc.want)
			}
		})
	}
}

func TestOnCommandResolved_UndecodablePayload_NoEmit(t *testing.T) {
	svc, rec := newTestService(t, mem.NewDeviceRepository())

	entry := rotateCmdEntry(t)
	entry.Payload = []byte("not json")
	svc.OnCommandResolved(context.Background(), entry, kcommand.AckSuccess)

	if n := len(rec.Entries()); n != 0 {
		t.Fatalf("emitted %d entries for undecodable payload, want 0 (logged, not emitted)", n)
	}
}

// ---------------------------------------------------------------------------
// HandleRotationResolved (subscribe side)
// ---------------------------------------------------------------------------

func seedDevice(t *testing.T, repo *mem.DeviceRepository, id string, epoch int64, expiry time.Time) {
	t.Helper()
	if err := repo.Create(context.Background(), &domain.Device{
		ID: id, Name: id, Status: "online", LastSeen: testBase,
		CertEpoch: epoch, CertExpiresAt: expiry,
	}); err != nil {
		t.Fatalf("seed device: %v", err)
	}
}

func TestHandleRotationResolved_Succeeded_AdvancesCert(t *testing.T) {
	repo := mem.NewDeviceRepository()
	seedDevice(t, repo, "dev-1", 2, testBase.Add(time.Hour)) // near-expiry candidate
	svc, _ := newTestService(t, repo)

	res := svc.HandleRotationResolved(context.Background(), resolvedEntry(t, "dev-1", 2, outcomeSucceeded, testBase))
	if res.Disposition != outbox.DispositionAck {
		t.Fatalf("Disposition = %v, want Ack; err=%v", res.Disposition, res.Err)
	}

	got, err := repo.GetByID(context.Background(), "dev-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.CertEpoch != 3 {
		t.Fatalf("CertEpoch = %d, want 3 (advanced)", got.CertEpoch)
	}
	wantExpiry := testBase.Add(domain.CertValidity)
	if !got.CertExpiresAt.Equal(wantExpiry) {
		t.Fatalf("CertExpiresAt = %v, want %v (resolvedAt + CertValidity)", got.CertExpiresAt, wantExpiry)
	}
}

func TestHandleRotationResolved_StaleEpoch_AckNoOp(t *testing.T) {
	repo := mem.NewDeviceRepository()
	seedDevice(t, repo, "dev-1", 3, testBase.Add(365*24*time.Hour)) // already advanced
	svc, _ := newTestService(t, repo)

	// Replay a resolve for the now-stale epoch 2.
	res := svc.HandleRotationResolved(context.Background(), resolvedEntry(t, "dev-1", 2, outcomeSucceeded, testBase))
	if res.Disposition != outbox.DispositionAck {
		t.Fatalf("Disposition = %v, want Ack (idempotent no-op)", res.Disposition)
	}
	got, _ := repo.GetByID(context.Background(), "dev-1")
	if got.CertEpoch != 3 {
		t.Fatalf("CertEpoch = %d, want 3 (unchanged by stale replay)", got.CertEpoch)
	}
}

func TestHandleRotationResolved_Failure_AckNoCertChange(t *testing.T) {
	for _, outcome := range []string{outcomeFailed, outcomeRejected} {
		t.Run(outcome, func(t *testing.T) {
			repo := mem.NewDeviceRepository()
			seedDevice(t, repo, "dev-1", 2, testBase.Add(time.Hour))
			svc, _ := newTestService(t, repo)

			res := svc.HandleRotationResolved(context.Background(), resolvedEntry(t, "dev-1", 2, outcome, testBase))
			if res.Disposition != outbox.DispositionAck {
				t.Fatalf("Disposition = %v, want Ack (failure is observed, not retried)", res.Disposition)
			}
			got, _ := repo.GetByID(context.Background(), "dev-1")
			if got.CertEpoch != 2 || !got.CertExpiresAt.Equal(testBase.Add(time.Hour)) {
				t.Fatalf("device cert state changed on failure outcome: %+v", got)
			}
		})
	}
}

func TestHandleRotationResolved_RejectsBadOrMissingPayload(t *testing.T) {
	cases := []struct {
		name  string
		entry func(t *testing.T) outbox.Entry
	}{
		{"undecodable", func(t *testing.T) outbox.Entry { return rawEntry(t, []byte("not json")) }},
		{"empty deviceId", func(t *testing.T) outbox.Entry { return resolvedEntry(t, "", 2, outcomeSucceeded, testBase) }},
		{"epoch below 1", func(t *testing.T) outbox.Entry { return resolvedEntry(t, "dev-1", 0, outcomeSucceeded, testBase) }},
		{"unknown outcome", func(t *testing.T) outbox.Entry { return resolvedEntry(t, "dev-1", 2, "weird", testBase) }},
		{"empty resolvedAt", func(t *testing.T) outbox.Entry { return resolvedEntryRawTime(t, "") }},
		{"unparseable resolvedAt", func(t *testing.T) outbox.Entry { return resolvedEntryRawTime(t, "not-a-time") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newTestService(t, mem.NewDeviceRepository())
			res := svc.HandleRotationResolved(context.Background(), tc.entry(t))
			if res.Disposition != outbox.DispositionReject {
				t.Fatalf("Disposition = %v, want Reject (permanent producer-side violation)", res.Disposition)
			}
		})
	}
}

// errAdvanceRepo wraps mem but fails AdvanceCertAfterRotation, to drive the
// transient (Requeue) branch.
type errAdvanceRepo struct {
	*mem.DeviceRepository
	err error
}

func (r errAdvanceRepo) AdvanceCertAfterRotation(context.Context, string, int64, time.Time) (bool, error) {
	return false, r.err
}

func TestHandleRotationResolved_RepoError_Requeue(t *testing.T) {
	repo := errAdvanceRepo{DeviceRepository: mem.NewDeviceRepository(), err: errors.New("db down")}
	svc, err := NewService(clockmock.New(testBase), repo)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	res := svc.HandleRotationResolved(context.Background(), resolvedEntry(t, "dev-1", 2, outcomeSucceeded, testBase))
	if res.Disposition != outbox.DispositionRequeue {
		t.Fatalf("Disposition = %v, want Requeue (transient repo error)", res.Disposition)
	}
}

func TestNewService_NilRepo_FailsFast(t *testing.T) {
	_, err := NewService(clockmock.New(testBase), nil)
	if err == nil {
		t.Fatal("NewService(nil repo): expected error, got nil")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
