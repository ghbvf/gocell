package mqtt

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
)

// TestNewProviderConnectionCollector_RejectsNilProvider verifies nil provider is
// rejected with an errcode.Error.
func TestNewProviderConnectionCollector_RejectsNilProvider(t *testing.T) {
	t.Parallel()
	_, err := NewProviderConnectionCollector(nil, "testcell")
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
}

// TestNewProviderConnectionCollector_RejectsEmptyCellID verifies empty cellID is
// rejected with an errcode.Error.
func TestNewProviderConnectionCollector_RejectsEmptyCellID(t *testing.T) {
	t.Parallel()
	_, err := NewProviderConnectionCollector(metrics.NopProvider{}, "")
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
}

// TestNewProviderConnectionCollector_NopProviderNoPanic exercises the happy path
// on the no-op provider.
func TestNewProviderConnectionCollector_NopProviderNoPanic(t *testing.T) {
	t.Parallel()
	c, err := NewProviderConnectionCollector(metrics.NopProvider{}, "testcell")
	require.NoError(t, err)
	require.NotNil(t, c)
	assert.NotPanics(t, func() { c.RecordReconnect(context.Background()) })
}

// TestNewProviderConnectionCollector_RegistrationFailure verifies a Provider
// returning an error from CounterVec is wrapped in an errcode.Error.
func TestNewProviderConnectionCollector_RegistrationFailure(t *testing.T) {
	t.Parallel()
	provider := &connErrProvider{err: errors.New("duplicate counter")}
	_, err := NewProviderConnectionCollector(provider, "testcell")
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
}

func TestNewProviderConnectionCollector_SubscribeFailureRegistrationFailure(t *testing.T) {
	t.Parallel()
	provider := &connErrProvider{err: errors.New("duplicate counter"), failAtCounter: 2}
	_, err := NewProviderConnectionCollector(provider, "testcell")
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, 2, provider.counterCalls)
}

// TestProviderConnectionCollector_RecordReconnect_CounterLabel pins that
// RecordReconnect emits exactly one Inc with label cell=testcell.
func TestProviderConnectionCollector_RecordReconnect_CounterLabel(t *testing.T) {
	t.Parallel()
	spy := newConnSpyProvider()
	col, err := NewProviderConnectionCollector(spy, "testcell")
	require.NoError(t, err)

	col.RecordReconnect(context.Background())

	ops := spy.ops()
	require.Len(t, ops, 1)
	assert.Equal(t, "mqtt_reconnect_total", ops[0].name)
	assert.Equal(t, "Inc", ops[0].op)
	assert.Equal(t, "testcell", ops[0].labels["cell"])
}

// TestProviderConnectionCollector_RegistersExpectedLabelNames pins the label
// schema: mqtt_reconnect_total {cell} and mqtt_subscribe_failed_total
// {cell, reason}.
func TestProviderConnectionCollector_RegistersExpectedLabelNames(t *testing.T) {
	t.Parallel()
	spy := newConnSpyProvider()
	_, err := NewProviderConnectionCollector(spy, "testcell")
	require.NoError(t, err)

	regs := spy.registrations()
	require.Len(t, regs, 2)
	assert.Equal(t, "mqtt_reconnect_total", regs[0].Name)
	assert.Equal(t, []string{"cell"}, regs[0].LabelNames)
	assert.Equal(t, "mqtt_subscribe_failed_total", regs[1].Name)
	assert.Equal(t, []string{"cell", "reason"}, regs[1].LabelNames)
}

// TestProviderConnectionCollector_RecordSubscribeFailure verifies both reason
// consts increment mqtt_subscribe_failed_total with the correct reason label.
func TestProviderConnectionCollector_RecordSubscribeFailure(t *testing.T) {
	t.Parallel()
	allReasons := []SubscribeFailureReason{
		subscribeReasonSubackReject,
		subscribeReasonTransport,
	}
	for _, reason := range allReasons {
		reason := reason
		t.Run(string(reason), func(t *testing.T) {
			t.Parallel()
			spy := newConnSpyProvider()
			col, err := NewProviderConnectionCollector(spy, "testcell")
			require.NoError(t, err)

			col.RecordSubscribeFailure(context.Background(), reason)

			ops := spy.ops()
			var found bool
			for _, op := range ops {
				if op.name == "mqtt_subscribe_failed_total" && op.op == "Inc" {
					assert.Equal(t, "testcell", op.labels["cell"])
					assert.Equal(t, string(reason), op.labels["reason"])
					found = true
				}
			}
			assert.True(t, found, "mqtt_subscribe_failed_total Inc not found for reason %s", reason)
		})
	}
}

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// connErrProvider returns a fixed error from CounterVec at a configured call.
type connErrProvider struct {
	err           error
	failAtCounter int
	counterCalls  int
}

