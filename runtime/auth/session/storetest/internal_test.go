package storetest

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/runtime/auth/session"
)

// Internal package test: exercises the unexported validateFixtureInputs
// branches that NewSessionFixture's t.Fatal wrapper makes inaccessible from
// outside this package. Keeps Sonar's per-package new-code coverage gate
// happy without forking testing.TB.

func TestValidateFixtureInputs_OK(t *testing.T) {
	t.Parallel()
	if err := validateFixtureInputs("sub", "jti", time.Hour); err != nil {
		t.Errorf("expected nil error for valid inputs, got %v", err)
	}
}

func TestValidateFixtureInputs_Errors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		subjectID string
		jti       string
		ttl       time.Duration
		wantSubs  string
	}{
		{"empty subject", "", "jti", time.Hour, "subjectID"},
		{"empty jti", "sub", "", time.Hour, "jti"},
		{"zero ttl", "sub", "jti", 0, "ttl"},
		{"negative ttl", "sub", "jti", -time.Second, "ttl"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			err := validateFixtureInputs(c.subjectID, c.jti, c.ttl)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), c.wantSubs) {
				t.Errorf("expected error to mention %q, got %q", c.wantSubs, err.Error())
			}
		})
	}
}

// TestAuditFingerprintJTIShape_Clean — current Session struct produces no
// problems (regression guard against accidental plaintext-token field
// additions; complements runFingerprintJTINoPlaintext which only fires when
// the suite drives it via Run).
func TestAuditFingerprintJTIShape_Clean(t *testing.T) {
	t.Parallel()
	got := auditFingerprintJTIShape(reflect.TypeOf(session.Session{}))
	if len(got) != 0 {
		t.Errorf("expected zero problems on canonical Session, got %v", got)
	}
}

// TestAuditFingerprintJTIShape_Violations — synthetic struct types exercise
// each failure branch the audit reports. Forbidden names, missing JTI, and
// non-string JTI are checked.
func TestAuditFingerprintJTIShape_Violations(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		shape    any
		wantSubs string
	}{
		{
			name:     "AccessToken field",
			shape:    struct{ AccessToken string }{},
			wantSubs: "AccessToken",
		},
		{
			name:     "Password field",
			shape:    struct{ Password string }{},
			wantSubs: "Password",
		},
		{
			name:     "missing JTI",
			shape:    struct{ Other string }{},
			wantSubs: "JTI field missing",
		},
		{
			name:     "non-string JTI",
			shape:    struct{ JTI int }{},
			wantSubs: "must be string",
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			problems := auditFingerprintJTIShape(reflect.TypeOf(c.shape))
			if len(problems) == 0 {
				t.Fatalf("expected at least one problem, got none")
			}
			joined := strings.Join(problems, "\n")
			if !strings.Contains(joined, c.wantSubs) {
				t.Errorf("expected problem to mention %q, got %q", c.wantSubs, joined)
			}
		})
	}
}
