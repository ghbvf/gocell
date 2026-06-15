package certsigning_test

import (
	"context"
	"testing"
	"time"

	cs "github.com/ghbvf/gocell/framework/runtime/certsigning"
)

func TestRevocationReasonClosedSet(t *testing.T) {
	t.Parallel()

	// Zero value is invalid and renders as the fail-closed sentinel.
	var zero cs.RevocationReason
	if !zero.IsZero() || zero.String() != "unknown" {
		t.Errorf("zero reason: isZero=%v str=%q", zero.IsZero(), zero.String())
	}

	reasons := cs.RevocationReasons()
	if len(reasons) != 6 {
		t.Fatalf("expected 6 RFC 5280 reasons, got %d", len(reasons))
	}
	// Every registered reason is non-zero and stringifies to its mnemonic.
	for _, r := range reasons {
		if r.IsZero() || r.String() == "unknown" {
			t.Errorf("registered reason %q must be non-zero", r.String())
		}
	}
	// Each accessor returns its RFC 5280 mnemonic.
	for got, want := range map[string]string{
		cs.ReasonUnspecified().String():          "unspecified",
		cs.ReasonKeyCompromise().String():        "keyCompromise",
		cs.ReasonCACompromise().String():         "cACompromise",
		cs.ReasonAffiliationChanged().String():   "affiliationChanged",
		cs.ReasonSuperseded().String():           "superseded",
		cs.ReasonCessationOfOperation().String(): "cessationOfOperation",
	} {
		if got != want {
			t.Errorf("reason mnemonic = %q, want %q", got, want)
		}
	}

	// RevocationReasons returns a copy (mutating it must not affect the registry).
	reasons[0] = cs.RevocationReason{}
	if cs.RevocationReasons()[0].IsZero() {
		t.Error("RevocationReasons must return a defensive copy")
	}
}

// memRevocationStore is a TEST-ONLY fake demonstrating that the RevocationStore
// interface shape supports CertScope isolation: a revoked serial is recorded
// AND keyed by scope, and a query/revoke under a different scope cannot see it.
// The production fail-closed cross-scope behavior (acceptance scenario 7) lands
// with the softca / PG implementation in PR-6 (#1902); this fake proves the seam
// can express it (绝不凭裸 serial 跨隔离域).
type memRevocationStore struct {
	byScope map[string]map[string]cs.RevokedCertificate
}

func newMemRevocationStore() *memRevocationStore {
	return &memRevocationStore{byScope: map[string]map[string]cs.RevokedCertificate{}}
}

func scopeKey(s cs.CertScope) string {
	return s.Tenant().String() + "|" + s.Issuer().String() + "|" + s.Device().String()
}

func (m *memRevocationStore) Revoke(_ context.Context, scope cs.CertScope, serial cs.Serial, reason cs.RevocationReason) error {
	k := scopeKey(scope)
	if m.byScope[k] == nil {
		m.byScope[k] = map[string]cs.RevokedCertificate{}
	}
	m.byScope[k][serial.String()] = cs.RevokedCertificate{
		Serial: serial, Reason: reason, RevokedAt: time.Unix(0, 0).UTC(),
	}
	return nil
}

func (m *memRevocationStore) RevocationList(_ context.Context, scope cs.CertScope) ([]cs.RevokedCertificate, error) {
	var out []cs.RevokedCertificate
	for _, rc := range m.byScope[scopeKey(scope)] {
		out = append(out, rc)
	}
	return out, nil
}

func (m *memRevocationStore) Tidy(_ context.Context, scope cs.CertScope, _ time.Time) error {
	delete(m.byScope, scopeKey(scope))
	return nil
}

func TestRevocationStoreScopeIsolation(t *testing.T) {
	t.Parallel()
	var store cs.RevocationStore = newMemRevocationStore()
	ctx := context.Background()

	issuer, _ := cs.NewIssuerID("ca-root")
	dev, _ := cs.NewDeviceID("device-1")
	scopeA, _ := cs.NewCertScope(mustTenant(t, testTenant), issuer, dev)
	scopeB, _ := cs.NewCertScope(mustTenant(t, testTenantB), issuer, dev)
	serial, _ := cs.NewSerial("1a2b3c")

	if err := store.Revoke(ctx, scopeA, serial, cs.ReasonKeyCompromise()); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	// Visible within scope A.
	listA, err := store.RevocationList(ctx, scopeA)
	if err != nil || len(listA) != 1 || listA[0].Serial != serial {
		t.Fatalf("scope A list: %v %+v", err, listA)
	}

	// Invisible across the tenant boundary (fail-closed by scope key) — a
	// tenant-B principal cannot see tenant-A's serial.
	listB, err := store.RevocationList(ctx, scopeB)
	if err != nil {
		t.Fatalf("scope B list: %v", err)
	}
	if len(listB) != 0 {
		t.Errorf("cross-tenant query leaked %d entries; serial must be invisible across scope", len(listB))
	}

	// Tidy is scope-bounded.
	if err := store.Tidy(ctx, scopeB, time.Now()); err != nil {
		t.Fatalf("tidy scope B: %v", err)
	}
	if listA, _ = store.RevocationList(ctx, scopeA); len(listA) != 1 {
		t.Error("tidy of scope B must not affect scope A")
	}
}
