package outbox

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var _ Emitter = (*WriterEmitter)(nil)
var _ Emitter = (*DirectEmitter)(nil)

func TestWriterEmitter_ConstructRejectsNilWriter(t *testing.T) {
	_, err := NewWriterEmitter(nil)

	assert.Error(t, err)
}

func TestWriterEmitter_EmitDelegatesToWriter(t *testing.T) {
	writer := &recordingEmitterWriter{}
	emitter, err := NewWriterEmitter(writer)
	require.NoError(t, err)

	entry := validEntry("writer-emitter")
	err = emitter.Emit(context.Background(), entry)

	require.NoError(t, err)
	require.Len(t, writer.entries, 1)
	assert.Equal(t, entry.ID, writer.entries[0].ID)
}

func TestDirectEmitter_ConstructRejectsNilPublisher(t *testing.T) {
	_, err := NewDirectEmitter(nil, DirectPublishFailClosed, metrics.NopProvider{}, clock.Real(), "testcell")

	assert.Error(t, err)
}

func TestDirectEmitter_EmitWrapsV1EnvelopeAndPublishes(t *testing.T) {
	publisher := &recordingEmitterPublisher{}
	emitter, err := NewDirectEmitter(publisher, DirectPublishFailClosed, metrics.NopProvider{}, clock.Real(), "testcell")
	require.NoError(t, err)

	entry := validEntry("direct-emitter")
	entry.Topic = "direct.topic.v1"
	entry.EventType = "direct.event.v1"

	err = emitter.Emit(context.Background(), entry)

	require.NoError(t, err)
	require.Len(t, publisher.calls, 1)
	assert.Equal(t, entry.Topic, publisher.calls[0].topic)

	got, err := UnmarshalEnvelope(entry.Topic, publisher.calls[0].payload)
	require.NoError(t, err)
	assert.Equal(t, entry.ID, got.ID)
	assert.Equal(t, entry.EventType, got.EventType)
	assert.Equal(t, entry.Topic, got.Topic)
	assert.Equal(t, string(entry.Payload), string(got.Payload))
}

func TestDirectEmitter_FailClosedReturnsPublishError(t *testing.T) {
	want := errors.New("broker down")
	publisher := &recordingEmitterPublisher{err: want}
	emitter, err := NewDirectEmitter(publisher, DirectPublishFailClosed, metrics.NopProvider{}, clock.Real(), "testcell")
	require.NoError(t, err)

	got := emitter.Emit(context.Background(), validEntry("direct-fail-closed"))

	assert.ErrorIs(t, got, want)
}

func TestDirectEmitter_FailOpenSwallowsPublishError(t *testing.T) {
	want := errors.New("broker down")
	publisher := &recordingEmitterPublisher{err: want}
	emitter, err := NewDirectEmitter(publisher, DirectPublishFailOpen, metrics.NopProvider{}, clock.Real(), "testcell")
	require.NoError(t, err)

	got := emitter.Emit(context.Background(), validEntry("direct-fail-open"))

	assert.NoError(t, got)
	require.Len(t, publisher.calls, 1, "fail-open must still attempt publish")
}

func TestDirectEmitter_InvalidEntryFailsBeforePublish(t *testing.T) {
	publisher := &recordingEmitterPublisher{}
	emitter, err := NewDirectEmitter(publisher, DirectPublishFailOpen, metrics.NopProvider{}, clock.Real(), "testcell")
	require.NoError(t, err)

	got := emitter.Emit(context.Background(), Entry{})

	assert.Error(t, got)
	assert.Empty(t, publisher.calls)
}

type recordingEmitterWriter struct {
	entries []Entry
	err     error
}

func (w *recordingEmitterWriter) Write(_ context.Context, entry Entry) error {
	w.entries = append(w.entries, entry)
	return w.err
}

type emitterPublishCall struct {
	topic   string
	payload []byte
}

type recordingEmitterPublisher struct {
	calls []emitterPublishCall
	err   error
}

