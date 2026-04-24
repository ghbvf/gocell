package outbox

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	_, err := NewDirectEmitter(nil, DirectPublishFailClosed)

	assert.Error(t, err)
}

func TestDirectEmitter_EmitWrapsV1EnvelopeAndPublishes(t *testing.T) {
	publisher := &recordingEmitterPublisher{}
	emitter, err := NewDirectEmitter(publisher, DirectPublishFailClosed)
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
	emitter, err := NewDirectEmitter(publisher, DirectPublishFailClosed)
	require.NoError(t, err)

	got := emitter.Emit(context.Background(), validEntry("direct-fail-closed"))

	assert.ErrorIs(t, got, want)
}

func TestDirectEmitter_FailOpenSwallowsPublishError(t *testing.T) {
	want := errors.New("broker down")
	publisher := &recordingEmitterPublisher{err: want}
	emitter, err := NewDirectEmitter(publisher, DirectPublishFailOpen)
	require.NoError(t, err)

	got := emitter.Emit(context.Background(), validEntry("direct-fail-open"))

	assert.NoError(t, got)
	require.Len(t, publisher.calls, 1, "fail-open must still attempt publish")
}

func TestDirectEmitter_InvalidEntryFailsBeforePublish(t *testing.T) {
	publisher := &recordingEmitterPublisher{}
	emitter, err := NewDirectEmitter(publisher, DirectPublishFailOpen)
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
		tc := tc
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
	e, err := NewDirectEmitter(&recordingEmitterPublisher{}, DirectPublishFailOpen)
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
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			publisher := &recordingEmitterPublisher{err: errors.New("broker down")}
			emitter, err := NewDirectEmitter(publisher, tc.ctorMode)
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
