package outbox

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
)

// EnvelopeSchemaV1 is the canonical schema version for outbox wire envelopes.
const EnvelopeSchemaV1 = "v1"

// ErrUnknownEnvelopeVersion is returned when a wire message carries an
// unrecognized or absent schemaVersion field.
var ErrUnknownEnvelopeVersion = errcode.New(errcode.KindInvalid, errcode.ErrEnvelopeSchema,
	"outbox: unknown envelope schema version")

// wireMessage is the package-private wire envelope used by outbox relay and
// direct publisher paths across transports. It is encoded via MarshalEnvelope
// and decoded via UnmarshalEnvelope; no cross-package construction or
// json.Unmarshal-target reference is reachable — Go package-level visibility
// is the upstream Hard seal of the SafeID funnel.
//
// ID-shaped fields use idutil.SafeID, whose UnmarshalJSON fail-closes on
// unsafe characters (CWE-117 log injection) and length-cap violation. The
// closed Hard funnel:
//
//   - Downstream: SAFEID-WIREMESSAGE-USAGE-01 archtest reflectively asserts
//     every exported field is idutil.SafeID-typed (with explicit carve-outs).
//     ai-robust.md §"Hard 范本目录" 第 3 条 string-typed concept funnel.
//   - Upstream: SAFEID-UPSTREAM-FUNNEL-HARD-01 archtest asserts the struct
//     is unexported and no exported WireMessage re-export exists. Combined
//     with Go visibility, packages outside kernel/outbox cannot construct,
//     reference, or json.Unmarshal-target the envelope — only the public
//     MarshalEnvelope / UnmarshalEnvelope move bytes ↔ envelope.
//     ai-robust.md §"Hard 范本目录" sealed construction 范本.
//
// ref: go-kratos/kratos transport/grpc/codec.go — zero-size unexported codec
// struct, the closest industry equivalent (encoding/json wire decoder gated
// by an unexported type with single registration). ref: etcd-io/etcd
// server/wal/wal.go — WAL handle sealed via unexported fields + factory-only
// constructors (Create / Open / OpenForRead); cited at the handle level,
// distinct from etcd's wire-level wal.Record which has different framing
// semantics. Watermill message.Message is NOT a precedent here — its UUID /
// Metadata / Payload fields are exported, only the ack lifecycle is sealed
// via unexported channels, so envelope construction is not compile-time
// impossible cross-package.
type wireMessage struct {
	SchemaVersion string                `json:"schemaVersion"`
	ID            idutil.SafeID         `json:"id"`
	AggregateID   idutil.SafeID         `json:"aggregateId,omitempty"`
	AggregateType idutil.SafeID         `json:"aggregateType,omitempty"`
	EventType     idutil.SafeID         `json:"eventType"`
	Topic         idutil.SafeID         `json:"topic,omitempty"`
	Payload       json.RawMessage       `json:"payload"`
	Metadata      map[string]string     `json:"metadata,omitempty"`
	Observability ObservabilityMetadata `json:"observability,omitempty"`
	// Principal carries OAuth/OIDC identity (actor/subject/tenant/session)
	// across the async boundary. Populated by the producer-side bridge
	// (InjectPrincipalFromContext) and restored to consumer ctx by
	// SubscriberWithMiddleware (RestoreToContext). omitempty: zero-value
	// entries emitted by producers without a request context omit the field.
	Principal PrincipalMetadata `json:"principal,omitempty"`
	// OccurredAt is the producer-domain event time (when the business event
	// actually happened in the producer's reference frame). Distinct from
	// CreatedAt (outbox row INSERT time set by writer/store). Required on
	// wire: Entry.Validate rejects zero-value. Producers MUST set this field
	// before Writer.Write.
	//
	// ref: CloudEvents v1.0 §3 Required Attributes "time" — occurrence time.
	OccurredAt time.Time `json:"occurredAt"`
	CreatedAt  time.Time `json:"createdAt"`
}

