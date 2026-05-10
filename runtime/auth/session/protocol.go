package session

import (
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// FingerprintMode is sealed: only types declared in this package may implement
// it (the marker method fingerprintModeOK is unexported). Callers select a
// concrete fingerprint shape at composition root via WithFingerprint.
//
// Future opaque-token deployments may add a sibling type (e.g.
// FingerprintHMACSha256) without breaking the existing API; jti-only is the
// only shape supported today (see ADR D1).
type FingerprintMode interface {
	fingerprintModeOK()
}

// FingerprintJTIRef stores the JWT jti claim reference (RFC 9068 §2.2.4) on
// the server side. Session rows persist {sid, jti, authz_epoch_at_issue}; no
// token plaintext or HMAC fingerprint is stored.
type FingerprintJTIRef struct{}

func (FingerprintJTIRef) fingerprintModeOK() {}

// CredentialEvent enumerates credential state changes that revoke active
// sessions and refresh chains for the affected subject (ADR D3 — fail-closed
// by default; permission removal routes through CredentialEventRoleRevoke).
type CredentialEvent int

const (
	// CredentialEventPasswordReset is emitted when a user's password is reset
	// (forced reset, self-service change, or operator-initiated).
	CredentialEventPasswordReset CredentialEvent = iota + 1
	// CredentialEventLock is emitted when an account transitions to a locked
	// state (manual lock or auto-lock from failed-login threshold).
	CredentialEventLock
	// CredentialEventDelete is emitted when a user is deleted.
	CredentialEventDelete
	// CredentialEventRoleRevoke is emitted when any role assignment changes
	// for the user (revoke, downgrade, or permission-set change).
	CredentialEventRoleRevoke
)

// String returns a stable textual label suitable for diagnostics, storetest
// case names, and slog attributes.
func (e CredentialEvent) String() string {
	switch e {
	case CredentialEventPasswordReset:
		return "PasswordReset"
	case CredentialEventLock:
		return "Lock"
	case CredentialEventDelete:
		return "Delete"
	case CredentialEventRoleRevoke:
		return "RoleRevoke"
	default:
		return "Unknown"
	}
}

// OrderingModel is sealed: only types declared in this package may implement
// it. Callers select a concrete ordering primitive at composition root via
// WithOrdering. Future alternatives (advisory lock / row version) may be
// added as sibling types if a use case emerges.
type OrderingModel interface {
	orderingModelOK()
}

// OrderingAuthzEpoch uses a per-user monotonic epoch column to invalidate
// stale tokens (OAuth Security BCP §4.13.1). Session rows snapshot the epoch
// at issuance; validate rejects when claim.epoch < user.authz_epoch.
type OrderingAuthzEpoch struct{}

func (OrderingAuthzEpoch) orderingModelOK() {}

// Protocol bundles the protocol decisions that govern a session subsystem.
//
// Fields are required (NewProtocol fail-fasts on missing values) and are
// immutable after construction. Accessor methods return defensive copies
// where applicable.
type Protocol struct {
	fingerprint FingerprintMode
	revokeOn    []CredentialEvent
	ordering    OrderingModel
}

// Fingerprint returns the configured fingerprint mode.
func (p *Protocol) Fingerprint() FingerprintMode { return p.fingerprint }

// Ordering returns the configured ordering model.
func (p *Protocol) Ordering() OrderingModel { return p.ordering }

// RevokeOn returns a defensive copy of the configured credential events.
// Callers must not assume the returned slice retains its underlying array.
func (p *Protocol) RevokeOn() []CredentialEvent {
	out := make([]CredentialEvent, len(p.revokeOn))
	copy(out, p.revokeOn)
	return out
}

// Option mutates a Protocol during NewProtocol. Options are applied in order;
// each Option may return an error to short-circuit construction.
type Option func(*Protocol) error

// WithFingerprint declares the token fingerprint mode.
//
// This is a strong-dependency wiring option (see runtime-api.md §Option 范式
// 分层): a typed-nil value is rejected at construction time. There is no
// "accumulate" semantics — a second WithFingerprint call overwrites the
// previous value, which would be a wiring contradiction.
func WithFingerprint(fp FingerprintMode) Option {
	return func(p *Protocol) error {
		if validation.IsNilInterface(fp) {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"session protocol: fingerprint mode required (use WithFingerprint)")
		}
		p.fingerprint = fp
		return nil
	}
}

// WithOrdering declares the login-vs-revoke ordering primitive (ADR D2).
//
// Strong-dependency wiring option: typed-nil is rejected at construction.
func WithOrdering(om OrderingModel) Option {
	return func(p *Protocol) error {
		if validation.IsNilInterface(om) {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"session protocol: ordering model required (use WithOrdering)")
		}
		p.ordering = om
		return nil
	}
}

// WithRevokeOn declares a set of credential events that revoke active
// sessions and refresh chains for the affected subject (ADR D3).
//
// This is a builder-style option: multiple WithRevokeOn calls accumulate;
// duplicates are deduplicated, preserving the order of first occurrence.
// Calling WithRevokeOn() with zero events is a misuse and is rejected at
// construction (≥1 event required for fail-closed semantics).
func WithRevokeOn(events ...CredentialEvent) Option {
	return func(p *Protocol) error {
		if len(events) == 0 {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"session protocol: WithRevokeOn requires at least one event")
		}
		seen := make(map[CredentialEvent]struct{}, len(p.revokeOn)+len(events))
		for _, e := range p.revokeOn {
			seen[e] = struct{}{}
		}
		for _, e := range events {
			if _, dup := seen[e]; dup {
				continue
			}
			seen[e] = struct{}{}
			p.revokeOn = append(p.revokeOn, e)
		}
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
	if validation.IsNilInterface(p.fingerprint) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"session protocol: fingerprint mode required (use WithFingerprint)")
	}
	if validation.IsNilInterface(p.ordering) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"session protocol: ordering model required (use WithOrdering)")
	}
	if len(p.revokeOn) == 0 {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"session protocol: WithRevokeOn must declare at least one event")
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
		panic(err)
	}
	return p
}
