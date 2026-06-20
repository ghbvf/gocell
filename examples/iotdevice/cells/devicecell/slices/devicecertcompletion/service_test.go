package devicecertcompletion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	kcommand "github.com/ghbvf/gocell/framework/kernel/command"
	kernelmetrics "github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/outbox/outboxtest"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	rotationresolved "github.com/ghbvf/gocell/generated/contracts/event/devicecert-rotation-resolved/v1"
)

var testBase = time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)

// certLifetime1y is the far-future cert lifetime used to seed already-advanced devices in tests.
const certLifetime1y = 365 * 24 * time.Hour

// selfCtx returns a context carrying a device-self principal (PrincipalUser with
// Subject == deviceID) — the identity a device presents when acking its OWN
// rotate-cert command. OnCommandResolved emits the completion event only for
// such device-self acks (operator/admin acks carry a different subject and must
// not advance cert state).
func selfCtx(deviceID string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{
		Kind:    auth.PrincipalUser,
		Subject: deviceID,
	})
}

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
// outcome is the typed enum; bad-wire tests pass rotationresolved.PayloadOutcome("…")
// explicitly to inject an out-of-set value (json decoding does not enforce enum).
func resolvedEntry(t *testing.T, deviceID string, epoch int64, outcome rotationresolved.PayloadOutcome, resolvedAt time.Time) outbox.Entry {
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
		DeviceID: testDeviceID, Epoch: 2, Outcome: rotationresolved.PayloadOutcomeSucceeded, ResolvedAt: resolvedAt,
	}))
}

// ---------------------------------------------------------------------------
// OnCommandResolved (publish side)
// ---------------------------------------------------------------------------

