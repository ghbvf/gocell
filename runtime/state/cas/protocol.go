package cas

import (
	"fmt"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// ConflictPolicy is a sealed marker interface — it carries no behavior, only
// type discrimination. Today only ConflictPolicyStrictReject is implemented;
// future siblings (LastWriteWins / RetryWithMerge) may be added as additional
// types in this package. The marker method conflictPolicyOK is unexported, so
// external packages cannot implement ConflictPolicy — this prevents callers
// from injecting unsanctioned conflict strategies past the runtime/state/cas
// API boundary. Matches the sealed-marker pattern in runtime/auth/session
// (FingerprintMode / OrderingModel) and runtime/audit/ledger (RestartRecoveryMode
// / IdempotencyMode).
type ConflictPolicy interface {
	conflictPolicyOK()
}

// ConflictPolicyStrictReject means CAS mismatch returns ErrVersionConflict
// (HTTP 409) without retry. This is the safe default and the only policy
// implemented today.
type ConflictPolicyStrictReject struct{}

// conflictPolicyOK is the sealed-marker method. Intentionally empty: presence
// of the method (not its body) is what implements ConflictPolicy, denying
// out-of-package types from satisfying the interface.
func (ConflictPolicyStrictReject) conflictPolicyOK() {}

// Protocol bundles CAS protocol decisions for a particular consumer (cell).
// Construct via NewProtocol or MustNewProtocol (composition root only).
type Protocol struct {
	versionField    string
	versionFieldSet bool
	conflict        ConflictPolicy
	conflictNil     bool // sentinel: WithConflictPolicy received a typed-nil
}

// VersionField returns the configured DB column / domain field name that
// carries the monotonic version counter.
func (p *Protocol) VersionField() string { return p.versionField }

// Conflict returns the configured ConflictPolicy.
func (p *Protocol) Conflict() ConflictPolicy { return p.conflict }

// Option configures a Protocol during construction.
type Option func(*Protocol) error

// WithVersionField names the DB column / domain field that carries the
// monotonic version counter (e.g. "password_version", "version"). Required.
// Empty string is rejected at Option apply time.
func WithVersionField(name string) Option {
	return func(p *Protocol) error {
		if name == "" {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"cas protocol: WithVersionField requires non-empty name")
		}
		p.versionField = name
		p.versionFieldSet = true
		return nil
	}
}

// WithConflictPolicy sets the policy. Nil interface (bare or typed) is
// sticky-rejected via sentinel — fail-fast at NewProtocol.
//
// Both bare-nil and typed-nil ConflictPolicy values are rejected by
// NewProtocol so the conflict policy is never silently absent. Pattern mirrors
// runtime/auth/session.WithFingerprint (strong-dependency wiring option).
func WithConflictPolicy(c ConflictPolicy) Option {
	return func(p *Protocol) error {
		if validation.IsNilInterface(c) {
			p.conflictNil = true
			return nil
		}
		p.conflict = c
		return nil
	}
}

// NewProtocol fail-fasts on missing required fields and typed-nil.
// Defaults conflict policy to ConflictPolicyStrictReject when omitted.
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
	if !p.versionFieldSet {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cas protocol: version field required (use WithVersionField)")
	}
	if p.conflictNil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cas protocol: typed-nil conflict policy rejected")
	}
	if p.conflict == nil {
		p.conflict = ConflictPolicyStrictReject{}
	}
	return p, nil
}

// MustNewProtocol panics on misconfiguration. Use only in composition root
// (cmd/*); CAS-PROTOCOL-COMPOSITION-ROOT-01 archtest enforces this. B-class
// panic: programming error visible at process startup.
//
// B 类 panic（参数约定违反，编程错误）：composition-root 静态字面量配错；
// Must* 是 fail-fast 包装，在进程启动时立刻暴露配置错误。
func MustNewProtocol(opts ...Option) *Protocol {
	p, err := NewProtocol(opts...)
	if err != nil {
		panic(err)
	}
	return p
}

// CheckVersionMatch translates UPDATE/DELETE ... WHERE version=$expected
// RowsAffected outcome into the standard CAS error vocabulary.
//
//   - rowsAffected == 1: nil (caller applied successfully)
//   - rowsAffected == 0: ErrVersionConflict (KindConflict / HTTP 409) — caller
//     may follow up with a NotFound probe (separate GetByKey) to distinguish
//     "key absent" from "version mismatch"; cas treats both as conflict from
//     CAS perspective and lets the caller decide upstream error mapping.
//   - rowsAffected > 1: also returns ErrVersionConflict — the WHERE clause
//     matched more rows than expected, indicating a schema or query error.
//
// entityDesc and entityKey are persisted in the Internal field (server-side
// slog only) — they are NOT emitted to the HTTP wire as Details. This avoids
// leaking entity type / record identifiers (userID, config key, etc.) to
// clients via the standard error envelope. Operators retrieve them from the
// service log when correlating retries.
//
// Callers MUST distinguish key-absent vs version-mismatch by probing existence
// first (e.g. via SELECT FOR UPDATE before UPDATE, or a GetByKey probe after
// UPDATE/DELETE returns no rows). CheckVersionMatch unconditionally translates
// rowsAffected==0 to ErrVersionConflict; the caller's pre-check provides the
// NotFound branch. Without a pre-check, a DELETE on a non-existent key returns
// ErrVersionConflict (409) instead of the correct ErrNotFound (404).
func CheckVersionMatch(rowsAffected int64, entityDesc, entityKey string) error {
	if rowsAffected == 1 {
		return nil
	}
	return errcode.New(errcode.KindConflict, errcode.ErrVersionConflict,
		"concurrent update detected; reload and retry",
		errcode.WithInternal(fmt.Sprintf("cas conflict: entity=%s key=%s rowsAffected=%d",
			entityDesc, entityKey, rowsAffected)))
}