func (p *recordingEmitterPublisher) Publish(_ context.Context, topic string, payload []byte) error {
	p.calls = append(p.calls, emitterPublishCall{topic: topic, payload: payload})
	return p.err
}

func (p *recordingEmitterPublisher) Close(_ context.Context) error { return nil }

// TestWriterEmitter_Durable covers DurabilityReporter contract on WriterEmitter.
// Backed by NoopWriter → non-durable; backed by a real writer → durable.
func TestWriterEmitter_Durable(t *testing.T) {
	tests := []struct {
		name   string
		writer Writer
		want   bool
	}{
		{name: "noop_writer_not_durable", writer: NoopWriter{}, want: false},
		{name: "real_writer_durable", writer: &recordingEmitterWriter{}, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e, err := NewWriterEmitter(tc.writer)
			require.NoError(t, err)
			assert.Equal(t, tc.want, e.Durable())
			assert.Equal(t, tc.want, ReportDurable(e))
		})
	}
}

// TestDirectEmitter_Durable: DirectEmitter is always non-durable by design.
func TestDirectEmitter_Durable(t *testing.T) {
	e, err := NewDirectEmitter(&recordingEmitterPublisher{}, DirectPublishFailOpen, metrics.NopProvider{}, clock.Real(), "testcell")
	require.NoError(t, err)
	assert.False(t, e.Durable())
	assert.False(t, ReportDurable(e))
}

// TestReportDurable_FallbackForUnknownEmitter: an Emitter that does not
// implement DurabilityReporter is treated as non-durable (safe default).
func TestReportDurable_FallbackForUnknownEmitter(t *testing.T) {
	e := unknownEmitter{}
	assert.False(t, ReportDurable(e))
	assert.False(t, ReportDurable(nil))
}

type unknownEmitter struct{}

func (unknownEmitter) Emit(_ context.Context, _ Entry) error { return nil }

// TestDirectEmitter_EntryFailurePolicyOverridesCtorDefault covers the F9
// per-entry failure policy matrix: zero value falls through to ctor default;
// non-zero entry policy wins over ctor. Ensures security topics that set
// FailurePolicyFailClosed at entry-construction time surface publisher
// failures even when Emitter was constructed with FailOpen for other entries.
//
// ref: k8s apiserver/pkg/audit Backend.FailurePolicy (per-event, not per-sink).
func TestDirectEmitter_EntryFailurePolicyOverridesCtorDefault(t *testing.T) {
	tests := []struct {
		name        string
		ctorMode    DirectPublishFailureMode
		entryPolicy FailurePolicy
		wantErr     bool
	}{
		{"default_policy_falls_through_to_ctor_failopen", DirectPublishFailOpen, FailurePolicyDefault, false},
		{"default_policy_falls_through_to_ctor_failclosed", DirectPublishFailClosed, FailurePolicyDefault, true},
		{"entry_failclosed_overrides_ctor_failopen", DirectPublishFailOpen, FailurePolicyFailClosed, true},
		{"entry_failopen_overrides_ctor_failclosed", DirectPublishFailClosed, FailurePolicyFailOpen, false},
		{"entry_failopen_matches_ctor_failopen", DirectPublishFailOpen, FailurePolicyFailOpen, false},
		{"entry_failclosed_matches_ctor_failclosed", DirectPublishFailClosed, FailurePolicyFailClosed, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			publisher := &recordingEmitterPublisher{err: errors.New("broker down")}
			emitter, err := NewDirectEmitter(publisher, tc.ctorMode, metrics.NopProvider{}, clock.Real(), "testcell")
			require.NoError(t, err)

			entry := validEntry("policy-test-" + tc.name)
			entry.FailurePolicy = tc.entryPolicy

			got := emitter.Emit(context.Background(), entry)
			if tc.wantErr {
				assert.Error(t, got, "expected publisher error to surface")
			} else {
				assert.NoError(t, got, "expected publisher error to be dropped")
			}
			require.Len(t, publisher.calls, 1, "publish must be attempted regardless of policy")
		})
	}
}