func (p *connErrProvider) CounterVec(opts metrics.CounterOpts) (metrics.CounterVec, error) {
	p.counterCalls++
	if p.failAtCounter == 0 || p.counterCalls == p.failAtCounter {
		return nil, p.err
	}
	return metrics.NopProvider{}.CounterVec(opts)
}

func (p *connErrProvider) HistogramVec(_ metrics.HistogramOpts) (metrics.HistogramVec, error) {
	return nil, p.err
}

func (p *connErrProvider) GaugeVec(_ metrics.GaugeOpts) (metrics.GaugeVec, error) {
	return nil, p.err
}

// connSpyProvider records counter registrations and Inc/Add operations.
type connSpyProvider struct {
	regs    []metrics.CounterOpts
	records []connSpyRecord
}

type connSpyRecord struct {
	name   string
	op     string
	labels metrics.Labels
	value  float64
}

func newConnSpyProvider() *connSpyProvider { return &connSpyProvider{} }

func (p *connSpyProvider) CounterVec(opts metrics.CounterOpts) (metrics.CounterVec, error) {
	p.regs = append(p.regs, opts)
	return &connSpyCounterVec{parent: p, name: opts.Name, labelNames: opts.LabelNames}, nil
}

func (p *connSpyProvider) HistogramVec(_ metrics.HistogramOpts) (metrics.HistogramVec, error) {
	return nil, errors.New("connSpyProvider: HistogramVec not implemented")
}

func (p *connSpyProvider) GaugeVec(_ metrics.GaugeOpts) (metrics.GaugeVec, error) {
	return metrics.NopProvider{}.GaugeVec(metrics.GaugeOpts{})
}

func (p *connSpyProvider) ops() []connSpyRecord {
	out := make([]connSpyRecord, len(p.records))
	copy(out, p.records)
	return out
}

func (p *connSpyProvider) registrations() []metrics.CounterOpts {
	out := make([]metrics.CounterOpts, len(p.regs))
	copy(out, p.regs)
	return out
}

type connSpyCounterVec struct {
	parent     *connSpyProvider
	name       string
	labelNames []string
}

func (v *connSpyCounterVec) Registered() bool { return true }

func (v *connSpyCounterVec) With(l metrics.Labels) metrics.Counter {
	metrics.MustValidateLabels(v.labelNames, l)
	return &connSpyCounter{parent: v.parent, name: v.name, labels: l}
}

type connSpyCounter struct {
	parent *connSpyProvider
	name   string
	labels metrics.Labels
}

func (c *connSpyCounter) Inc(_ context.Context) {
	c.parent.records = append(c.parent.records, connSpyRecord{
		name: c.name, op: "Inc", labels: c.labels, value: 1,
	})
}

func (c *connSpyCounter) Add(_ context.Context, d float64) {
	c.parent.records = append(c.parent.records, connSpyRecord{
		name: c.name, op: "Add", labels: c.labels, value: d,
	})
}

// ---------------------------------------------------------------------------
// Publisher metrics tests
// ---------------------------------------------------------------------------

// Compile-time interface check — kept here so the file fails to build if the
// interface shape diverges.
var _ PublisherCollector = (*providerPublisherCollector)(nil)

// TestNewProviderPublisherCollector_NilProvider verifies nil provider returns
// an errcode.Error (KindInternal).
func TestNewProviderPublisherCollector_NilProvider(t *testing.T) {
	t.Parallel()
	_, err := NewProviderPublisherCollector(nil, "testcell")
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.KindInternal, ec.Kind)
}

// TestNewProviderPublisherCollector_EmptyCellID verifies empty cellID returns
// an errcode.Error (KindInternal).
func TestNewProviderPublisherCollector_EmptyCellID(t *testing.T) {
	t.Parallel()
	_, err := NewProviderPublisherCollector(metrics.NopProvider{}, "")
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.KindInternal, ec.Kind)
}

