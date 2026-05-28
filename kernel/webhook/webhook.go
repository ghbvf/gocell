package webhook

import (
	"log/slog"
	"regexp"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
	"github.com/ghbvf/gocell/pkg/redaction"
)

// Algorithm is the webhook signing algorithm marker. HMAC-SHA256 is the only
// legal value (see ADR webhook-signing-algorithm); SHA-1 and other downgrades
// are rejected by [Algorithm.Validate].
type Algorithm string

// AlgorithmHMACSHA256 is the sole supported signing algorithm.
const AlgorithmHMACSHA256 Algorithm = "hmac-sha256"

// String returns the algorithm name as a plain string.
func (a Algorithm) String() string { return string(a) }

// Validate reports whether a is a supported algorithm.
func (a Algorithm) Validate() error {
	if a != AlgorithmHMACSHA256 {
		return errcode.New(errcode.KindInvalid, errcode.ErrWebhookAlgorithmUnsupported,
			"webhook: unsupported signing algorithm",
			errcode.WithDetails(errcode.PublicString("algorithm", string(a))))
	}
	return nil
}

// SourceID is the typed identifier for a webhook source — the secret-isolation
// key under which a [Source] is registered. Construct via [NewSourceID] /
// [MustSourceID]; the validator mirrors kernel/healthz.ProbeName (snake/kebab
// lowercase identifier).
type SourceID string

// String returns the source ID as a plain string.
func (id SourceID) String() string { return string(id) }

const sourceIDMaxLen = 64

var sourceIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// Validate reports whether id is a well-formed source ID.
func (id SourceID) Validate() error {
	if id == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook: source ID must not be empty")
	}
	if len(id) > sourceIDMaxLen {
		return errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook: source ID exceeds length budget",
			errcode.WithDetails(
				errcode.PublicInt("max", sourceIDMaxLen),
				errcode.PublicInt("got", len(id)),
			))
	}
	if !sourceIDPattern.MatchString(string(id)) {
		return errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook: source ID must match [a-z][a-z0-9_-]* regex",
			errcode.WithDetails(errcode.PublicString("sourceId", string(id))))
	}
	return nil
}

// NewSourceID validates s and returns a typed [SourceID]. It is the runtime
// entry point; tests and init use [MustSourceID].
func NewSourceID(s string) (SourceID, error) {
	id := SourceID(s)
	if err := id.Validate(); err != nil {
		return "", err
	}
	return id, nil
}

// MustSourceID is the panic variant of [NewSourceID] for test fixtures and
// package init where an invalid ID is a programmer error.
func MustSourceID(s string) SourceID {
	id, err := NewSourceID(s)
	if err != nil {
		panic(panicregister.Approved("webhook-source-id-invalid",
			errcode.Assertion("webhook.MustSourceID(%q): %v", s, err)))
	}
	return id
}

// DeliveryID is the typed per-delivery identifier. It doubles as the
// idempotency key on the receiver side and is part of the signed content, so
// it must not contain whitespace. Construct via [NewDeliveryID] /
// [MustDeliveryID].
type DeliveryID string

// String returns the delivery ID as a plain string.
func (id DeliveryID) String() string { return string(id) }

const deliveryIDMaxLen = 128

// deliveryIDPattern permits the identifier shapes real providers emit (UUIDs,
// "msg_…", "evt_…", colon/dot-segmented IDs) while forbidding whitespace — the
// signed content concatenation relies on the absence of whitespace, and the
// MAC covers the whole string so segment separators never enable forgery.
var deliveryIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*$`)

// Validate reports whether id is a well-formed delivery ID. A malformed value
// in an inbound header is a client error, so it maps to
// [errcode.ErrWebhookInvalidHeader].
func (id DeliveryID) Validate() error {
	if id == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrWebhookInvalidHeader,
			"webhook: delivery ID must not be empty")
	}
	if len(id) > deliveryIDMaxLen {
		return errcode.New(errcode.KindInvalid, errcode.ErrWebhookInvalidHeader,
			"webhook: delivery ID exceeds length budget",
			errcode.WithDetails(
				errcode.PublicInt("max", deliveryIDMaxLen),
				errcode.PublicInt("got", len(id)),
			))
	}
	if !deliveryIDPattern.MatchString(string(id)) {
		return errcode.New(errcode.KindInvalid, errcode.ErrWebhookInvalidHeader,
			"webhook: delivery ID contains forbidden characters",
			errcode.WithDetails(errcode.PublicString("deliveryId", string(id))))
	}
	return nil
}

// NewDeliveryID validates s and returns a typed [DeliveryID].
func NewDeliveryID(s string) (DeliveryID, error) {
	id := DeliveryID(s)
	if err := id.Validate(); err != nil {
		return "", err
	}
	return id, nil
}

// MustDeliveryID is the panic variant of [NewDeliveryID] for test fixtures.
func MustDeliveryID(s string) DeliveryID {
	id, err := NewDeliveryID(s)
	if err != nil {
		panic(panicregister.Approved("webhook-delivery-id-invalid",
			errcode.Assertion("webhook.MustDeliveryID(%q): %v", s, err)))
	}
	return id
}

// minSecretLen is the floor for an HMAC secret. 16 bytes (128 bits) is the
// minimum recommended for HMAC-SHA256 keying material.
const minSecretLen = 16

// Source is an opaque webhook source: an identifier plus the shared secret
// used to sign/verify its deliveries. The secret is an unexported field with
// no getter — in-package signer/verifier read it directly, package-external
// callers can only construct a Source via [NewSource] and never read the
// secret back. [Source.LogValue] redacts the secret so slog of a Source is
// always safe.
type Source struct {
	id     SourceID
	secret []byte
}

// NewSource validates id and secret and returns a [Source]. The secret must be
// at least minSecretLen bytes.
func NewSource(id SourceID, secret []byte) (Source, error) {
	if err := id.Validate(); err != nil {
		return Source{}, err
	}
	if len(secret) < minSecretLen {
		return Source{}, errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook: source secret is shorter than the minimum length",
			errcode.WithDetails(
				errcode.PublicInt("min", minSecretLen),
				errcode.PublicInt("got", len(secret)),
			))
	}
	// Defensive copy so the caller cannot mutate the secret after construction.
	cp := make([]byte, len(secret))
	copy(cp, secret)
	return Source{id: id, secret: cp}, nil
}

// ID returns the source identifier. The secret is intentionally not exposed.
func (s Source) ID() SourceID { return s.id }

// LogValue implements slog.LogValuer so that logging a Source never leaks the
// secret: the secret field is rendered as redaction.Mask.
func (s Source) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("id", string(s.id)),
		slog.String("secret", redaction.Mask),
	)
}

// Compile-time guarantee that Source redacts itself when logged — the
// type-system half of the secret-leak defense (WEBHOOK-HMAC-FUNNEL-01 B6).
// slog.Any honors slog.LogValuer, so even slog.Any("source", src) is safe.
var _ slog.LogValuer = Source{}

// Headers is the set of signature headers a [Signer] produces and a [Verifier]
// consumes. Timestamp is unix seconds as a decimal string; Signature is one or
// more space-separated "v1,<base64>" tokens.
type Headers struct {
	DeliveryID DeliveryID
	Timestamp  string
	Signature  string
}
