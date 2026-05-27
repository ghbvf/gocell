package ledger

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"unicode"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// minHMACKeyBytes is the smallest HMAC-SHA256 key the audit ledger accepts.
// Keys shorter than the hash output (32 bytes) violate RFC 2104 §3 and
// NIST SP 800-107 / FIPS 198-1.
//
// INVARIANT: AUDIT-HMAC-KEY-MINLEN-01
// Enforcement: Go type system — NewProtocol returns (*Protocol, error) and
// every caller is forced to handle the error. No archtest layer is added:
// no caller can construct a Protocol without going through NewProtocol.
const minHMACKeyBytes = 32

// maxNamespaceIDLen is the maximum byte length of a NamespaceID.
const maxNamespaceIDLen = 48

// RestartRecoveryMode is sealed: only types declared in this package may
// implement it (the marker method restartRecoveryModeOK is unexported).
// Callers select a concrete restart recovery shape at composition root via
// WithRestartRecovery.
type RestartRecoveryMode interface {
	restartRecoveryModeOK()
}

// RestartRecoveryStrictTailVerify configures strict tail verification on
// startup: the store must verify the tail of the hash chain before accepting
// new entries. This prevents a restarted process from appending to a
// corrupted or tampered chain.
//
// ref: google/trillian log/sequencer.go — IntegrateBatch verifies tree
// integrity before accepting new leaves.
type RestartRecoveryStrictTailVerify struct{}

// restartRecoveryModeOK is the empty seal marker — its mere presence makes
// RestartRecoveryStrictTailVerify satisfy RestartRecoveryMode at compile time.
// The unexported method prevents external packages from implementing
// RestartRecoveryMode, closing the enumeration.
func (RestartRecoveryStrictTailVerify) restartRecoveryModeOK() {}

// IdempotencyMode is sealed: only types declared in this package may
// implement it (the marker method idempotencyModeOK is unexported).
// Callers select a concrete idempotency shape at composition root via
// WithIdempotency.
type IdempotencyMode interface {
	idempotencyModeOK()
}

// IdempotencyContentFingerprint uses a HMAC-SHA256 fingerprint of the entry
// content (eventID + eventType + actorID + timestamp + payload) as the
// idempotency key. Duplicate entries with identical content are rejected with
// ErrAuditLedgerAlreadyExists.
//
// ref: google/trillian types/logroot.go — LeafIdentityHash pattern for
// content-addressed deduplication.
type IdempotencyContentFingerprint struct{}

// idempotencyModeOK is the empty seal marker.
func (IdempotencyContentFingerprint) idempotencyModeOK() {}

// NamespaceID is a typed string that identifies the owner of a ledger store
// (e.g. a cell ID). It mirrors adapters/redis.KeyNamespace validation rules:
// lowercase only, no ':', '{', '}', length ≤ 48, first char [a-z_].
type NamespaceID string

// Validate reports whether the NamespaceID satisfies all format constraints.
// Rejects: empty, contains ':', '{', '}', uppercase letters, length > 48,
// first character not in [a-z_].
func (ns NamespaceID) Validate() error {
	s := string(ns)
	if s == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger: namespace ID must not be empty")
	}
	if len(s) > maxNamespaceIDLen {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger: namespace ID exceeds maximum length",
			errcode.WithDetails(
				errcode.PublicInt("maxLength", maxNamespaceIDLen),
				errcode.PublicInt("actualLength", len(s)),
			))
	}
	first := rune(s[0])
	if first != '_' && (first < 'a' || first > 'z') {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger: namespace ID first character must be [a-z_]")
	}
	for _, r := range s {
		if r == ':' || r == '{' || r == '}' {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"audit ledger: namespace ID must not contain ':', '{', or '}'")
		}
		if unicode.IsUpper(r) {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"audit ledger: namespace ID must be lowercase")
		}
	}
	return nil
}

// ParseNamespaceID parses and validates a NamespaceID from a string.
func ParseNamespaceID(s string) (NamespaceID, error) {
	ns := NamespaceID(s)
	if err := ns.Validate(); err != nil {
		return "", err
	}
	return ns, nil
}

// Protocol bundles the protocol decisions that govern an audit ledger.
//
// Fields are required (NewProtocol fail-fasts on missing values) and are
// immutable after construction.
type Protocol struct {
	hmacKey         []byte
	namespace       NamespaceID
	restartRecovery RestartRecoveryMode
	idempotency     IdempotencyMode
}