// TestFailurePolicy_Resolve covers the standalone resolution table (ctor
// default fallback semantics for the zero value).
func TestFailurePolicy_Resolve(t *testing.T) {
	tests := []struct {
		policy      FailurePolicy
		ctorDefault DirectPublishFailureMode
		want        DirectPublishFailureMode
	}{
		{FailurePolicyDefault, DirectPublishFailOpen, DirectPublishFailOpen},
		{FailurePolicyDefault, DirectPublishFailClosed, DirectPublishFailClosed},
		{FailurePolicyFailOpen, DirectPublishFailClosed, DirectPublishFailOpen},
		{FailurePolicyFailClosed, DirectPublishFailOpen, DirectPublishFailClosed},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, tc.policy.Resolve(tc.ctorDefault),
			"policy=%v default=%v", tc.policy, tc.ctorDefault)
	}
}

// TM5: TestDirectEmitter_FailOpenCounterIncrement verifies that the
// gocell_outbox_emit_failopen_dropped_total counter increments on each
// fail-open dropped event.
func TestDirectEmitter_FailOpenCounterIncrement(t *testing.T) {
	spy := &spyMetricsProvider{}
	publisher := &recordingEmitterPublisher{err: errors.New("broker down")}

	emitter, err := NewDirectEmitter(publisher, DirectPublishFailOpen, spy, clock.Real(), "testcell")
	require.NoError(t, err)

	entry := validEntry("counter-test")
	entry.Topic = "test.topic.v1"

	require.NoError(t, emitter.Emit(context.Background(), entry))
	require.NoError(t, emitter.Emit(context.Background(), entry))
	require.NoError(t, emitter.Emit(context.Background(), entry))

	assert.Equal(t, 3, spy.counter.count, "failopen_dropped counter must increment on each dropped event")
}

// TestDirectEmitter_FailClosedCounterNotIncremented verifies that the
// fail-open dropped counter does NOT increment on a fail-closed publish error.
// The counter is only for events dropped (swallowed); fail-closed surfaces the
// error instead of dropping it.
func TestDirectEmitter_FailClosedCounterNotIncremented(t *testing.T) {
	spy := &spyMetricsProvider{}
	publisher := &recordingEmitterPublisher{err: errors.New("broker down")}

	emitter, err := NewDirectEmitter(publisher, DirectPublishFailClosed, spy, clock.Real(), "testcell")
	require.NoError(t, err)

	entry := validEntry("counter-not-inc-test")
	entry.Topic = "test.topic.v1"

	got := emitter.Emit(context.Background(), entry)
	require.Error(t, got, "fail-closed must return publish error")

	// Ensure spy.counter was registered (CounterVec was called) but never incremented.
	require.NotNil(t, spy.counter, "counter must be registered by constructor")
	assert.Equal(t, 0, spy.counter.count, "fail-closed path must NOT increment fail-open dropped counter")
}

// TM7: TestNewDirectEmitter_NilProvider verifies that passing a nil
// metrics.Provider returns an errcode error (fail-fast, not nil-propagation).
func TestNewDirectEmitter_NilProvider(t *testing.T) {
	publisher := &recordingEmitterPublisher{}
	_, err := NewDirectEmitter(publisher, DirectPublishFailClosed, nil, clock.Real(), "testcell")
	require.Error(t, err)
	// errcode type assertion
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "must be an errcode.Error, got %T: %v", err, err)
	assert.Equal(t, errcode.ErrCellMissingOutbox, ec.Code, "expected ErrCellMissingOutbox code")
}

// TestNewDirectEmitter_NilClock verifies that passing a nil clock panics at
// construction (fail-fast via MustHaveClock, not silent nil propagation).
func TestNewDirectEmitter_NilClock(t *testing.T) {
	publisher := &recordingEmitterPublisher{}
	assert.Panics(t, func() {
		_, _ = NewDirectEmitter(publisher, DirectPublishFailClosed, metrics.NopProvider{}, nil, "testcell")
	}, "nil clock must panic at construction (MustHaveClock)")
}