// TestNewProviderPublisherCollector_Registration verifies happy-path returns
// non-nil collector with no error.
func TestNewProviderPublisherCollector_Registration(t *testing.T) {
	t.Parallel()
	col, err := NewProviderPublisherCollector(metrics.NopProvider{}, "testcell")
	require.NoError(t, err)
	require.NotNil(t, col)
}

// TestProviderPublisherCollector_RecordPublishSuccess verifies that a success
// call increments mqtt_publish_total and observes mqtt_publish_ack_duration_seconds.
func TestProviderPublisherCollector_RecordPublishSuccess(t *testing.T) {
	t.Parallel()
	spy := newPubSpyProvider()
	col, err := NewProviderPublisherCollector(spy, "testcell")
	require.NoError(t, err)

	col.RecordPublishSuccess(context.Background(), testtime.D25ms)

	ops := spy.ops()
	// Expect: one Inc on publish_total and one Observe on ack_duration
	var foundTotal, foundDuration bool
	for _, op := range ops {
		switch {
		case op.name == "mqtt_publish_total" && op.op == "Inc":
			assert.Equal(t, "testcell", op.labels["cell"])
			foundTotal = true
		case op.name == "mqtt_publish_ack_duration_seconds" && op.op == "Observe":
			assert.Equal(t, "testcell", op.labels["cell"])
			assert.InDelta(t, 0.025, op.value, 1e-9)
			foundDuration = true
		}
	}
	assert.True(t, foundTotal, "mqtt_publish_total Inc not found in spy ops")
	assert.True(t, foundDuration, "mqtt_publish_ack_duration_seconds Observe not found in spy ops")
}

// TestProviderPublisherCollector_RecordPublishFailure verifies all 10 reason
// consts increment mqtt_publish_failed_total with the correct reason label.
func TestProviderPublisherCollector_RecordPublishFailure(t *testing.T) {
	t.Parallel()
	allReasons := []PublishFailureReason{
		PublishFailurePayloadTooLarge,
		PublishFailureClosed,
		PublishFailurePubAckTimeout,
		PublishFailureContextCanceled,
		PublishFailurePublishError,
		PublishFailureTopicOutsideNamespace,
		PublishFailureRateLimited,
		PublishFailureNotAuthorized,
		PublishFailurePayloadFormatInvalid,
		PublishFailureRejected,
	}
	for _, reason := range allReasons {
		reason := reason
		t.Run(string(reason), func(t *testing.T) {
			t.Parallel()
			spy := newPubSpyProvider()
			col, err := NewProviderPublisherCollector(spy, "testcell")
			require.NoError(t, err)

			col.RecordPublishFailure(context.Background(), reason)

			ops := spy.ops()
			var found bool
			for _, op := range ops {
				if op.name == "mqtt_publish_failed_total" && op.op == "Inc" {
					assert.Equal(t, "testcell", op.labels["cell"])
					assert.Equal(t, string(reason), op.labels["reason"])
					found = true
				}
			}
			assert.True(t, found, "mqtt_publish_failed_total Inc not found for reason %s", reason)
		})
	}
}

// TestNoopPublisherCollector_Methods verifies NoopPublisherCollector does not panic.
func TestNoopPublisherCollector_Methods(t *testing.T) {
	t.Parallel()
	col := NoopPublisherCollector{}
	assert.NotPanics(t, func() {
		col.RecordPublishSuccess(context.Background(), testtime.D10ms)
		col.RecordPublishFailure(context.Background(), PublishFailureClosed)
	})
}

func TestNewProviderPublisherCollector_RegistrationFailure_ReturnsError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		failAtCounter int
		failHistogram bool
	}{
		{name: "publish_total", failAtCounter: 1},
		{name: "publish_failed", failAtCounter: 2},
		{name: "ack_duration", failHistogram: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			provider := &pubRegistrationFailureProvider{
				failAtCounter: tc.failAtCounter,
				failHistogram: tc.failHistogram,
			}
			_, err := NewProviderPublisherCollector(provider, "testcell")
			require.Error(t, err)
			var ec *errcode.Error
			require.True(t, errors.As(err, &ec))
			assert.Equal(t, errcode.KindInternal, ec.Kind)
		})
	}
}

// ---------------------------------------------------------------------------
// Publisher test doubles
// ---------------------------------------------------------------------------

type pubSpyRecord struct {
	name   string
	op     string
	labels metrics.Labels
	value  float64
}

