package webhook

import (
	"fmt"
	"log/slog"
	"net/http"
	"regexp"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/redaction"
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
// key under which a [Source] is registered. Construct via [NewSourceID]; the
// validator mirrors kernel/healthz.ProbeName (snake/kebab lowercase identifier).
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

// NewSourceID validates s and returns a typed [SourceID].
func NewSourceID(s string) (SourceID, error) {
	id := SourceID(s)
	if err := id.Validate(); err != nil {
		return "", err
	}
	return id, nil
}

// DeliveryID is the typed per-delivery identifier. It doubles as the
// idempotency key on the receiver side and is part of the signed content, so
// it must not contain whitespace. Construct via [NewDeliveryID].
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

// minSecretLen is the floor for an HMAC secret: 24 bytes (192 bits). This sits
// well above the NIST SP800-107 floor of L/2 = 128 bits for HMAC-SHA256 while
// staying compatible with real provider secrets — Svix issues 24-byte keys, so
// a higher floor (e.g. 32) would reject genuine Svix/provider secrets and break
// inbound verification. Outbound (dispatcher) secrets should prefer 32+ bytes.
const minSecretLen = 24

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
// at least 24 bytes (192-bit floor; above NIST's 128-bit HMAC-SHA256 minimum
// and compatible with provider keys such as Svix's 24-byte secrets).
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

// String implements fmt.Stringer so fmt.Sprintf("%v", src), fmt.Errorf("… %v",
// src), and log.Print(src) never leak the secret. fmt invokes String for %v,
// %s, %q, and %+v — covering every non-#v verb. slog.LogValuer only covers the
// slog package, so this closes the fmt/log/panic path (WEBHOOK-HMAC-FUNNEL-01
// secret-leak defense).
func (s Source) String() string {
	return "Source(id=" + string(s.id) + ", secret=" + redaction.Mask + ")"
}

// GoString implements fmt.GoStringer so fmt.Sprintf("%#v", src) is also safe.
func (s Source) GoString() string {
	return "webhook.Source{id:" + string(s.id) + ", secret:" + redaction.Mask + "}"
}

// Compile-time guarantee that Source redacts itself across every formatting
// path — the type-system half of the secret-leak defense
// (WEBHOOK-HMAC-FUNNEL-01 B6). slog.Any honors slog.LogValuer; fmt honors
// Stringer (%v/%s/%q/%+v) and GoStringer (%#v).
var (
	_ slog.LogValuer = Source{}
	_ fmt.Stringer   = Source{}
	_ fmt.GoStringer = Source{}
)

// Headers is the INBOUND signature-header DTO that a [Verifier] consumes. The
// receiver constructs it from the raw request headers (untrusted input), so its
// fields are exported by design — a forged Headers simply fails [Verifier.Verify].
// The OUTBOUND, provenance-sealed counterpart written to the wire is
// [SignedHeaders] (produced only by [Signer.Sign]); the two are deliberately
// distinct types so the wire-write path cannot be fed a hand-built value (#1492).
//
// Timestamp is unix seconds as a decimal string; Signature is one or more
// space-separated "v1,<base64>" tokens.
type Headers struct {
	DeliveryID DeliveryID
	Timestamp  string
	Signature  string
}

// SignedHeaders is the provenance-sealed OUTBOUND signature-header set: the value
// [Signer.Sign] produces and [SignedHeaders.Apply] writes onto an outbound
// request. All fields are unexported, INCLUDING a `valid` provenance flag that
// only [Signer.Sign] sets. A package-external caller can neither build a populated
// literal (unexported fields) nor set `valid` on a zero value, so it cannot obtain
// a valid SignedHeaders; [SignedHeaders.Apply] fail-closes (writes nothing) on a
// zero value. Hence the only SignedHeaders that can write to the wire is one
// produced by the sealed [Signer.Sign] = WEBHOOK-SIGNER-FUNNEL-01 upstream Hard
// (external; #1492, valid-token closure of the zero-value-construction gap found
// in review #1733 F1 — unexported fields alone do NOT stop `var h SignedHeaders`).
// The in-package `SignedHeaders{valid: true}` literal remains the permanent Go
// ceiling (same family as #851/#893/#1282/#1375). This is the outbound counterpart
// to the inbound [Headers] parse DTO.
type SignedHeaders struct {
	deliveryID DeliveryID
	timestamp  string
	signature  string
	// valid is the provenance flag — only Signer.Sign sets it true. A zero-value
	// SignedHeaders (the only form a package-external caller can construct) has
	// valid==false, and Apply fail-closes on it, so a non-Sign-produced value can
	// never write signature headers to the wire.
	valid bool
}

// DeliveryID returns the signed delivery identifier. These accessors expose the
// values for read-only inspection (logging, test wire reconstruction); the values
// travel on the wire in plaintext, so reading them is not a secret leak. The seal
// is on *construction* (only [Signer.Sign] produces a SignedHeaders), not reading.
func (h SignedHeaders) DeliveryID() DeliveryID { return h.deliveryID }

// Timestamp returns the signed unix-seconds timestamp string.
func (h SignedHeaders) Timestamp() string { return h.timestamp }

// Signature returns the signed "v1,<base64>" signature token(s).
func (h SignedHeaders) Signature() string { return h.signature }

// Outbound signature header names. GoCell signs outbound webhooks under the
// vendor-neutral standard-webhooks header names (webhook-id / webhook-timestamp
// / webhook-signature) rather than the Svix-branded svix-* names: the dispatcher
// is the sender, so these headers define GoCell's own outbound protocol and must
// not embed a third-party vendor name. The signed content, HMAC-SHA256, and
// "v1,<base64>" token format are Svix / standard-webhooks aligned (see signer.go
// and ADR webhook-signing-algorithm); only the header names differ.
//
// ref: standard-webhooks/standard-webhooks spec/standard-webhooks.md (webhook-id
// / webhook-timestamp / webhook-signature header names).
const (
	HeaderID        = "webhook-id"
	HeaderTimestamp = "webhook-timestamp"
	HeaderSignature = "webhook-signature"
)

// Apply writes the three signature headers (HeaderID, HeaderTimestamp,
// HeaderSignature) onto an outbound request's http.Header.
//
// It is the SOLE sanctioned writer of these header-name constants
// (WEBHOOK-SIGNER-FUNNEL-01/A1 downstream funnel): the dispatcher MUST call
// signed.Apply(req.Header) rather than Header.Set a signature header from an
// arbitrary value, and the archtest locks every Header.Set of these constants to
// this method body. That makes the *write site* uniform.
//
// Provenance is type-system closed (#1492 + review #1733 F1): Apply fail-closes on
// a zero-value SignedHeaders (valid==false) — the ONLY form a package-external
// caller can construct, since the fields (including `valid`) are unexported. Only
// [Signer.Sign] sets valid==true, so the value Apply writes always originated from
// the sealed signer; no external zero-value nor hand-built value can write
// signature headers to the wire (WEBHOOK-SIGNER-FUNNEL-01/A2 reflect freeze locks
// the field set incl. `valid`). The in-package `SignedHeaders{valid: true}` literal
// remains the permanent Go ceiling shared with #851/#893/#1282/#1375.
func (h SignedHeaders) Apply(header http.Header) {
	if !h.valid {
		// Fail-closed: a zero-value / non-Sign-produced SignedHeaders writes no
		// signature headers; the receiver then rejects the delivery (observable),
		// rather than letting an empty-signature delivery reach the wire.
		return
	}
	header.Set(HeaderID, string(h.deliveryID))
	header.Set(HeaderTimestamp, h.timestamp)
	header.Set(HeaderSignature, h.signature)
}
