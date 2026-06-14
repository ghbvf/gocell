package domain

import (
	"encoding/json"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// NonEmpty is a string guaranteed non-empty at the type-system layer.
//
// Used in PATCH-style port APIs (e.g. UserRepository.UpdateProfile) so that
// the "did the caller mean 'don't change'?" question is answered by the
// pointer kind (`*NonEmpty == nil` = skip; non-nil = write) — and the
// orthogonal "value must not be empty" question is answered at the type
// boundary, not by per-callsite runtime checks.
//
// AI-Hard 闭环: NewNonEmpty / UnmarshalJSON 是唯二经过验证的构造路径; Go
// type system 让 `*string` 调用方写不出 `repo.UpdateProfile(..., &"", ...)`
// (编译期 type mismatch)。包内显式转换 `NonEmpty("")` 仍合法 (Go 不可禁) —
// 由 archtest USERREPO-NONEMPTY-CAST-FUNNEL-01 锁包内 callsite allowlist
// (Medium 上游 + Hard 下游)。
//
// ref: ai-robust.md "string-typed concept funnel" Hard 范本
// ref: keycloak UserModel.setEmail (throws on empty)
type NonEmpty string

// ErrEmpty signals that an empty string was supplied to a NonEmpty
// constructor / JSON unmarshal path. Exported sentinel must route through
// errcode.New (EXPORTED-ERROR-NEW-01); empty string is caller input, so it is
// a KindInvalid / ErrValidationFailed (400) — callers still match it via
// errors.Is by identity, since NewNonEmpty / UnmarshalJSON return this var.
var ErrEmpty = errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
	"nonempty: empty string not allowed")

// NewNonEmpty constructs a NonEmpty value, rejecting "".
func NewNonEmpty(s string) (NonEmpty, error) {
	if s == "" {
		return "", ErrEmpty
	}
	return NonEmpty(s), nil
}

// String returns the underlying value (read-only access; conversion back to
// untyped string for legacy adapters that bind plain strings to DB drivers).
func (n NonEmpty) String() string { return string(n) }

// UnmarshalJSON validates that the JSON source is a non-empty string. JSON
// null leaves *NonEmpty as nil (caller's pointer field remains unset) —
// callers express "not provided" via JSON null, "provided non-empty" via
// JSON string. JSON empty string ("") is rejected with ErrEmpty.
func (n *NonEmpty) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	if s == "" {
		return ErrEmpty
	}
	*n = NonEmpty(s)
	return nil
}

// MarshalJSON encodes NonEmpty as a plain JSON string. Wire format is
// indistinguishable from `string`; the typed wrapper is a server-side
// validation tool, not a wire-protocol change.
func (n NonEmpty) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(n))
}