type pubSpyProvider struct {
	counterRegs   []metrics.CounterOpts
	histogramRegs []metrics.HistogramOpts
	records       []pubSpyRecord
}

func newPubSpyProvider() *pubSpyProvider { return &pubSpyProvider{} }

func (p *pubSpyProvider) CounterVec(opts metrics.CounterOpts) (metrics.CounterVec, error) {
	p.counterRegs = append(p.counterRegs, opts)
	return &pubSpyCounterVec{parent: p, name: opts.Name, labelNames: opts.LabelNames}, nil
}

func (p *pubSpyProvider) HistogramVec(opts metrics.HistogramOpts) (metrics.HistogramVec, error) {
	p.histogramRegs = append(p.histogramRegs, opts)
	return &pubSpyHistogramVec{parent: p, name: opts.Name, labelNames: opts.LabelNames}, nil
}

func (p *pubSpyProvider) GaugeVec(_ metrics.GaugeOpts) (metrics.GaugeVec, error) {
	return metrics.NopProvider{}.GaugeVec(metrics.GaugeOpts{})
}

func (p *pubSpyProvider) ops() []pubSpyRecord {
	out := make([]pubSpyRecord, len(p.records))
	copy(out, p.records)
	return out
}

type pubRegistrationFailureProvider struct {
	failAtCounter int
	failHistogram bool
	counterCalls  int
}

func (p *pubRegistrationFailureProvider) CounterVec(opts metrics.CounterOpts) (metrics.CounterVec, error) {
	p.counterCalls++
	if p.failAtCounter > 0 && p.counterCalls == p.failAtCounter {
		return nil, errors.New("duplicate counter")
	}
	return metrics.NopProvider{}.CounterVec(opts)
}

func (p *pubRegistrationFailureProvider) HistogramVec(opts metrics.HistogramOpts) (metrics.HistogramVec, error) {
	if p.failHistogram {
		return nil, errors.New("duplicate histogram")
	}
	return metrics.NopProvider{}.HistogramVec(opts)
}

func (p *pubRegistrationFailureProvider) GaugeVec(opts metrics.GaugeOpts) (metrics.GaugeVec, error) {
	return metrics.NopProvider{}.GaugeVec(opts)
}

type pubSpyCounterVec struct {
	parent     *pubSpyProvider
	name       string
	labelNames []string
}

func (v *pubSpyCounterVec) Registered() bool { return true }

func (v *pubSpyCounterVec) With(l metrics.Labels) metrics.Counter {
	metrics.MustValidateLabels(v.labelNames, l)
	return &pubSpyCounter{parent: v.parent, name: v.name, labels: l}
}

type pubSpyCounter struct {
	parent *pubSpyProvider
	name   string
	labels metrics.Labels
}

func (c *pubSpyCounter) Inc(_ context.Context) {
	c.parent.records = append(c.parent.records, pubSpyRecord{
		name: c.name, op: "Inc", labels: c.labels, value: 1,
	})
}

func (c *pubSpyCounter) Add(_ context.Context, d float64) {
	c.parent.records = append(c.parent.records, pubSpyRecord{
		name: c.name, op: "Add", labels: c.labels, value: d,
	})
}

type pubSpyHistogramVec struct {
	parent     *pubSpyProvider
	name       string
	labelNames []string
}

func (v *pubSpyHistogramVec) Registered() bool { return true }

func (v *pubSpyHistogramVec) With(l metrics.Labels) metrics.Histogram {
	metrics.MustValidateLabels(v.labelNames, l)
	return &pubSpyHistogram{parent: v.parent, name: v.name, labels: l}
}

type pubSpyHistogram struct {
	parent *pubSpyProvider
	name   string
	labels metrics.Labels
}

func (h *pubSpyHistogram) Observe(_ context.Context, val float64) {
	h.parent.records = append(h.parent.records, pubSpyRecord{
		name: h.name, op: "Observe", labels: h.labels, value: val,
	})
}

// ---------------------------------------------------------------------------
// Subscriber metrics tests (PR-3)
// ---------------------------------------------------------------------------

// Compile-time interface check.
var _ SubscriberCollector = (*providerSubscriberCollector)(nil)

// TestNewProviderSubscriberCollector_NilProvider verifies nil provider returns
// an errcode.Error (KindInternal).
func TestNewProviderSubscriberCollector_NilProvider(t *testing.T) {
	t.Parallel()
	_, err := NewProviderSubscriberCollector(nil, "testcell")
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.KindInternal, ec.Kind)
}

