package redis

import (
	"strings"
	"testing"
)

func TestKeyNamespace_Validate(t *testing.T) {
	tests := []struct {
		name    string
		ns      KeyNamespace
		wantErr bool
		errSub  string
	}{
		{name: "empty rejected", ns: "", wantErr: true, errSub: "must not be empty"},
		{name: "valid lowercase", ns: "accesscore", wantErr: false},
		{name: "valid sentinel underscore prefix", ns: "_runtime", wantErr: false},
		{name: "valid digits", ns: "cell0", wantErr: false},
		{name: "valid mixed letters digits underscore", ns: "audit_v2", wantErr: false},
		{name: "valid role with dash", ns: "servicetoken-nonce", wantErr: false},
		{name: "uppercase rejected", ns: "AccessCore", wantErr: true, errSub: "lowercase"},
		{name: "colon rejected", ns: "ns:sub", wantErr: true, errSub: "colon"},
		{name: "open brace rejected", ns: "ns{x", wantErr: true, errSub: "brace"},
		{name: "close brace rejected", ns: "ns}x", wantErr: true, errSub: "brace"},
		{name: "dot rejected", ns: "access.core", wantErr: true},
		{name: "slash rejected", ns: "access/core", wantErr: true},
		{name: "leading digit rejected", ns: "1cell", wantErr: true},
		{name: "leading dash rejected", ns: "-cell", wantErr: true},
		{name: "whitespace rejected", ns: "ac core", wantErr: true},
		{name: "max length 48 ok", ns: KeyNamespace(strings.Repeat("a", 48)), wantErr: false},
		{name: "length 49 rejected", ns: KeyNamespace(strings.Repeat("a", 49)), wantErr: true, errSub: "length"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertNamespaceValidation(t, tt.ns, tt.wantErr, tt.errSub)
		})
	}
}

// assertNamespaceValidation checks the outcome of Validate against the
// table case's expectations. Extracted from the test loop so the loop
// body stays under SonarCloud's 15-cognitive-complexity threshold.
func assertNamespaceValidation(t *testing.T, ns KeyNamespace, wantErr bool, errSub string) {
	t.Helper()
	err := ns.Validate()
	if !wantErr {
		if err != nil {
			t.Fatalf("unexpected error for ns=%q: %v", string(ns), err)
		}
		return
	}
	if err == nil {
		t.Fatalf("expected error for ns=%q, got nil", string(ns))
	}
	if errSub != "" && !strings.Contains(err.Error(), errSub) {
		t.Fatalf("expected error containing %q, got %v", errSub, err)
	}
}

// keyns_apply_test verifies the unexported helpers used by the four
// constructors. Kept in the same package so unexported helpers are visible.
func TestKeyNamespace_apply(t *testing.T) {
	ns := KeyNamespace("audit_v2")
	if got, want := ns.apply("user:42"), "audit_v2:user:42"; got != want {
		t.Fatalf("apply(user:42) = %q, want %q", got, want)
	}
}

func TestKeyNamespace_applyHashtag(t *testing.T) {
	ns := KeyNamespace("_runtime")
	got := ns.applyHashtag("entry-uuid", "lease")
	want := "_runtime:{entry-uuid}:lease"
	if got != want {
		t.Fatalf("applyHashtag = %q, want %q", got, want)
	}
}
