package ledger

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
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
// (e.g. a cell ID). The legal set is restricted to [a-z_] with length ≤ 48:
// every byte must be a lowercase ASCII letter or underscore. This is stricter
// than adapters/redis.KeyNamespace ([a-z0-9_-]) on purpose — the namespace is
// the first signed field of the HMAC chain (cross-namespace domain separation,
// ADR-1042 §A), so its character set is frozen narrow to avoid any ambiguity in
// the canonical digest input.
type NamespaceID string

// Validate reports whether the NamespaceID satisfies all format constraints:
// non-empty, length ≤ 48, and every byte in [a-z_]. Any digit, dash, dot,
// uppercase letter, ':' / '{' / '}', or non-ASCII byte is rejected.
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
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '_' && (c < 'a' || c > 'z') {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"audit ledger: namespace ID must contain only [a-z_]",
				errcode.WithDetails(
					errcode.PublicInt("offset", i),
					errcode.PublicInt("byte", int(c)),
				))
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
// order (Go spec — reflect.Value.MapKeys does not apply to structs), producing
// deterministic bytes across all Go versions and platforms.
//
// Using a typed struct with JSON encoding eliminates the field-boundary
// collision risk that existed with the prior pipe-separated fmt.Sprintf format:
// JSON's quote/escape handling makes it impossible for any field value to
// shift the boundary between fields, regardless of the bytes a field contains
// (PR #1218 F3+F6).
//
// Payload is []byte; encoding/json serializes []byte as a base64-encoded JSON
// string (RFC 4648 §4), so Payload is self-encoding — no manual hex/base64
// step is required.
//
// The type is unexported (package-private) so external packages cannot
// construct, alias, or re-shape an equivalent struct. Combined with the
// archtest funnel AUDIT-HASH-INPUT-FROZEN-01 (which locks the field set +
// order + JSON tags by reflect and locks hmac.New callsites to ComputeHash by
// AST), this struct is the single source of truth for the HMAC message and
// cannot be bypassed.
//
// ref: google/trillian storage/leafdata.go — typed canonical input struct
// pattern for log-leaf HMAC.
// ref: RFC 8785 (JCS) — canonical JSON for deterministic signing (struct-order
// determinism is sufficient here because auditHashInput is a private,
// append-only type with no external serialiser).
type auditHashInput struct {
	// Namespace anchors the chain to its owner cell. Cross-namespace HMAC
	// replay (the same entry shape copied from chain A into chain B) is
	// invalid because the namespace bytes participate in the digest.
	// ref: google/trillian — TreeID participates in SignedEntryTimestamp.
	Namespace          string `json:"namespace"`
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
// (json.Marshal in source-declaration order). The 12-field canonical-JSON
// format supersedes the prior pipe-separated fmt.Sprintf format introduced in
// 020_audit_ledger.sql; both the field-boundary collision risk (any bytes in
// a field could shift `|` semantics) and the lack of OAuth Principal /
// CorrelationID / OccurredAt coverage are closed in one rewrite.
//
// There is no protocol version byte and no legacy path: per CLAUDE.md
// "Review 和重构时不考虑向后兼容——当前只有 gocell 自身", existing audit
// rows in the pre-043 schema are discarded (DROP TABLE in 043_audit_entries_v2.sql)
// and all hash fixture expectations are regenerated in the same PR.
//
// Payload is encoded as a base64 JSON string by encoding/json's []byte
// handling; no manual hex-encoding is needed.
//
// INVARIANT: AUDIT-HASH-INPUT-FROZEN-01 — the auditHashInput struct shape +
// hmac.New callsite uniqueness are double-locked by archtest. ComputeHash is
// the only place in the audit ledger package that may construct an HMAC over
// audit data.
//
// ref: google/trillian storage/leafdata.go (canonical input struct).
// ref: RFC 8785 JCS.
// ref: tools/archtest/audit_hash_input_frozen_test.go.
func (p *Protocol) ComputeHash(prevHash string, e *Entry) string {
	input := auditHashInput{
		Namespace:          string(p.namespace),
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
	// json.Marshal on a struct of string / int64 / []byte fields cannot
	// return a non-nil error in practice: the only error paths are channel /
	// function / cyclic-reference values, none of which auditHashInput
	// contains (AUDIT-HASH-INPUT-FROZEN-01 A1 reflect-locks the field set).
	// Treat any future regression as an unreachable-branch programmer error:
	// panic via the registered funnel so the audit chain hash is never
	// silently computed over an empty message.
	msgBytes, err := json.Marshal(input)
	if err != nil {
		panic(panicregister.Approved(
			"audit-hash-input-marshal-unreachable",
			errcode.Assertion("audit ledger: auditHashInput json.Marshal returned error (unreachable per AUDIT-HASH-INPUT-FROZEN-01 A1)"),
		))
	}
	mac := hmac.New(sha256.New, p.hmacKey)
	// crypto/hmac hash.Write always returns (len(b), nil) per io.Writer contract.
	mac.Write(msgBytes)
	return hex.EncodeToString(mac.Sum(nil))
}

// Option mutates a Protocol during NewProtocol. Options are applied in order;
// each Option may return an error to short-circuit construction.
//
// The mandatory namespace + HMAC key pair is passed positionally to NewProtocol
// — no Option can supply them, and no Option can override them. This forces
// the namespace ↔ key binding to be expressed at the type-system layer, so a
// caller cannot accidentally pair the wrong namespace with a stale or shared
// key (which would let HMAC chains from different cells be substituted across
// the wire). The compile error a caller gets when trying to construct a
// Protocol without supplying both arguments is the funnel's upstream Hard
// gate; archtest backs that up at the callsite layer.
type Option func(*Protocol) error

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

// NewProtocol assembles a Protocol from the namespace + HMAC key (mandatory
// positional arguments) and the supplied options. The positional form binds
// `namespace` and `key` at the type-system layer — a caller physically cannot
// pass just one, nor swap them, nor reuse a stale key against a fresh
// namespace silently. After the defensive HMAC key copy is made, the caller's
// `key` slice is zeroed (clear) so sensitive material is not retained in
// caller memory (F7).
//
// Options are applied in order; the first error short-circuits and no
// subsequent options are applied. The returned *Protocol is safe for
// concurrent read-only use.
//
// INVARIANT: AUDIT-HASH-INPUT-FROZEN-01 (positional binding closes the
// upstream "wrong namespace ↔ wrong key" attack vector at compile time;
// downstream Hard is provided by Protocol.ComputeHash hmac.New callsite
// uniqueness).
func NewProtocol(namespace NamespaceID, key []byte, opts ...Option) (*Protocol, error) {
	if namespace == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger protocol: namespace must not be empty")
	}
	if err := namespace.Validate(); err != nil {
		return nil, err
	}
	if len(key) == 0 {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger protocol: HMAC key must not be nil or empty (key >= 32 bytes)")
	}
	if len(key) < minHMACKeyBytes {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger protocol: HMAC key too short (RFC 2104 §3, NIST SP 800-107)",
			errcode.WithDetails(
				errcode.PublicInt("minimumBytes", minHMACKeyBytes),
				errcode.PublicInt("actualBytes", len(key)),
			))
	}
	dst := make([]byte, len(key))
	copy(dst, key)
	// Zero the caller's slice immediately after the defensive copy so that HMAC
	// key material does not remain accessible in the caller's allocation. The
	// Protocol retains the only live copy.
	clear(key)
	p := &Protocol{
		hmacKey:   dst,
		namespace: namespace,
	}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(p); err != nil {
			return nil, err
		}
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