// TestNewProviderSubscriberCollector_EmptyCellID verifies empty cellID returns
// an errcode.Error (KindInternal).
func TestNewProviderSubscriberCollector_EmptyCellID(t *testing.T) {
	t.Parallel()
	_, err := NewProviderSubscriberCollector(metrics.NopProvider{}, "")
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.KindInternal, ec.Kind)
}

// TestNewProviderSubscriberCollector_Registration verifies happy-path returns a
// non-nil collector with no error.
func TestNewProviderSubscriberCollector_Registration(t *testing.T) {
	t.Parallel()
	col, err := NewProviderSubscriberCollector(metrics.NopProvider{}, "testcell")
	require.NoError(t, err)
	require.NotNil(t, col)
}

// TestNewProviderSubscriberCollector_RegistrationFailure_ReturnsError verifies
// that a Provider returning an error from a later registration rejects the
// current wiring and wraps the error.
func TestNewProviderSubscriberCollector_RegistrationFailure_ReturnsError(t *testing.T) {
	t.Parallel()
	spy := &subRegistrationFailureProvider{failHistogram: true}
	_, err := NewProviderSubscriberCollector(spy, "testcell")
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, 4, spy.counterCount, "all four counters should be attempted before histogram failure")
}

// TestNewProviderSubscriberCollector_GaugeRegistrationFailure_ReturnsError
// verifies that a failure on the gauge rejects the current wiring and wraps the
// error.
func TestNewProviderSubscriberCollector_GaugeRegistrationFailure_ReturnsError(t *testing.T) {
	t.Parallel()
	spy := &subRegistrationFailureProvider{failGauge: true}
	_, err := NewProviderSubscriberCollector(spy, "testcell")
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.KindInternal, ec.Kind)
	assert.Equal(t, 4, spy.counterCount, "all four counters should be attempted before gauge failure")
}

// TestNewProviderSubscriberCollector_CounterRegistrationFailure_ReturnsError covers
// each of the four counter registration error branches: when the Nth CounterVec
// call fails, the error is wrapped as an errcode.Error. Pins the per-counter
// failure paths (consume_total / consume_failed / dlx_total / dlx_failed) that
// the inlined errcode.Wrap branches introduced.
func TestNewProviderSubscriberCollector_CounterRegistrationFailure_ReturnsError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		failAtCounter int // 1-based: fail the Nth CounterVec call
	}{
		{"consume_total", 1},
		{"consume_failed", 2},
		{"dlx_total", 3},
		{"dlx_failed", 4},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spy := &subRegistrationFailureProvider{failAtCounter: tc.failAtCounter}
			_, err := NewProviderSubscriberCollector(spy, "testcell")
			require.Error(t, err)
			var ec *errcode.Error
			require.True(t, errors.As(err, &ec))
			assert.Equal(t, errcode.KindInternal, ec.Kind)
			assert.Equal(t, tc.failAtCounter, spy.counterCount,
				"counter registration should stop at the failing call")
		})
	}
}

// TestProviderSubscriberCollector_RecordConsumeSuccess verifies success
// increments mqtt_consume_total and observes mqtt_consume_duration_seconds.
func TestProviderSubscriberCollector_RecordConsumeSuccess(t *testing.T) {
	t.Parallel()
	spy := newSubSpyProvider()
	col, err := NewProviderSubscriberCollector(spy, "testcell")
	require.NoError(t, err)

	col.RecordConsumeSuccess(context.Background(), testtime.D25ms)

	ops := spy.ops()
	var foundTotal, foundDuration bool
	for _, op := range ops {
		switch {
		case op.name == "mqtt_consume_total" && op.op == "Inc":
			assert.Equal(t, "testcell", op.labels["cell"])
			foundTotal = true
		case op.name == "mqtt_consume_duration_seconds" && op.op == "Observe":
			assert.Equal(t, "testcell", op.labels["cell"])
			assert.InDelta(t, 0.025, op.value, 1e-9)
			foundDuration = true
		}
	}
	assert.True(t, foundTotal, "mqtt_consume_total Inc not found")
	assert.True(t, foundDuration, "mqtt_consume_duration_seconds Observe not found")
}

