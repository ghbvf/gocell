package outbox

import kout "github.com/ghbvf/gocell/kernel/outbox"

// EnvelopeSchemaV1 is kept for compatibility; kernel/outbox owns the value.
const EnvelopeSchemaV1 = kout.EnvelopeSchemaV1

// ErrUnknownEnvelopeVersion is kept for compatibility; kernel/outbox owns the
// sentinel so errors.Is works across runtime and kernel callers.
var ErrUnknownEnvelopeVersion = kout.ErrUnknownEnvelopeVersion

// WireMessage is kept for compatibility; kernel/outbox owns the wire schema.
type WireMessage = kout.WireMessage

// MarshalEnvelope serializes a claimed runtime entry into the canonical v1
// wire envelope. Attempts stays runtime relay/store state and is not part of
// the kernel-owned envelope contract.
func MarshalEnvelope(entry ClaimedEntry) ([]byte, error) {
	return kout.MarshalEnvelope(entry.Entry)
}

// MarshalDirectEnvelope delegates direct-publish envelope construction to
// kernel/outbox while preserving the runtime/outbox call site.
func MarshalDirectEnvelope(topic, eventType, id string, payload []byte) []byte {
	return kout.MarshalDirectEnvelope(topic, eventType, id, payload)
}

// UnmarshalEnvelope delegates envelope decoding to kernel/outbox while
// preserving the runtime/outbox call site.
func UnmarshalEnvelope(topic string, raw []byte) (kout.Entry, error) {
	return kout.UnmarshalEnvelope(topic, raw)
}