// Namespace returns the configured namespace identifier.
func (p *Protocol) Namespace() NamespaceID { return p.namespace }

// RestartRecovery returns the configured restart recovery mode.
func (p *Protocol) RestartRecovery() RestartRecoveryMode { return p.restartRecovery }

// Idempotency returns the configured idempotency mode.
func (p *Protocol) Idempotency() IdempotencyMode { return p.idempotency }

// auditHashInput is the canonical typed input to the HMAC-SHA256 hash chain
// computation. json.Marshal serializes struct fields in source-declaration
// order (Go spec §reflect.Value.MapKeys does not apply to structs), producing
// deterministic bytes across all Go versions and platforms.
//
// Using a typed struct with JSON encoding eliminates the field-boundary
// collision risk that existed with the previous fmt.Sprintf pipe-separator
// format: JSON's quote/escape handling makes it impossible for any field value
// to shift the boundary between fields, regardless of the bytes the field
// contains (F3+F6, PR #1218 W1.2).
//
// Payload is []byte; encoding/json serializes []byte as a base64-encoded JSON
// string (RFC 4648 §4), so the Payload field is self-encoding — no manual
// hex-encoding or escaping is required.
//
// ref: google/trillian storage/leafdata.go — typed canonical input struct
// pattern for log-leaf HMAC.
// ref: RFC 8785 (JCS) — canonical JSON for deterministic signing (JCS uses
// full normalisation; here struct-order determinism is sufficient because
// auditHashInput is a private, append-only type with no external serialiser).
type auditHashInput struct {
	PrevHash           string `json:"prev_hash"`
	EventID            string `json:"event_id"`
	EventType          string `json:"event_type"`
	ActorID            string `json:"actor_id"`
	SubjectID          string `json:"subject_id"`
	TenantID           string `json:"tenant_id"`
	SessionID          string `json:"session_id"`
	CorrelationID      string `json:"correlation_id"`
	OccurredAtUnixNano int64  `json:"occurred_at_unix_nano"`
	TimestampUnixNano  int64  `json:"timestamp_unix_nano"`
	Payload            []byte `json:"payload"`
}

// ComputeHash produces the HMAC-SHA256 hex digest for an entry using the
// configured HMAC key.
//
// The HMAC message is the canonical JSON encoding of an auditHashInput struct
// (json.Marshal in source-declaration order). JSON encoding eliminates the
// field-boundary collision risk that existed in the previous pipe-separated
// fmt.Sprintf format (F3+F6): any bytes in any field are quoted/escaped by the
// JSON encoder, so an attacker cannot craft a Payload value that shifts the
// boundary between fields.
//
// Payload is encoded as a base64 JSON string by encoding/json's []byte
// handling; no manual hex-encoding is needed.
//
// No backwards compatibility: the canonical-JSON format supersedes the
// pipe-separated format introduced in B3 (issue #1042) and the hex-payload
// partial fix in PR #1218. gocell has no external deployments — existing hash
// fixtures are regenerated in the same PR.
//
// ref: google/trillian storage/leafdata.go; RFC 8785 JCS (struct-order
// determinism is sufficient for a private, append-only type).
func (p *Protocol) ComputeHash(prevHash string, e *Entry) string {
	input := auditHashInput{
		PrevHash:           prevHash,
		EventID:            e.EventID,
		EventType:          e.EventType,
		ActorID:            e.ActorID,
		SubjectID:          e.SubjectID,
		TenantID:           e.TenantID,
		SessionID:          e.SessionID,
		CorrelationID:      e.CorrelationID,
		OccurredAtUnixNano: e.OccurredAt.UnixNano(),
		TimestampUnixNano:  e.Timestamp.UnixNano(),
		Payload:            e.Payload,
	}
	// json.Marshal on a simple struct with only string/int64/[]byte fields
	// cannot return an error. The only error paths are for channels, functions,
	// and cyclic references — none of which are present here.
	msgBytes, _ := json.Marshal(input)
	mac := hmac.New(sha256.New, p.hmacKey)
	// crypto/hmac hash.Write always returns (len(b), nil) per io.Writer contract.
	mac.Write(msgBytes)
	return hex.EncodeToString(mac.Sum(nil))
}

// Option mutates a Protocol during NewProtocol. Options are applied in order;
// each Option may return an error to short-circuit construction.
type Option func(*Protocol) error