// TestProviderSubscriberCollector_RecordConsumeFailure verifies all 6 reason
// consts increment mqtt_consume_failed_total with the correct reason label.
func TestProviderSubscriberCollector_RecordConsumeFailure(t *testing.T) {
	t.Parallel()
	allReasons := []ConsumeFailureReason{
		consumeReasonUnmarshal,
		consumeReasonReject,
		consumeReasonRequeue,
		consumeReasonCommitFailed,
		consumeReasonAckFailed,
		consumeReasonUnknownDisposition,
	}
	for _, reason := range allReasons {
		reason := reason
		t.Run(string(reason), func(t *testing.T) {
			t.Parallel()
			spy := newSubSpyProvider()
			col, err := NewProviderSubscriberCollector(spy, "testcell")
			require.NoError(t, err)

			col.RecordConsumeFailure(context.Background(), reason)

			ops := spy.ops()
			var found bool
			for _, op := range ops {
				if op.name == "mqtt_consume_failed_total" && op.op == "Inc" {
					assert.Equal(t, "testcell", op.labels["cell"])
					assert.Equal(t, string(reason), op.labels["reason"])
					found = true
				}
			}
			assert.True(t, found, "mqtt_consume_failed_total Inc not found for reason %s", reason)
		})
	}
}

// TestProviderSubscriberCollector_RecordDeadLetter verifies that the two
// dead-letter reason consts (unmarshal poison + permanent reject) increment
// mqtt_dlx_total with the correct reason label.
func TestProviderSubscriberCollector_RecordDeadLetter(t *testing.T) {
	t.Parallel()
	dlxReasons := []ConsumeFailureReason{consumeReasonUnmarshal, consumeReasonReject}
	for _, reason := range dlxReasons {
		reason := reason
		t.Run(string(reason), func(t *testing.T) {
			t.Parallel()
			spy := newSubSpyProvider()
			col, err := NewProviderSubscriberCollector(spy, "testcell")
			require.NoError(t, err)

			col.RecordDeadLetter(context.Background(), reason)

			ops := spy.ops()
			var found bool
			for _, op := range ops {
				if op.name == "mqtt_dlx_total" && op.op == "Inc" {
					assert.Equal(t, "testcell", op.labels["cell"])
					assert.Equal(t, string(reason), op.labels["reason"])
					found = true
				}
			}
			assert.True(t, found, "mqtt_dlx_total Inc not found for reason %s", reason)
		})
	}
}

// TestProviderSubscriberCollector_RecordDeadLetterFailure verifies that the two
// dead-letter reason consts increment mqtt_dlx_failed_total with the correct
// reason label (the alertable DLT-publish-failure signal — F-2, ref KIP-298).
func TestProviderSubscriberCollector_RecordDeadLetterFailure(t *testing.T) {
	t.Parallel()
	dlxReasons := []ConsumeFailureReason{consumeReasonUnmarshal, consumeReasonReject}
	for _, reason := range dlxReasons {
		reason := reason
		t.Run(string(reason), func(t *testing.T) {
			t.Parallel()
			spy := newSubSpyProvider()
			col, err := NewProviderSubscriberCollector(spy, "testcell")
			require.NoError(t, err)

			col.RecordDeadLetterFailure(context.Background(), reason)

			ops := spy.ops()
			var found bool
			for _, op := range ops {
				if op.name == "mqtt_dlx_failed_total" && op.op == "Inc" {
					assert.Equal(t, "testcell", op.labels["cell"])
					assert.Equal(t, string(reason), op.labels["reason"])
					found = true
				}
			}
			assert.True(t, found, "mqtt_dlx_failed_total Inc not found for reason %s", reason)
		})
	}
}

// TestProviderSubscriberCollector_AdjustInflight verifies AdjustInflight(+1) and
// AdjustInflight(-1) each emit a gauge Add op on mqtt_consume_inflight with the
// construction-time cell label and the delta as the value.
func TestProviderSubscriberCollector_AdjustInflight(t *testing.T) {
	t.Parallel()
	spy := newSubSpyProvider()
	col, err := NewProviderSubscriberCollector(spy, "testcell")
	require.NoError(t, err)

	col.AdjustInflight(context.Background(), 1)
	col.AdjustInflight(context.Background(), -1)

	var deltas []float64
	for _, op := range spy.ops() {
		if op.name == "mqtt_consume_inflight" && op.op == "Add" {
			assert.Equal(t, "testcell", op.labels["cell"])
			deltas = append(deltas, op.value)
		}
	}
	assert.Equal(t, []float64{1, -1}, deltas,
		"mqtt_consume_inflight must receive a +1 Add then a -1 Add")
}

