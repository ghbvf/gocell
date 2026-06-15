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
	if len(reasons) != 10 {
		t.Fatalf("expected 10 RFC 5280 reasons, got %d", len(reasons))
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
		cs.ReasonCertificateHold().String():      "certificateHold",
		cs.ReasonRemoveFromCRL().String():        "removeFromCRL",
		cs.ReasonPrivilegeWithdrawn().String():   "privilegeWithdrawn",
		cs.ReasonAACompromise().String():         "aACompromise",
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
	issuerB, _ := cs.NewIssuerID("ca-other")
	dev, _ := cs.NewDeviceID("device-1")
	devB, _ := cs.NewDeviceID("device-2")
	scopeA, _ := cs.NewCertScope(mustTenant(t, testTenant), issuer, dev)
	serial, _ := cs.NewSerial("1a2b3c")

	if err := store.Revoke(ctx, scopeA, serial, cs.ReasonKeyCompromise()); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	// Visible within scope A.
	listA, err := store.RevocationList(ctx, scopeA)
	if err != nil || len(listA) != 1 || listA[0].Serial != serial {
		t.Fatalf("scope A list: %v %+v", err, listA)
	}

	// FR-007: the serial is invisible across EVERY isolation dimension — a query
	// under a different tenant, issuer, or device must not surface it. A bare
	// serial never crosses an isolation domain.
	crossScopes := map[string]cs.CertScope{
		"cross-tenant": mustScopeFrom(t, testTenantB, issuer, dev),
		"cross-issuer": mustScopeFrom(t, testTenant, issuerB, dev),
		"cross-device": mustScopeFrom(t, testTenant, issuer, devB),
	}
	for name, sc := range crossScopes {
		list, err := store.RevocationList(ctx, sc)
		if err != nil {
			t.Fatalf("%s list: %v", name, err)
		}
		if len(list) != 0 {
			t.Errorf("%s query leaked %d entries; serial must be invisible across scope", name, len(list))
		}
	}

	// Revoke under a different scope must not touch scope A's entry.
	if err := store.Revoke(ctx, crossScopes["cross-issuer"], serial, cs.ReasonSuperseded()); err != nil {
		t.Fatalf("cross-issuer revoke: %v", err)
	}
	if listA, _ = store.RevocationList(ctx, scopeA); len(listA) != 1 || listA[0].Reason != cs.ReasonKeyCompromise() {
		t.Error("cross-issuer Revoke must not mutate scope A's entry")
	}

	// Tidy is scope-bounded.
	if err := store.Tidy(ctx, crossScopes["cross-tenant"], time.Now()); err != nil {
		t.Fatalf("tidy cross-tenant: %v", err)
	}
	if listA, _ = store.RevocationList(ctx, scopeA); len(listA) != 1 {
		t.Error("tidy of another scope must not affect scope A")
	}
}

// mustScopeFrom builds a CertScope from explicit dimensions for isolation tests.
func mustScopeFrom(t *testing.T, tenantID string, issuer cs.IssuerID, dev cs.DeviceID) cs.CertScope {
	t.Helper()
	sc, err := cs.NewCertScope(mustTenant(t, tenantID), issuer, dev)
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	return sc
}
