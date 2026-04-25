package outbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/outbox"
)

type sampleEvent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// unmarshalableEvent forces json.Marshal to return an error by implementing
// json.Marshaler and returning one. Used to exercise the marshal-error path.
type unmarshalableEvent struct{}

func (unmarshalableEvent) MarshalJSON() ([]byte, error) {
	return nil, errors.New("intentional marshal failure")
}

type captureEmitter struct {
	entries []outbox.Entry
	err     error
}

func (c *captureEmitter) Emit(_ context.Context, entry outbox.Entry) error {
	if c.err != nil {
		return c.err
	}
	c.entries = append(c.entries, entry)
	return nil
}

func TestEmit_SuccessMarshalsAndDelegates(t *testing.T) {
	t.Parallel()

	const topic = "event.sample.v1"
	cap := &captureEmitter{}
	payload := sampleEvent{ID: "abc", Name: "demo"}

	if err := outbox.Emit(context.Background(), cap, topic, payload); err != nil {
		t.Fatalf("Emit returned error: %v", err)
	}

	if got := len(cap.entries); got != 1 {
		t.Fatalf("expected 1 captured entry, got %d", got)
	}
	entry := cap.entries[0]
	if entry.EventType != topic {
		t.Fatalf("EventType = %q, want %q", entry.EventType, topic)
	}
	if !strings.HasPrefix(entry.ID, outbox.EntryIDPrefix) {
		t.Fatalf("Entry.ID = %q, expected evt- prefix", entry.ID)
	}
	var round sampleEvent
	if err := json.Unmarshal(entry.Payload, &round); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if round != payload {
		t.Fatalf("payload round-trip mismatch: got %+v, want %+v", round, payload)
	}
}

func TestEmit_MarshalErrorWrapsAndDoesNotEmit(t *testing.T) {
	t.Parallel()

	const topic = "event.sample.v1"
	cap := &captureEmitter{}

	err := outbox.Emit(context.Background(), cap, topic, unmarshalableEvent{})
	if err == nil {
		t.Fatal("expected marshal error, got nil")
	}
	if !strings.Contains(err.Error(), "outbox.Emit("+topic+")") {
		t.Fatalf("error missing topic context: %v", err)
	}
	if !strings.Contains(err.Error(), "marshal payload") {
		t.Fatalf("error missing 'marshal payload' context: %v", err)
	}
	if len(cap.entries) != 0 {
		t.Fatalf("emitter should not have been called on marshal failure, got %d entries", len(cap.entries))
	}
}

func TestEmit_EmitterErrorWraps(t *testing.T) {
	t.Parallel()

	const topic = "event.sample.v1"
	sentinel := errors.New("broker unavailable")
	cap := &captureEmitter{err: sentinel}

	err := outbox.Emit(context.Background(), cap, topic, sampleEvent{ID: "x"})
	if err == nil {
		t.Fatal("expected emitter error, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected error to wrap sentinel, got %v", err)
	}
	if !strings.Contains(err.Error(), "outbox.Emit("+topic+")") {
		t.Fatalf("error missing topic context: %v", err)
	}
}
