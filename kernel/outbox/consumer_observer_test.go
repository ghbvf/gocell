package outbox

import (
	"testing"
)

func TestNopConsumerObserver_ObserveReject_DoesNotPanic(t *testing.T) {
	var o ConsumerObserver = NopConsumerObserver{}
	// All argument shapes must be safe; the Nop swallows them.
	o.ObserveReject("accesscore", "topic.foo", "cg-accesscore-foo", ConsumerRejectReasonHandlerReject)
	o.ObserveReject("", "", "", "")
	o.ObserveReject("auditcore", "topic.bar", "cg-auditcore-bar", ConsumerRejectReasonRetryExhausted)
}

func TestConsumerRejectReason_Constants(t *testing.T) {
	tests := map[string]string{
		"ConsumerRejectReasonHandlerReject":  ConsumerRejectReasonHandlerReject,
		"ConsumerRejectReasonRetryExhausted": ConsumerRejectReasonRetryExhausted,
	}
	want := map[string]string{
		"ConsumerRejectReasonHandlerReject":  "handler_reject",
		"ConsumerRejectReasonRetryExhausted": "retry_exhausted",
	}
	for name, got := range tests {
		if got != want[name] {
			t.Fatalf("%s: want %q, got %q", name, want[name], got)
		}
	}
}

// TestIsNilObserver verifies typed-nil detection through the shared
// pkg/validation helper. Both bare nil and a typed-nil wrapped in the
// ConsumerObserver interface must return true.
func TestIsNilObserver(t *testing.T) {
	if !isNilObserver(nil) {
		t.Fatal("isNilObserver(nil) must be true")
	}

	// Typed-nil shape: var p *NopConsumerObserver = nil — but NopConsumerObserver
	// is a value type, not pointer; use a pointer to it.
	var p *NopConsumerObserver
	var o ConsumerObserver = p
	if !isNilObserver(o) {
		t.Fatal("isNilObserver(typed-nil *NopConsumerObserver) must be true")
	}

	// Non-nil concrete: must be false.
	if isNilObserver(NopConsumerObserver{}) {
		t.Fatal("isNilObserver(NopConsumerObserver{}) must be false")
	}
}

// fakeObserver is a test spy used by ConsumerBase tests to verify
// ObserveReject is called with the expected dimensions.
type fakeObserver struct {
	calls []rejectCall
}

type rejectCall struct {
	cellID        string
	topic         string
	consumerGroup string
	reason        string
}

func (f *fakeObserver) ObserveReject(cellID, topic, consumerGroup, reason string) {
	f.calls = append(f.calls, rejectCall{cellID, topic, consumerGroup, reason})
}