// TestProviderSubscriberCollector_RegistersExpectedLabelNames pins the label
// schema: consume_total {cell}, consume_failed {cell, reason}, dlx_total
// {cell, reason}, dlx_failed {cell, reason}, duration {cell}, inflight {cell}.
func TestProviderSubscriberCollector_RegistersExpectedLabelNames(t *testing.T) {
	t.Parallel()
	spy := newSubSpyProvider()
	_, err := NewProviderSubscriberCollector(spy, "testcell")
	require.NoError(t, err)

	require.Len(t, spy.counterRegs, 4)
	assert.Equal(t, "mqtt_consume_total", spy.counterRegs[0].Name)
	assert.Equal(t, []string{"cell"}, spy.counterRegs[0].LabelNames)
	assert.Equal(t, "mqtt_consume_failed_total", spy.counterRegs[1].Name)
	assert.Equal(t, []string{"cell", "reason"}, spy.counterRegs[1].LabelNames)
	assert.Equal(t, "mqtt_dlx_total", spy.counterRegs[2].Name)
	assert.Equal(t, []string{"cell", "reason"}, spy.counterRegs[2].LabelNames)
	assert.Equal(t, "mqtt_dlx_failed_total", spy.counterRegs[3].Name)
	assert.Equal(t, []string{"cell", "reason"}, spy.counterRegs[3].LabelNames)
	require.Len(t, spy.histogramRegs, 1)
	assert.Equal(t, "mqtt_consume_duration_seconds", spy.histogramRegs[0].Name)
	assert.Equal(t, []string{"cell"}, spy.histogramRegs[0].LabelNames)
	require.Len(t, spy.gaugeRegs, 1)
	assert.Equal(t, "mqtt_consume_inflight", spy.gaugeRegs[0].Name)
	assert.Equal(t, []string{"cell"}, spy.gaugeRegs[0].LabelNames)
}

// TestNoopSubscriberCollector_Methods verifies NoopSubscriberCollector does not panic.
func TestNoopSubscriberCollector_Methods(t *testing.T) {
	t.Parallel()
	col := NoopSubscriberCollector{}
	assert.NotPanics(t, func() {
		col.RecordConsumeSuccess(context.Background(), testtime.D10ms)
		col.RecordConsumeFailure(context.Background(), consumeReasonUnmarshal)
		col.RecordDeadLetter(context.Background(), consumeReasonReject)
		col.RecordDeadLetterFailure(context.Background(), consumeReasonReject)
		col.AdjustInflight(context.Background(), 1)
		col.AdjustInflight(context.Background(), -1)
	})
}

// ---------------------------------------------------------------------------
// Subscriber test doubles
// ---------------------------------------------------------------------------

// subRegistrationFailureProvider registers metrics successfully until a configured failure
// point. Registration order is 4 counters → histogram → gauge. failAtCounter
// (1-based, 0=disabled)
// fails the Nth CounterVec call; failHistogram fails the histogram (after all 4
// counters); failGauge fails the gauge (after the 4 counters + histogram) so the
// last-registration error branch is exercised.
type subRegistrationFailureProvider struct {
	failHistogram bool
	failGauge     bool
	failAtCounter int
	counterCount  int
}

func (p *subRegistrationFailureProvider) CounterVec(opts metrics.CounterOpts) (metrics.CounterVec, error) {
	p.counterCount++
	if p.failAtCounter > 0 && p.counterCount == p.failAtCounter {
		return nil, errors.New("duplicate counter")
	}
	return &subSpyCounterVec{name: opts.Name, labelNames: opts.LabelNames}, nil
}

func (p *subRegistrationFailureProvider) HistogramVec(opts metrics.HistogramOpts) (metrics.HistogramVec, error) {
	if p.failHistogram {
		return nil, errors.New("duplicate histogram")
	}
	return metrics.NopProvider{}.HistogramVec(opts)
}

func (p *subRegistrationFailureProvider) GaugeVec(opts metrics.GaugeOpts) (metrics.GaugeVec, error) {
	if p.failGauge {
		return nil, errors.New("duplicate gauge")
	}
	return metrics.NopProvider{}.GaugeVec(opts)
}

