package ledger

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
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
				slog.Int("maxLength", maxNamespaceIDLen),
				slog.Int("actualLength", len(s)),
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
// immutable after construction. Accessor methods return defensive copies
// where applicable.
type Protocol struct {
	hmacKey            []byte
	hmacKeyNil         bool // sentinel: WithChainHMAC received a nil/empty key
	namespace          NamespaceID
	namespaceNil       bool // sentinel: WithNamespace was never called
	restartRecovery    RestartRecoveryMode
	restartRecoveryNil bool // sentinel: WithRestartRecovery received a nil interface value
	idempotency        IdempotencyMode
	idempotencyNil     bool // sentinel: WithIdempotency received a nil interface value
}

// HMACKey returns a defensive copy of the configured HMAC key.
// The returned slice must not be used for logging or error messages —
// HMAC key bytes are secret material.
func (p *Protocol) HMACKey() []byte {
	out := make([]byte, len(p.hmacKey))
	copy(out, p.hmacKey)
	return out
}

// Namespace returns the configured namespace identifier.
func (p *Protocol) Namespace() NamespaceID { return p.namespace }

// RestartRecovery returns the configured restart recovery mode.
func (p *Protocol) RestartRecovery() RestartRecoveryMode { return p.restartRecovery }

// Idempotency returns the configured idempotency mode.
func (p *Protocol) Idempotency() IdempotencyMode { return p.idempotency }

// ComputeHash produces the HMAC-SHA256 hex digest for an entry using the
// configured HMAC key. The message format is byte-for-byte compatible with
// cells/auditcore/internal/domain/hashchain.go computeHash:
//
//	msg = prevHash|eventID|eventType|actorID|UnixNano|payload
//
// ref: cells/auditcore/internal/domain/hashchain.go computeHash (must remain
// byte-for-byte equivalent to preserve chain continuity when PG store lands).
func (p *Protocol) ComputeHash(prevHash string, e *Entry) string {
	mac := hmac.New(sha256.New, p.hmacKey)
	msg := fmt.Sprintf("%s|%s|%s|%s|%d|%s",
		prevHash,
		e.EventID,
		e.EventType,
		e.ActorID,
		e.Timestamp.UnixNano(),
		string(e.Payload),
	)
	// crypto/hmac hash.Write always returns (len(b), nil) per io.Writer contract.
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil))
}

// Option mutates a Protocol during NewProtocol. Options are applied in order;
// each Option may return an error to short-circuit construction.
type Option func(*Protocol) error

// WithChainHMAC declares the HMAC-SHA256 key used for hash chain computation.
//
// Both nil and zero-length keys are rejected by NewProtocol (key must be ≥ 32
// bytes per RFC 2104 §3). The nil sentinel is sticky: once set, a subsequent
// valid WithChainHMAC call does NOT clear it — misconfiguration must surface
// at startup rather than being silently masked.
//
// Pattern mirrors runtime/http/router.WithRateLimiter (strong-dependency wiring
// option — runtime-api.md §Option 范式分层).
func WithChainHMAC(key []byte) Option {
	return func(p *Protocol) error {
		if len(key) == 0 {
			p.hmacKeyNil = true
			return nil
		}
		if len(key) < minHMACKeyBytes {
			// Reject short keys immediately; error mentions only byte counts,
			// never the key material itself.
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"audit ledger: HMAC key too short (RFC 2104 §3, NIST SP 800-107)",
				errcode.WithDetails(
					slog.Int("minimumBytes", minHMACKeyBytes),
					slog.Int("actualBytes", len(key)),
				))
		}
		dst := make([]byte, len(key))
		copy(dst, key)
		p.hmacKey = dst
		return nil
	}
}

// WithNamespace declares the NamespaceID that prefixes all store keys for
// this ledger instance.
//
// Both bare-nil (zero-value NamespaceID "") and invalid values are rejected
// by NewProtocol. Pattern mirrors runtime/http/router.WithRateLimiter
// (strong-dependency wiring option).
func WithNamespace(ns NamespaceID) Option {
	return func(p *Protocol) error {
		if ns == "" {
			p.namespaceNil = true
			return nil
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
// Both bare-nil and typed-nil are rejected by NewProtocol so the recovery
// mode is never silently absent. The nil sentinel is sticky: once set, a
// subsequent valid WithRestartRecovery call does NOT clear it.
// Pattern mirrors runtime/http/router.WithRateLimiter
// (strong-dependency wiring option).
func WithRestartRecovery(rr RestartRecoveryMode) Option {
	return func(p *Protocol) error {
		if validation.IsNilInterface(rr) {
			p.restartRecoveryNil = true
			return nil
		}
		p.restartRecovery = rr
		return nil
	}
}

// WithIdempotency declares the idempotency mode.
//
// Both bare-nil and typed-nil are rejected by NewProtocol so the idempotency
// mode is never silently absent. The nil sentinel is sticky: once set, a
// subsequent valid WithIdempotency call does NOT clear it.
// Pattern mirrors runtime/http/router.WithRateLimiter
// (strong-dependency wiring option).
func WithIdempotency(im IdempotencyMode) Option {
	return func(p *Protocol) error {
		if validation.IsNilInterface(im) {
			p.idempotencyNil = true
			return nil
		}
		p.idempotency = im
		return nil
	}
}

// NewProtocol assembles a Protocol from the supplied options and fail-fasts
// on missing required fields. The returned *Protocol is safe for concurrent
// read-only use.
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
	if p.hmacKeyNil || len(p.hmacKey) == 0 {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger protocol: HMAC key required (use WithChainHMAC, key >= 32 bytes)")
	}
	if p.namespaceNil || p.namespace == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger protocol: namespace required (use WithNamespace)")
	}
	if p.restartRecoveryNil || validation.IsNilInterface(p.restartRecovery) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger protocol: restart recovery mode required (use WithRestartRecovery)")
	}
	if p.idempotencyNil || validation.IsNilInterface(p.idempotency) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger protocol: idempotency mode required (use WithIdempotency)")
	}
	return p, nil
}

// MustNewProtocol is the composition-root convenience wrapper around
// NewProtocol. It panics on validation failure to surface misconfiguration at
// process startup. Use only from cmd/* (composition root); cells must consume
// an injected *Protocol.
func MustNewProtocol(opts ...Option) *Protocol {
	p, err := NewProtocol(opts...)
	if err != nil {
		// B 类 panic（参数约定违反，编程错误）：composition-root 静态字面量配错；
		// Must* 是 fail-fast 包装，在进程启动时立刻暴露配置错误。
		panic(err)
	}
	return p
}