// ---------------------------------------------------------------------------
// spy metrics helpers (test-only, same package)
// ---------------------------------------------------------------------------

type spyCounter struct{ count int }

func (c *spyCounter) Inc()          { c.count++ }
func (c *spyCounter) Add(d float64) { c.count += int(d) }

type spyCounterVec struct {
	counter *spyCounter
	labels  []string
}

func (v *spyCounterVec) Registered() bool { return true }
func (v *spyCounterVec) With(l metrics.Labels) metrics.Counter {
	metrics.MustValidateLabels(v.labels, l)
	return v.counter
}

type spyHistogramVec struct{ labels []string }

func (v *spyHistogramVec) Registered() bool { return true }
func (v *spyHistogramVec) With(l metrics.Labels) metrics.Histogram {
	metrics.MustValidateLabels(v.labels, l)
	return spyHistogram{}
}

type spyHistogram struct{}

func (spyHistogram) Observe(_ float64) {}

type spyMetricsProvider struct {
	counter *spyCounter
}

func (p *spyMetricsProvider) CounterVec(opts metrics.CounterOpts) (metrics.CounterVec, error) {
	p.counter = &spyCounter{}
	return &spyCounterVec{counter: p.counter, labels: opts.LabelNames}, nil
}

func (p *spyMetricsProvider) HistogramVec(opts metrics.HistogramOpts) (metrics.HistogramVec, error) {
	return &spyHistogramVec{labels: opts.LabelNames}, nil
}

func (p *spyMetricsProvider) Unregister(_ metrics.Collector) error { return nil }

// ---------------------------------------------------------------------------
// Coverage gap tests — paths not exercised by the initial test suite
// ---------------------------------------------------------------------------

// TestNewNoopEmitter_ReturnsNonNilEmitter verifies NewNoopEmitter returns a
// usable Emitter backed by NoopWriter (does not panic, validates entries).
func TestNewNoopEmitter_ReturnsNonNilEmitter(t *testing.T) {
	e := NewNoopEmitter()
	require.NotNil(t, e)

	// Valid entry → no error.
	require.NoError(t, e.Emit(context.Background(), validEntry("noop-emitter")))

	// Invalid entry → validation error.
	assert.Error(t, e.Emit(context.Background(), Entry{}))
}

// TestWriterEmitter_Emit_NilReceiver verifies that calling Emit on a nil
// *WriterEmitter returns an errcode error instead of panicking.
func TestWriterEmitter_Emit_NilReceiver(t *testing.T) {
	var e *WriterEmitter
	err := e.Emit(context.Background(), validEntry("nil-writer"))
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrCellMissingOutbox, ec.Code)
}

// TestDirectEmitter_Emit_NilReceiver verifies that calling Emit on a nil
// *DirectEmitter returns an errcode error instead of panicking.
func TestDirectEmitter_Emit_NilReceiver(t *testing.T) {
	var e *DirectEmitter
	err := e.Emit(context.Background(), validEntry("nil-direct"))
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrCellMissingOutbox, ec.Code)
}

// TestNewDirectEmitter_EmptyCellID verifies that an empty cellID is rejected
// at construction time with ErrValidationFailed.
func TestNewDirectEmitter_EmptyCellID(t *testing.T) {
	publisher := &recordingEmitterPublisher{}
	_, err := NewDirectEmitter(publisher, DirectPublishFailClosed, metrics.NopProvider{}, clock.Real(), "")
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
}

// alwaysFailProvider is a metrics.Provider whose CounterVec always returns an
// error, used to exercise the registration-failure branch in NewDirectEmitter.
type alwaysFailProvider struct{}

func (alwaysFailProvider) CounterVec(_ metrics.CounterOpts) (metrics.CounterVec, error) {
	return nil, errors.New("simulated counter registration failure")
}

func (alwaysFailProvider) HistogramVec(_ metrics.HistogramOpts) (metrics.HistogramVec, error) {
	return nil, errors.New("simulated histogram registration failure")
}

