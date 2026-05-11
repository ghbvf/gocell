package cas_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/state/cas"
)

// Compile-time verification that the package-internal type satisfies the
// sealed marker interface. The marker method conflictPolicyOK is unexported,
// so external packages cannot satisfy ConflictPolicy — any attempt would fail
// to compile.
var _ cas.ConflictPolicy = cas.ConflictPolicyStrictReject{}

// TestNewProtocol_RequiresVersionField: no WithVersionField → ErrValidationFailed.
func TestNewProtocol_RequiresVersionField(t *testing.T) {
	t.Parallel()
	p, err := cas.NewProtocol()
	if err == nil {
		t.Fatalf("expected error for missing version field, got nil; protocol=%+v", p)
	}
	if p != nil {
		t.Fatalf("expected nil protocol on error, got %+v", p)
	}
	var coded *errcode.Error
	if !errors.As(err, &coded) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if coded.Code != errcode.ErrValidationFailed {
		t.Errorf("expected ErrValidationFailed, got %s", coded.Code)
	}
	if !strings.Contains(coded.Message, "version field") {
		t.Errorf("expected message to mention version field, got %q", coded.Message)
	}
}

// TestNewProtocol_RejectsEmptyVersionField: WithVersionField("") returns error
// at Option apply time (not deferred to NewProtocol).
func TestNewProtocol_RejectsEmptyVersionField(t *testing.T) {
	t.Parallel()
	_, err := cas.NewProtocol(
		cas.WithVersionField(""),
	)
	if err == nil {
		t.Fatal("expected error for empty version field name")
	}
	var coded *errcode.Error
	if !errors.As(err, &coded) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if coded.Code != errcode.ErrValidationFailed {
		t.Errorf("expected ErrValidationFailed, got %s", coded.Code)
	}
	if !strings.Contains(err.Error(), "non-empty") {
		t.Errorf("expected error to mention non-empty, got %q", err.Error())
	}
}

// TestNewProtocol_RejectsTypedNilConflictPolicy: typed-nil ConflictPolicy
// must be sticky-rejected — fail-closed sentinel pattern.
func TestNewProtocol_RejectsTypedNilConflictPolicy(t *testing.T) {
	t.Parallel()
	var nilPolicy *cas.ConflictPolicyStrictReject // typed nil
	_, err := cas.NewProtocol(
		cas.WithVersionField("version"),
		cas.WithConflictPolicy(nilPolicy),
	)
	if err == nil {
		t.Fatal("expected error for typed-nil ConflictPolicy")
	}
	if !strings.Contains(err.Error(), "typed-nil") {
		t.Errorf("expected error to mention typed-nil, got %q", err.Error())
	}
}

// TestNewProtocol_DefaultsToStrictReject: when WithConflictPolicy is omitted,
// Conflict() returns ConflictPolicyStrictReject{}.
func TestNewProtocol_DefaultsToStrictReject(t *testing.T) {
	t.Parallel()
	p, err := cas.NewProtocol(
		cas.WithVersionField("version"),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := p.Conflict().(cas.ConflictPolicyStrictReject); !ok {
		t.Errorf("expected ConflictPolicyStrictReject default, got %T", p.Conflict())
	}
}

// TestNewProtocol_SuccessWithBothOptions: normal path with explicit
// ConflictPolicy succeeds and returns correct values.
func TestNewProtocol_SuccessWithBothOptions(t *testing.T) {
	t.Parallel()
	p, err := cas.NewProtocol(
		cas.WithVersionField("password_version"),
		cas.WithConflictPolicy(cas.ConflictPolicyStrictReject{}),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil protocol")
	}
	if got := p.VersionField(); got != "password_version" {
		t.Errorf("VersionField = %q, want %q", got, "password_version")
	}
	if _, ok := p.Conflict().(cas.ConflictPolicyStrictReject); !ok {
		t.Errorf("expected ConflictPolicyStrictReject, got %T", p.Conflict())
	}
}

// TestMustNewProtocol_PanicsOnInvalid: missing WithVersionField triggers panic.
func TestMustNewProtocol_PanicsOnInvalid(t *testing.T) {
	t.Parallel()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic from MustNewProtocol when version field missing")
		}
		err, ok := r.(error)
		if !ok {
			t.Fatalf("expected panic value to be error, got %T: %v", r, r)
		}
		if !strings.Contains(err.Error(), "version field") {
			t.Errorf("expected panic error to mention version field, got %q", err.Error())
		}
	}()
	_ = cas.MustNewProtocol()
}