func TestOnCommandResolved_RotateCertSuccess_EmitsResolvedEvent(t *testing.T) {
	svc, rec := newTestService(t, mem.NewDeviceRepository())

	svc.OnCommandResolved(selfCtx(testDeviceID), rotateCmdEntry(t), kcommand.AckSuccess)

	entries := rec.Entries()
	if len(entries) != 1 {
		t.Fatalf("emitted %d entries, want 1", len(entries))
	}
	// Assert the broker routing topic against the contract id LITERAL (not the
	// topicRotationResolved const the producer uses — that would be tautological):
	// this pins the publish-side topic to event.devicecert-rotation-resolved.v1 so
	// a drift of the const away from the contract id fails here. The e2e drives the
	// consumer directly (bypassing routing), so this is the only topic guard (F4).
	if got := entries[0].RoutingTopic(); got != "event.devicecert-rotation-resolved.v1" {
		t.Fatalf("RoutingTopic = %q, want %q", got, "event.devicecert-rotation-resolved.v1")
	}
	var got rotationresolved.Payload
	if err := json.Unmarshal(entries[0].Payload(), &got); err != nil {
		t.Fatalf("decode emitted payload: %v", err)
	}
	if got.DeviceID != "dev-1" || got.Epoch != 2 || got.Outcome != rotationresolved.PayloadOutcomeSucceeded {
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
		want   rotationresolved.PayloadOutcome
	}{
		{kcommand.AckSuccess, rotationresolved.PayloadOutcomeSucceeded},
		{kcommand.AckFailed, rotationresolved.PayloadOutcomeFailed},
		{kcommand.AckRejected, rotationresolved.PayloadOutcomeRejected},
	}
	for _, tc := range cases {
		t.Run(string(tc.want), func(t *testing.T) {
			svc, rec := newTestService(t, mem.NewDeviceRepository())
			svc.OnCommandResolved(selfCtx(testDeviceID), rotateCmdEntry(t), tc.reason)
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
	// device-self ctx so the undecodable-payload path is reached (not short-circuited
	// by the device-self guard).
	svc.OnCommandResolved(selfCtx(testDeviceID), entry, kcommand.AckSuccess)

	if n := len(rec.Entries()); n != 0 {
		t.Fatalf("emitted %d entries for undecodable payload, want 0 (logged, not emitted)", n)
	}
}

// TestOnCommandResolved_NonDeviceSubject_NoEmit proves the device-self guard (F1):
// an operator/admin acking a device's rotate-cert (subject != entry.DeviceID, via the
// device:consume PDP baseline override) resolves the command but does NOT emit a completion
// event — so cert state is never advanced on a non-device assertion.
func TestOnCommandResolved_NonDeviceSubject_NoEmit(t *testing.T) {
	svc, rec := newTestService(t, mem.NewDeviceRepository())

	operatorCtx := auth.WithPrincipal(context.Background(), &auth.Principal{
		Kind:    auth.PrincipalUser,
		Subject: "operator-1", // != testDeviceID ("dev-1")
		Roles:   []string{"admin"},
	})
	svc.OnCommandResolved(operatorCtx, rotateCmdEntry(t), kcommand.AckSuccess)

	if n := len(rec.Entries()); n != 0 {
		t.Fatalf("emitted %d entries for operator (non-device) ack, want 0", n)
	}
}

// TestOnCommandResolved_NoPrincipal_NoEmit proves the guard fail-closes when no
// principal is present in context (e.g. a server-side Sweeper timeout is not a
// device observation): no completion event is emitted.
func TestOnCommandResolved_NoPrincipal_NoEmit(t *testing.T) {
	svc, rec := newTestService(t, mem.NewDeviceRepository())

	svc.OnCommandResolved(context.Background(), rotateCmdEntry(t), kcommand.AckSuccess)

	if n := len(rec.Entries()); n != 0 {
		t.Fatalf("emitted %d entries with no principal in ctx, want 0 (fail-closed)", n)
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

	res := svc.HandleRotationResolved(context.Background(), resolvedEntry(t, "dev-1", 2, rotationresolved.PayloadOutcomeSucceeded, testBase))
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
	seedDevice(t, repo, "dev-1", 3, testBase.Add(certLifetime1y)) // already advanced
	svc, _ := newTestService(t, repo)

	// Replay a resolve for the now-stale epoch 2.
	res := svc.HandleRotationResolved(context.Background(), resolvedEntry(t, "dev-1", 2, rotationresolved.PayloadOutcomeSucceeded, testBase))
	if res.Disposition != outbox.DispositionAck {
		t.Fatalf("Disposition = %v, want Ack (idempotent no-op)", res.Disposition)
	}
	got, _ := repo.GetByID(context.Background(), "dev-1")
	if got.CertEpoch != 3 {
		t.Fatalf("CertEpoch = %d, want 3 (unchanged by stale replay)", got.CertEpoch)
	}
}

func TestHandleRotationResolved_Failure_AckNoCertChange(t *testing.T) {
	for _, outcome := range []rotationresolved.PayloadOutcome{rotationresolved.PayloadOutcomeFailed, rotationresolved.PayloadOutcomeRejected} {
		t.Run(string(outcome), func(t *testing.T) {
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
		{"empty deviceId", func(t *testing.T) outbox.Entry {
			return resolvedEntry(t, "", 2, rotationresolved.PayloadOutcomeSucceeded, testBase)
		}},
		{"epoch below DefaultCertEpoch", func(t *testing.T) outbox.Entry {
			return resolvedEntry(t, "dev-1", 0, rotationresolved.PayloadOutcomeSucceeded, testBase)
		}},
		{"unknown outcome", func(t *testing.T) outbox.Entry {
			// Out-of-set wire value: typed field, but json decode does not enforce
			// enum membership, so the consumer's default arm must DLX it.
			return resolvedEntry(t, "dev-1", 2, rotationresolved.PayloadOutcome("weird"), testBase)
		}},
		{"empty resolvedAt", func(t *testing.T) outbox.Entry { return resolvedEntryRawTime(t, "") }},
		{"unparseable resolvedAt", func(t *testing.T) outbox.Entry { return resolvedEntryRawTime(t, "not-a-time") }},
		// FIX 2: over-long field length bounds (defense against log-injection / parse-DoS).
		{"deviceId too long", func(t *testing.T) outbox.Entry {
			return resolvedEntry(t, strings.Repeat("x", maxDeviceIDLen+1), 2, rotationresolved.PayloadOutcomeSucceeded, testBase)
		}},
		{"outcome too long", func(t *testing.T) outbox.Entry {
			return resolvedEntry(t, "dev-1", 2, rotationresolved.PayloadOutcome(strings.Repeat("o", maxOutcomeLen+1)), testBase)
		}},
		{"resolvedAt too long", func(t *testing.T) outbox.Entry {
			return resolvedEntryRawTime(t, strings.Repeat("t", maxResolvedAtLen+1))
		}},
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

	res := svc.HandleRotationResolved(context.Background(), resolvedEntry(t, "dev-1", 2, rotationresolved.PayloadOutcomeSucceeded, testBase))
	if res.Disposition != outbox.DispositionRequeue {
		t.Fatalf("Disposition = %v, want Requeue (transient repo error)", res.Disposition)
	}
}

func TestNewService_NilRepo_FailsFast(t *testing.T) {
	_, err := NewService(clockmock.New(testBase), nil)
	if err == nil {
		t.Fatal("NewService(nil repo): expected error, got nil")
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("NewService(nil repo): want *errcode.Error, got %T: %v", err, err)
	}
	if ec.Code != errcode.ErrCellInvalidConfig {
		t.Fatalf("NewService(nil repo): errcode = %q, want %q", ec.Code, errcode.ErrCellInvalidConfig)
	}
}

// TestHandleRotationResolved_UnknownDevice_AckNoOp proves that a rotation-resolved
// event for a device that does not exist in the repo is an idempotent Ack (no-op),
// not a Reject or Requeue. The CAS on AdvanceCertAfterRotation returns advanced=false
// for an unknown device, so the handler safely Acks without any state change.
func TestHandleRotationResolved_UnknownDevice_AckNoOp(t *testing.T) {
	// Repo has no devices seeded — "dev-ghost" is unknown.
	svc, _ := newTestService(t, mem.NewDeviceRepository())

	res := svc.HandleRotationResolved(context.Background(),
		resolvedEntry(t, "dev-ghost", 1, rotationresolved.PayloadOutcomeSucceeded, testBase))
	if res.Disposition != outbox.DispositionAck {
		t.Fatalf("Disposition = %v, want Ack (unknown device is an idempotent no-op); err=%v",
			res.Disposition, res.Err)
	}
}

// TestHandleRotationResolved_OutcomeCounter_Increments proves the metrics counter
// is incremented for each valid outcome.
func TestHandleRotationResolved_OutcomeCounter_Increments(t *testing.T) {
	cases := []struct {
		outcome rotationresolved.PayloadOutcome
		seedID  string // device to seed; empty = no seed
	}{
		{rotationresolved.PayloadOutcomeSucceeded, "dev-1"},
		{rotationresolved.PayloadOutcomeFailed, "dev-metric"},
		{rotationresolved.PayloadOutcomeRejected, "dev-1"},
	}
	for _, tc := range cases {
		t.Run(string(tc.outcome), func(t *testing.T) {
			spy := newCounterSpyProvider()
			repo := mem.NewDeviceRepository()
			if tc.seedID != "" {
				seedDevice(t, repo, tc.seedID, 2, testBase.Add(time.Hour))
			}
			svc, err := NewService(clockmock.New(testBase), repo,
				WithMetricsProvider(spy))
			if err != nil {
				t.Fatalf("NewService: %v", err)
			}

			svc.HandleRotationResolved(context.Background(),
				resolvedEntry(t, "dev-1", 2, tc.outcome, testBase))

			ops := spy.ops[metricRotationResolved]
			if len(ops) != 1 {
				t.Fatalf("outcome=%q: want 1 counter inc, got %d", tc.outcome, len(ops))
			}
			if ops[0].labels["outcome"] != string(tc.outcome) {
				t.Fatalf("outcome=%q: counter label outcome=%q, want %q",
					tc.outcome, ops[0].labels["outcome"], tc.outcome)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// counterSpyProvider — minimal spy implementing metrics.Provider for counter tests.
// ---------------------------------------------------------------------------

type counterSpyRecord struct {
	labels kernelmetrics.Labels
}

type counterSpyProvider struct {
	ops map[string][]counterSpyRecord
}

func newCounterSpyProvider() *counterSpyProvider {
	return &counterSpyProvider{ops: make(map[string][]counterSpyRecord)}
}

func (p *counterSpyProvider) CounterVec(opts kernelmetrics.CounterOpts) (kernelmetrics.CounterVec, error) {
	return &counterSpyVec{parent: p, name: opts.Name, labelNames: opts.LabelNames}, nil
}

func (p *counterSpyProvider) HistogramVec(opts kernelmetrics.HistogramOpts) (kernelmetrics.HistogramVec, error) {
	return kernelmetrics.NopProvider{}.HistogramVec(opts)
}

func (p *counterSpyProvider) GaugeVec(opts kernelmetrics.GaugeOpts) (kernelmetrics.GaugeVec, error) {
	return kernelmetrics.NopProvider{}.GaugeVec(opts)
}

type counterSpyVec struct {
	parent     *counterSpyProvider
	name       string
	labelNames []string
}

func (v *counterSpyVec) Registered() bool { return true }
func (v *counterSpyVec) With(l kernelmetrics.Labels) kernelmetrics.Counter {
	kernelmetrics.MustValidateLabels(v.labelNames, l)
	return &counterSpyCounter{parent: v.parent, name: v.name, labels: l}
}

type counterSpyCounter struct {
	parent *counterSpyProvider
	name   string
	labels kernelmetrics.Labels
}

func (c *counterSpyCounter) Inc(ctx context.Context) { c.Add(ctx, 1) }
func (c *counterSpyCounter) Add(_ context.Context, _ float64) {
	c.parent.ops[c.name] = append(c.parent.ops[c.name], counterSpyRecord{labels: c.labels})
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