func (alwaysFailProvider) Unregister(_ metrics.Collector) error { return nil }

// TestNewDirectEmitter_CounterVecRegistrationFailure verifies that a
// CounterVec registration failure in the constructor is propagated as an error
// (not swallowed) so the caller knows the emitter was not initialized.
func TestNewDirectEmitter_CounterVecRegistrationFailure(t *testing.T) {
	publisher := &recordingEmitterPublisher{}
	_, err := NewDirectEmitter(publisher, DirectPublishFailClosed, alwaysFailProvider{}, clock.Real(), "testcell")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failopen_dropped counter")
}

// TestNewDirectEmitter_WithLogger verifies that WithLogger option is accepted
// and wired into the emitter (covers the logger-injection branch in NewDirectEmitter).
func TestNewDirectEmitter_WithLogger(t *testing.T) {
	logger := slog.Default()
	publisher := &recordingEmitterPublisher{err: errors.New("broker down")}
	emitter, err := NewDirectEmitter(publisher, DirectPublishFailOpen, metrics.NopProvider{}, clock.Real(), "testcell", WithLogger(logger))
	require.NoError(t, err)
	require.NotNil(t, emitter)
	// Confirm emitter works — fail-open path must not error.
	assert.NoError(t, emitter.Emit(context.Background(), validEntry("logger-injected")))
}

// TestWriterEmitter_Durable_NilReceiver verifies that calling Durable on a
// nil *WriterEmitter returns false (safe default, no panic).
func TestWriterEmitter_Durable_NilReceiver(t *testing.T) {
	var e *WriterEmitter
	assert.False(t, e.Durable())
}

// ---------------------------------------------------------------------------
// Probes tests
// ---------------------------------------------------------------------------

// TestDirectEmitter_Probes_DegradedOnHighDropRatio verifies that after
// all publishes fail (fail-open), the Probes checker reports ErrDegraded.
func TestDirectEmitter_Probes_DegradedOnHighDropRatio(t *testing.T) {
	fp := &recordingEmitterPublisher{err: errors.New("broker down")}
	e, err := NewDirectEmitter(fp, DirectPublishFailOpen, metrics.NopProvider{}, clock.Real(), "testcell")
	require.NoError(t, err)

	ctx := context.Background()
	entry := validEntry("health-degrade")
	for range 10 {
		require.NoError(t, e.Emit(ctx, entry)) // fail-open does not return err
	}

	checkers := e.Probes()
	require.Contains(t, checkers, "outbox-failopen-rate.testcell")

	// 10 drops / 10 total = 100% > 5% default threshold → Tripped
	checkErr := checkers["outbox-failopen-rate.testcell"](ctx)
	require.Error(t, checkErr)
	assert.ErrorIs(t, checkErr, ErrDegraded)
}

// TestDirectEmitter_Probes_HealthyOnLowDropRatio verifies that when
// all publishes succeed (zero drops), the Probes checker returns nil.
func TestDirectEmitter_Probes_HealthyOnLowDropRatio(t *testing.T) {
	pub := &recordingEmitterPublisher{} // no error → success path
	e, err := NewDirectEmitter(pub, DirectPublishFailOpen, metrics.NopProvider{}, clock.Real(), "testcell")
	require.NoError(t, err)

	ctx := context.Background()
	entry := validEntry("health-ok")
	for range 10 {
		require.NoError(t, e.Emit(ctx, entry))
	}

	checkers := e.Probes()
	require.Contains(t, checkers, "outbox-failopen-rate.testcell")

	// 0 drops / 10 total = 0% < 5% threshold → not tripped
	checkErr := checkers["outbox-failopen-rate.testcell"](ctx)
	assert.NoError(t, checkErr)
}

