package metadata

import "testing"

// TestHTTPAuthModeDeclared covers the #2020 mode-classification oracle: a route is
// "mode-declared" iff it sets endpoints.http.permission (ABAC) or an opt-out flag.
// passwordResetExempt is a modifier, not a mode.
func TestHTTPAuthModeDeclared(t *testing.T) {
	cases := []struct {
		name string
		h    *HTTPTransportMeta
		want bool
	}{
		{"nil", nil, false},
		{"modeless empty", &HTTPTransportMeta{}, false},
		{"passwordResetExempt only is modeless", &HTTPTransportMeta{Auth: HTTPAuthMeta{PasswordResetExempt: true}}, false},
		{"permission (ABAC)", &HTTPTransportMeta{Permission: "config:read"}, true},
		{"public", &HTTPTransportMeta{Auth: HTTPAuthMeta{Public: true}}, true},
		{"serviceOwned", &HTTPTransportMeta{Auth: HTTPAuthMeta{ServiceOwned: true}}, true},
		{"bootstrap", &HTTPTransportMeta{Auth: HTTPAuthMeta{Bootstrap: true}}, true},
		{"clientsOnly", &HTTPTransportMeta{Auth: HTTPAuthMeta{ClientsOnly: true}}, true},
		{"serviceOwned + passwordResetExempt", &HTTPTransportMeta{Auth: HTTPAuthMeta{ServiceOwned: true, PasswordResetExempt: true}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HTTPAuthModeDeclared(tc.h); got != tc.want {
				t.Errorf("HTTPAuthModeDeclared(%+v) = %v, want %v", tc.h, got, tc.want)
			}
		})
	}
}

// TestHTTPAuthModeIsOptOut covers the opt-out predicate that drives the reason
// requirement (the 4 non-ABAC modes need a justification; passwordResetExempt does not).
func TestHTTPAuthModeIsOptOut(t *testing.T) {
	cases := []struct {
		name string
		a    HTTPAuthMeta
		want bool
	}{
		{"none", HTTPAuthMeta{}, false},
		{"passwordResetExempt is not an opt-out", HTTPAuthMeta{PasswordResetExempt: true}, false},
		{"public", HTTPAuthMeta{Public: true}, true},
		{"serviceOwned", HTTPAuthMeta{ServiceOwned: true}, true},
		{"bootstrap", HTTPAuthMeta{Bootstrap: true}, true},
		{"clientsOnly", HTTPAuthMeta{ClientsOnly: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HTTPAuthModeIsOptOut(tc.a); got != tc.want {
				t.Errorf("HTTPAuthModeIsOptOut(%+v) = %v, want %v", tc.a, got, tc.want)
			}
		})
	}
}

// TestHTTPAuthModeLedger_Accessors checks the ledger accessors: membership is
// consistent with the sorted ID list, and unknown IDs are not ledgered. The
// real-project no-stale + frozen guard lives in the governance package (it needs
// to load the repo); this only covers the in-package accessor contract.
func TestHTTPAuthModeLedger_Accessors(t *testing.T) {
	ids := HTTPAuthModeLedgerIDs()
	for i := 1; i < len(ids); i++ {
		if ids[i-1] >= ids[i] {
			t.Errorf("ledger IDs not strictly sorted/unique at %d: %q >= %q", i, ids[i-1], ids[i])
		}
	}
	for _, id := range ids {
		if !IsHTTPAuthModeLedgered(id) {
			t.Errorf("IsHTTPAuthModeLedgered(%q) = false, want true (listed by HTTPAuthModeLedgerIDs)", id)
		}
	}
	if IsHTTPAuthModeLedgered("http.not.a.real.ledger.id.v1") {
		t.Error("IsHTTPAuthModeLedgered: unknown id must not be ledgered")
	}
}

// TestHTTPAuthModeLedger_FrozenSize guards the frozen ceiling: the ledger may only
// shrink, never grow. A growing ledger means a new modeless route was added AND
// ledgered, bypassing the cellgen Hard gate — the size cap catches that.
func TestHTTPAuthModeLedger_FrozenSize(t *testing.T) {
	if got := len(httpAuthModeMigrationLedger); got > httpAuthModeLedgerFrozenSize {
		t.Fatalf("ledger grew to %d entries (> frozen %d) — a new modeless route must declare "+
			"a mode, not be ledgered (#2020 frozen). If you migrated/removed entries, decrement "+
			"httpAuthModeLedgerFrozenSize to match", got, httpAuthModeLedgerFrozenSize)
	}
}