// MarshalEnvelope serializes an Entry into the canonical v1 wire envelope.
//
// Callers are responsible for setting entry.CreatedAt before calling
// MarshalEnvelope; this function is pure (no clock interaction). The PG
// outbox writer pre-fills CreatedAt from now() at INSERT time, the relay
// reads it back from the row, and DirectEmitter sets it from its injected
// clock.Clock — all paths populate CreatedAt before reaching this function.
//
// Producer-side fail-fast: every ID-shaped field is parsed through
// idutil.ParseSafeID so an unsafe in-memory Entry surfaces an error at
// write time rather than poisoning downstream consumers (defense in depth
// against accidental Entry{ID: rawUnsafe} construction outside the
// trusted MustNewEntryID path).
func MarshalEnvelope(entry Entry) ([]byte, error) {
	id, err := idutil.ParseSafeID(entry.ID)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrEnvelopeSchema, "outbox: marshal envelope: invalid entry.ID", err)
	}
	aggID, err := idutil.ParseSafeID(entry.AggregateID)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrEnvelopeSchema, "outbox: marshal envelope: invalid entry.AggregateID", err)
	}
	aggType, err := idutil.ParseSafeID(entry.AggregateType)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrEnvelopeSchema, "outbox: marshal envelope: invalid entry.AggregateType", err)
	}
	eventType, err := idutil.ParseSafeID(entry.EventType)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrEnvelopeSchema, "outbox: marshal envelope: invalid entry.EventType", err)
	}
	topic, err := idutil.ParseSafeID(entry.RoutingTopic())
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrEnvelopeSchema, "outbox: marshal envelope: invalid entry.Topic", err)
	}
	// Producer-side observability fail-fast (PR #582 round-3 review F2/F3):
	// TraceParent is a `string` (W3C format, not SafeID) — without this
	// explicit revalidate, an unsafe TraceParent in entry.Observability
	// would slip past SafeID's UnmarshalJSON-driven funnel and reach wire.
	if err := entry.Observability.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrEnvelopeSchema,
			"outbox: marshal envelope: invalid observability", err)
	}
	// Producer-side principal fail-fast: all four SafeID fields are validated
	// before serialization. entry.Principal.Validate() already runs inside
	// entry.Validate() (called by writers), but MarshalEnvelope may be called
	// on entries that were constructed directly (e.g. relay replay), so the
	// explicit check here mirrors the Observability fail-fast pattern above.
	if err := entry.Principal.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrEnvelopeSchema,
			"outbox: marshal envelope: invalid principal", err)
	}
	msg := wireMessage{
		SchemaVersion: EnvelopeSchemaV1,
		ID:            id,
		AggregateID:   aggID,
		AggregateType: aggType,
		EventType:     eventType,
		Topic:         topic,
		Payload:       json.RawMessage(entry.Payload),
		Metadata:      entry.Metadata,
		Observability: entry.Observability,
		Principal:     entry.Principal,
		OccurredAt:    entry.OccurredAt,
		CreatedAt:     entry.CreatedAt,
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrEnvelopeSchema, "outbox: marshal envelope", err)
	}
	return b, nil
}

// UnmarshalEnvelope decodes a v1 wire envelope into an Entry. Unsafe
// ID-shaped fields (newline, length overrun, etc.) are rejected during
// json.Unmarshal via idutil.SafeID.UnmarshalJSON — wrapped here as
// ErrEnvelopeSchema for consistent error classification across the
// schema-version / missing-field / unsafe-id rejection paths.
func UnmarshalEnvelope(topic string, raw []byte) (Entry, error) {
	var msg wireMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return Entry{}, errcode.Wrap(errcode.KindInvalid, errcode.ErrEnvelopeSchema,
			"outbox: unmarshal envelope", err)
	}
	if msg.SchemaVersion != EnvelopeSchemaV1 {
		return Entry{}, ErrUnknownEnvelopeVersion
	}
	// Wire-only required-field checks that Entry.Validate cannot express:
	//   - EventType must be set on wire (Entry.Validate uses RoutingTopic
	//     fallback so Entry{Topic:"x", EventType:""} passes — wire requires
	//     EventType explicit so a stale producer cannot omit type tagging).
	//   - Payload null vs absent: wire bytes `"payload":null` produce a
	//     4-byte RawMessage `[]byte("null")` that Entry.Validate sees as
	//     non-empty. User-flagged finding: such envelopes used to flow
	//     through and reach handlers with semantically empty Entry.Payload.
	if msg.EventType == "" {
		return Entry{}, errcode.New(errcode.KindInvalid, errcode.ErrEnvelopeSchema,
			"outbox: envelope missing required field: eventType")
	}
	if len(msg.Payload) == 0 || bytes.Equal(msg.Payload, []byte("null")) {
		return Entry{}, errcode.New(errcode.KindInvalid, errcode.ErrEnvelopeSchema,
			"outbox: envelope missing required field: payload")
	}
	entryTopic := string(msg.Topic)
	if entryTopic == "" {
		entryTopic = topic
	}
	entry := Entry{
		ID:            string(msg.ID),
		AggregateID:   string(msg.AggregateID),
		AggregateType: string(msg.AggregateType),
		EventType:     string(msg.EventType),
		Topic:         entryTopic,
		Payload:       []byte(msg.Payload),
		Metadata:      msg.Metadata,
		Observability: msg.Observability,
		Principal:     msg.Principal,
		OccurredAt:    msg.OccurredAt,
		CreatedAt:     msg.CreatedAt,
	}
	// Wire-boundary single-source fail-closed (PR #582 round-3 review F3 +
	// user-flagged "missing payload" finding): defer all required-field /
	// charset / size / observability checks to Entry.Validate so a new
	// invariant on Entry automatically applies at the wire boundary too.
	// Mirrors AWS Smithy DeserializeMiddleware → ValidateInputAndOutput and
	// K8s runtime.Decode → obj.Validate() patterns.
	if err := entry.Validate(); err != nil {
		return Entry{}, errcode.Wrap(errcode.KindInvalid, errcode.ErrEnvelopeSchema,
			"outbox: envelope failed validation", err)
	}
	return entry, nil
}