// WithChainHMAC declares the HMAC-SHA256 key used for hash chain computation.
//
// Nil and zero-length keys are rejected immediately (key must be ≥ 32 bytes
// per RFC 2104 §3). NewProtocol short-circuits on the first error — a nil key
// prevents subsequent options from running.
//
// F7: after the defensive copy is made, the caller's key slice is zeroed
// (clear(key)) so that sensitive key material does not remain live in the
// caller's memory. The Protocol retains its own internal copy.
//
// Pattern mirrors runtime/http/router.WithRateLimiter (strong-dependency wiring
// option — runtime-api.md §Option 范式分层).
func WithChainHMAC(key []byte) Option {
	return func(p *Protocol) error {
		if len(key) == 0 {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"audit ledger: HMAC key must not be nil or empty (use WithChainHMAC, key >= 32 bytes)")
		}
		if len(key) < minHMACKeyBytes {
			// Reject short keys immediately; error mentions only byte counts,
			// never the key material itself.
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"audit ledger: HMAC key too short (RFC 2104 §3, NIST SP 800-107)",
				errcode.WithDetails(
					errcode.PublicInt("minimumBytes", minHMACKeyBytes),
					errcode.PublicInt("actualBytes", len(key)),
				))
		}
		dst := make([]byte, len(key))
		copy(dst, key)
		// Zero the caller's slice immediately after the defensive copy so that
		// HMAC key material does not remain accessible in the caller's allocation.
		// The Protocol retains the only live copy.
		clear(key)
		p.hmacKey = dst
		return nil
	}
}

// WithNamespace declares the NamespaceID that prefixes all store keys for
// this ledger instance.
//
// Empty (zero-value) and invalid NamespaceID values are rejected immediately.
// NewProtocol short-circuits on the first error — an empty namespace prevents
// subsequent options from running.
// Pattern mirrors runtime/http/router.WithRateLimiter (strong-dependency
// wiring option — runtime-api.md §Option 范式分层).
func WithNamespace(ns NamespaceID) Option {
	return func(p *Protocol) error {
		if ns == "" {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"audit ledger: namespace ID must not be empty")
		}
		if err := ns.Validate(); err != nil {
			return err
		}
		p.namespace = ns
		return nil
	}
}

// WithRestartRecovery declares the restart recovery mode.
//
// Both bare-nil and typed-nil RestartRecoveryMode values are rejected
// immediately. NewProtocol short-circuits on the first error.
// Pattern mirrors runtime/http/router.WithRateLimiter
// (strong-dependency wiring option).
func WithRestartRecovery(rr RestartRecoveryMode) Option {
	return func(p *Protocol) error {
		if validation.IsNilInterface(rr) {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"audit ledger: restart recovery mode must not be nil (use WithRestartRecovery)")
		}
		p.restartRecovery = rr
		return nil
	}
}

// WithIdempotency declares the idempotency mode.
//
// Both bare-nil and typed-nil IdempotencyMode values are rejected
// immediately. NewProtocol short-circuits on the first error.
// Pattern mirrors runtime/http/router.WithRateLimiter
// (strong-dependency wiring option).
func WithIdempotency(im IdempotencyMode) Option {
	return func(p *Protocol) error {
		if validation.IsNilInterface(im) {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"audit ledger: idempotency mode must not be nil (use WithIdempotency)")
		}
		p.idempotency = im
		return nil
	}
}

// NewProtocol assembles a Protocol from the supplied options and fail-fasts
// on missing or invalid required fields. Options are applied in order; the
// first error short-circuits and no subsequent options are applied.
// The returned *Protocol is safe for concurrent read-only use.
func NewProtocol(opts ...Option) (*Protocol, error) {
	p := &Protocol{}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(p); err != nil {
			return nil, err
		}
	}
	// Zero-value defense: catch the case where no Option was passed at all.
	if len(p.hmacKey) == 0 {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger protocol: HMAC key required (use WithChainHMAC, key >= 32 bytes)")
	}
	if p.namespace == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger protocol: namespace required (use WithNamespace)")
	}
	if validation.IsNilInterface(p.restartRecovery) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger protocol: restart recovery mode required (use WithRestartRecovery)")
	}
	if validation.IsNilInterface(p.idempotency) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger protocol: idempotency mode required (use WithIdempotency)")
	}
	return p, nil
}