// TestMustNewProtocol_SuccessReturnsProtocol: normal path returns non-nil Protocol.
func TestMustNewProtocol_SuccessReturnsProtocol(t *testing.T) {
	t.Parallel()
	p := cas.MustNewProtocol(
		cas.WithVersionField("version"),
	)
	if p == nil {
		t.Fatal("expected non-nil protocol from MustNewProtocol")
	}
}

// TestProtocol_VersionFieldAccessor: VersionField returns the exact name supplied.
func TestProtocol_VersionFieldAccessor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		want string
	}{
		{"version", "version"},
		{"password_version", "password_version"},
		{"seq", "seq"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, err := cas.NewProtocol(cas.WithVersionField(tc.name))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := p.VersionField(); got != tc.want {
				t.Errorf("VersionField() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestCheckVersionMatch_OneRowSucceeds: rowsAffected=1 → nil error.
func TestCheckVersionMatch_OneRowSucceeds(t *testing.T) {
	t.Parallel()
	if err := cas.CheckVersionMatch(1, "user", "usr-001"); err != nil {
		t.Errorf("expected nil for rowsAffected=1, got %v", err)
	}
}

// TestCheckVersionMatch_ZeroRowsReturnsVersionConflict: rowsAffected=0 →
// ErrVersionConflict with entity and key in Details.
func TestCheckVersionMatch_ZeroRowsReturnsVersionConflict(t *testing.T) {
	t.Parallel()
	err := cas.CheckVersionMatch(0, "user", "usr-001")
	if err == nil {
		t.Fatal("expected error for rowsAffected=0")
	}
	var coded *errcode.Error
	if !errors.As(err, &coded) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if coded.Code != errcode.ErrVersionConflict {
		t.Errorf("expected ErrVersionConflict, got %s", coded.Code)
	}
	// Verify Details carry entity and key.
	entityAttr, ok := coded.FindAttr("entity")
	if !ok {
		t.Error("expected 'entity' detail to be present")
	} else if got := entityAttr.Value.String(); got != "user" {
		t.Errorf("entity detail = %q, want %q", got, "user")
	}
	keyAttr, ok := coded.FindAttr("key")
	if !ok {
		t.Error("expected 'key' detail to be present")
	} else if got := keyAttr.Value.String(); got != "usr-001" {
		t.Errorf("key detail = %q, want %q", got, "usr-001")
	}
}

// TestCheckVersionMatch_MultipleRowsReturnsVersionConflict: rowsAffected=2 →
// also returns ErrVersionConflict (WHERE clause matched unexpected rows).
func TestCheckVersionMatch_MultipleRowsReturnsVersionConflict(t *testing.T) {
	t.Parallel()
	err := cas.CheckVersionMatch(2, "config_entry", "cfg-42")
	if err == nil {
		t.Fatal("expected error for rowsAffected=2")
	}
	var coded *errcode.Error
	if !errors.As(err, &coded) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if coded.Code != errcode.ErrVersionConflict {
		t.Errorf("expected ErrVersionConflict for rowsAffected=2, got %s", coded.Code)
	}
}

// TestNewProtocol_NilOption_Ignored: a nil Option in the variadic list must be
// silently skipped (no panic).
func TestNewProtocol_NilOption_Ignored(t *testing.T) {
	t.Parallel()
	var nilOpt cas.Option // typed nil — avoids gocritic dupOption lint
	p, err := cas.NewProtocol(
		nilOpt,
		cas.WithVersionField("version"),
	)
	if err != nil {
		t.Fatalf("unexpected error with nil options: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil protocol")
	}
}

// TestNewProtocol_ConflictPolicyNilSticky: once typed-nil is observed, a
// subsequent valid WithConflictPolicy call does NOT clear the sentinel —
// NewProtocol still fails. Aligns with session.WithFingerprint sticky-sentinel
// pattern.
func TestNewProtocol_ConflictPolicyNilSticky(t *testing.T) {
	t.Parallel()
	var nilPolicy *cas.ConflictPolicyStrictReject // typed nil
	_, err := cas.NewProtocol(
		cas.WithVersionField("version"),
		cas.WithConflictPolicy(nilPolicy),
		cas.WithConflictPolicy(cas.ConflictPolicyStrictReject{}), // valid call after nil
	)
	if err == nil {
		t.Fatal("expected error: typed-nil sentinel must be sticky (caller misconfiguration must not be silently masked by a later valid call)")
	}
	if !strings.Contains(err.Error(), "typed-nil") {
		t.Errorf("expected error to mention typed-nil, got %q", err.Error())
	}
}