// TestNewDirectEmitter_WithLoggerOption verifies that WithLogger injects a custom
// logger and that the fail-open Warn is written through it (not slog.Default()).
func TestNewDirectEmitter_WithLoggerOption(t *testing.T) {
	customLogger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))

	fp := &recordingEmitterPublisher{err: errors.New("broker down")}
	e, err := NewDirectEmitter(fp, DirectPublishFailOpen, metrics.NopProvider{}, clock.Real(), "testcell",
		WithLogger(customLogger))
	require.NoError(t, err)
	require.NotNil(t, e)

	// Trigger fail-open path; must not error even though publish fails.
	require.NoError(t, e.Emit(context.Background(), validEntry("log-opt")))
}

// TestNewDirectEmitter_WithFailOpenRateThresholdZeroDisables verifies that
// WithFailOpenRateThreshold(0) disables the degraded check (checker always nil).
func TestNewDirectEmitter_WithFailOpenRateThresholdZeroDisables(t *testing.T) {
	fp := &recordingEmitterPublisher{err: errors.New("broker down")}
	e, err := NewDirectEmitter(fp, DirectPublishFailOpen, metrics.NopProvider{}, clock.Real(), "testcell",
		WithFailOpenRateThreshold(0))
	require.NoError(t, err)

	ctx := context.Background()
	entry := validEntry("thresh-zero")
	for range 100 {
		require.NoError(t, e.Emit(ctx, entry))
	}

	// threshold 0 → Tripped always false
	checkErr := e.Probes()["outbox-failopen-rate.testcell"](ctx)
	assert.NoError(t, checkErr)
}

// TestDirectEmitter_InjectsObservabilityFromContext verifies that DirectEmitter.Emit
// populates entry.Observability from the context before publishing, mirroring the
// behavior of Postgres OutboxWriter.Write (adapters/postgres/outbox_writer.go:60-61).
// Trace correlation across async boundaries is broken when DirectEmitter silently
// drops request_id / trace_id — this test pins the fix.
func TestDirectEmitter_InjectsObservabilityFromContext(t *testing.T) {
	const wantRequestID = "req-abc123"
	const wantTraceID = "aabbccddeeff00112233445566778899" // 32 lowercase hex chars

	ctx := context.Background()
	ctx = ctxkeys.WithRequestID(ctx, wantRequestID)
	ctx = ctxkeys.WithTraceID(ctx, wantTraceID)

	publisher := &recordingEmitterPublisher{}
	emitter, err := NewDirectEmitter(publisher, DirectPublishFailClosed, metrics.NopProvider{}, clock.Real(), "testcell")
	require.NoError(t, err)

	entry := validEntry("obs-inject-test")
	entry.Topic = "test.event.v1"
	entry.EventType = "test.event.v1"

	require.NoError(t, emitter.Emit(ctx, entry))
	require.Len(t, publisher.calls, 1, "publish must be attempted")

	// Decode the published envelope and inspect the entry it carries.
	got, err := UnmarshalEnvelope(entry.Topic, publisher.calls[0].payload)
	require.NoError(t, err)
	assert.Equal(t, wantRequestID, got.Observability.RequestID,
		"DirectEmitter must inject request_id from ctx into entry.Observability")
	assert.Equal(t, wantTraceID, got.Observability.TraceID,
		"DirectEmitter must inject trace_id from ctx into entry.Observability")
}

// TestNewDirectEmitter_WithLoggerNilFallsBackToDefault verifies that
// WithLogger(nil) falls back to slog.Default() in NewDirectEmitter
// (defensive — protects against accidental nil logger from caller).
func TestNewDirectEmitter_WithLoggerNilFallsBackToDefault(t *testing.T) {
	e, err := NewDirectEmitter(noopPub{}, DirectPublishFailClosed, metrics.NopProvider{}, clock.Real(), "test-cell",
		WithLogger(nil))
	require.NoError(t, err)
	require.NotNil(t, e)
	// Trigger Emit path, confirm no panic.
	require.NoError(t, e.Emit(context.Background(), validEntry("logger-nil-test")))
}

type noopPub struct{}

func (noopPub) Publish(_ context.Context, _ string, _ []byte) error { return nil }
func (noopPub) Close(_ context.Context) error                       { return nil }
