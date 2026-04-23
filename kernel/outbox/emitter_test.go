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