type subSpyRecord struct {
	name   string
	op     string
	labels metrics.Labels
	value  float64
}

type subSpyProvider struct {
	counterRegs   []metrics.CounterOpts
	histogramRegs []metrics.HistogramOpts
	gaugeRegs     []metrics.GaugeOpts
	records       []subSpyRecord
}

func newSubSpyProvider() *subSpyProvider { return &subSpyProvider{} }

func (p *subSpyProvider) CounterVec(opts metrics.CounterOpts) (metrics.CounterVec, error) {
	p.counterRegs = append(p.counterRegs, opts)
	return &subSpyCounterVec{parent: p, name: opts.Name, labelNames: opts.LabelNames}, nil
}

func (p *subSpyProvider) HistogramVec(opts metrics.HistogramOpts) (metrics.HistogramVec, error) {
	p.histogramRegs = append(p.histogramRegs, opts)
	return &subSpyHistogramVec{parent: p, name: opts.Name, labelNames: opts.LabelNames}, nil
}

func (p *subSpyProvider) GaugeVec(opts metrics.GaugeOpts) (metrics.GaugeVec, error) {
	p.gaugeRegs = append(p.gaugeRegs, opts)
	return &subSpyGaugeVec{parent: p, name: opts.Name, labelNames: opts.LabelNames}, nil
}

func (p *subSpyProvider) ops() []subSpyRecord {
	out := make([]subSpyRecord, len(p.records))
	copy(out, p.records)
	return out
}

type subSpyCounterVec struct {
	parent     *subSpyProvider
	name       string
	labelNames []string
}

func (v *subSpyCounterVec) Registered() bool { return true }

func (v *subSpyCounterVec) With(l metrics.Labels) metrics.Counter {
	metrics.MustValidateLabels(v.labelNames, l)
	return &subSpyCounter{parent: v.parent, name: v.name, labels: l}
}

type subSpyCounter struct {
	parent *subSpyProvider
	name   string
	labels metrics.Labels
}

func (c *subSpyCounter) Inc(_ context.Context) {
	if c.parent == nil {
		return
	}
	c.parent.records = append(c.parent.records, subSpyRecord{
		name: c.name, op: "Inc", labels: c.labels, value: 1,
	})
}

func (c *subSpyCounter) Add(_ context.Context, d float64) {
	if c.parent == nil {
		return
	}
	c.parent.records = append(c.parent.records, subSpyRecord{
		name: c.name, op: "Add", labels: c.labels, value: d,
	})
}

type subSpyHistogramVec struct {
	parent     *subSpyProvider
	name       string
	labelNames []string
}

func (v *subSpyHistogramVec) Registered() bool { return true }

func (v *subSpyHistogramVec) With(l metrics.Labels) metrics.Histogram {
	metrics.MustValidateLabels(v.labelNames, l)
	return &subSpyHistogram{parent: v.parent, name: v.name, labels: l}
}

type subSpyHistogram struct {
	parent *subSpyProvider
	name   string
	labels metrics.Labels
}

func (h *subSpyHistogram) Observe(_ context.Context, val float64) {
	h.parent.records = append(h.parent.records, subSpyRecord{
		name: h.name, op: "Observe", labels: h.labels, value: val,
	})
}

type subSpyGaugeVec struct {
	parent     *subSpyProvider
	name       string
	labelNames []string
}

func (v *subSpyGaugeVec) Registered() bool { return true }

func (v *subSpyGaugeVec) With(l metrics.Labels) metrics.Gauge {
	metrics.MustValidateLabels(v.labelNames, l)
	return &subSpyGauge{parent: v.parent, name: v.name, labels: l}
}

type subSpyGauge struct {
	parent *subSpyProvider
	name   string
	labels metrics.Labels
}

func (g *subSpyGauge) record(op string, val float64) {
	if g.parent == nil {
		return
	}
	g.parent.records = append(g.parent.records, subSpyRecord{
		name: g.name, op: op, labels: g.labels, value: val,
	})
}

func (g *subSpyGauge) Set(_ context.Context, val float64) { g.record("Set", val) }
func (g *subSpyGauge) Inc(_ context.Context)              { g.record("Inc", 1) }
func (g *subSpyGauge) Dec(_ context.Context)              { g.record("Dec", -1) }
func (g *subSpyGauge) Add(_ context.Context, d float64)   { g.record("Add", d) }
