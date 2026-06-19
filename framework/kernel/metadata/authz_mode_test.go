package metadata

import (
	"strings"
	"testing"
)

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

// frozenInitialLedgerIDs is the IMMUTABLE #2020-landing ledger ID set (37). Never edit
// it — migrating a route removes its entry from httpAuthModeMigrationLedger only; this
// set stays put so TestHTTPAuthModeLedger_FrozenSubset can prove the live ledger never
// grows or swaps in a new modeless ID.
var frozenInitialLedgerIDs = map[string]struct{}{
	"http.admin.health.cells.v1": {}, "http.audit.list.v1": {}, "http.auth.decide.v1": {},
	"http.auth.role.assign.v1": {}, "http.auth.role.check.v1": {}, "http.auth.role.list.v1": {},
	"http.auth.role.revoke.v1": {}, "http.auth.user.change-password.v1": {}, "http.auth.user.create.v1": {},
	"http.auth.user.delete.v1": {}, "http.auth.user.get.v1": {}, "http.auth.user.lock.v1": {},
	"http.auth.user.patch.v1": {}, "http.auth.user.unlock.v1": {}, "http.auth.user.update.v1": {},
	"http.config.internal.get.v1": {}, "http.device.command.ack.v1": {}, "http.device.command.dequeue.v1": {},
	"http.device.command.enqueue-async.v1": {}, "http.device.command.enqueue.v1": {}, "http.device.command.extend-lease.v1": {},
	"http.device.command.report.v1": {}, "http.device.list.v1": {}, "http.device.status.v1": {},
	"http.devicestate.v1": {}, "http.order.confirm.v1": {}, "http.order.create.v1": {},
	"http.order.get.v1": {}, "http.order.list.v1": {}, "http.order.projection-summary.v1": {},
	"http.policy.create.v1": {}, "http.policy.delete.v1": {}, "http.policy.get.v1": {},
	"http.policy.list.v1": {}, "http.policy.update.v1": {}, "http.registry.contract.list.v1": {},
	"http.registry.contract.submit.v1": {},
}

// TestHTTPAuthModeLedger_FrozenSubset guards the frozen ID set: the live ledger may only
// shrink WITHIN frozenInitialLedgerIDs. An ID not in the frozen set means a new modeless
// route was ledgered (grow) or swapped in (remove-one-add-one stays same size but a
// size-only cap would miss it) — both bypass the codegen Hard gate.
func TestHTTPAuthModeLedger_FrozenSubset(t *testing.T) {
	for _, id := range HTTPAuthModeLedgerIDs() {
		if _, ok := frozenInitialLedgerIDs[id]; !ok {
			t.Errorf("ledger entry %q is not in the frozen #2020-landing set — a new modeless route "+
				"must declare a mode, not be ledgered (the frozen set is immutable; the ledger may "+
				"only shrink)", id)
		}
	}
}

// TestValidateProjectHTTPAuthModes covers the comprehensive project-level gate shared by
// every codegen/verify entry point: it must flag any active codegen HTTP contract that
// violates the rule — including contracts with no cell/slice (the gap a serve-scan alone
// misses) — and aggregate all offenders in one run.
func TestValidateProjectHTTPAuthModes(t *testing.T) {
	mk := func(id string, h *HTTPTransportMeta) *ContractMeta {
		return &ContractMeta{ID: id, Kind: "http", Lifecycle: "active", Codegen: true, Endpoints: EndpointsMeta{HTTP: h}}
	}
	t.Run("nil project ok", func(t *testing.T) {
		if err := ValidateProjectHTTPAuthModes(nil); err != nil {
			t.Fatalf("nil project must pass, got: %v", err)
		}
	})
	t.Run("clean project passes", func(t *testing.T) {
		p := &ProjectMeta{Contracts: map[string]*ContractMeta{
			"http.ok.v1": mk("http.ok.v1", &HTTPTransportMeta{Permission: "config:read"}),
		}}
		if err := ValidateProjectHTTPAuthModes(p); err != nil {
			t.Fatalf("clean project must pass, got: %v", err)
		}
	})
	t.Run("modeless contract fails even with no cell/slice", func(t *testing.T) {
		p := &ProjectMeta{Contracts: map[string]*ContractMeta{
			"http.bad.v1": mk("http.bad.v1", &HTTPTransportMeta{}),
		}}
		err := ValidateProjectHTTPAuthModes(p)
		if err == nil || !strings.Contains(err.Error(), "http.bad.v1") {
			t.Fatalf("modeless contract must fail naming the offender, got: %v", err)
		}
	})
	t.Run("aggregates every violation", func(t *testing.T) {
		p := &ProjectMeta{Contracts: map[string]*ContractMeta{
			"http.a.v1": mk("http.a.v1", &HTTPTransportMeta{}),
			"http.b.v1": mk("http.b.v1", &HTTPTransportMeta{Auth: HTTPAuthMeta{Public: true}}),
		}}
		err := ValidateProjectHTTPAuthModes(p)
		if err == nil || !strings.Contains(err.Error(), "http.a.v1") || !strings.Contains(err.Error(), "http.b.v1") {
			t.Fatalf("must aggregate both offenders, got: %v", err)
		}
	})
}

// TestClassifyHTTPAuthMode covers the shared oracle consumed by contractgen, cellgen,
// and FMT-42: scope gating + the three violation kinds + ledger exemption.
func TestClassifyHTTPAuthMode(t *testing.T) {
	httpC := func(lifecycle string, codegen bool, h *HTTPTransportMeta) *ContractMeta {
		return &ContractMeta{ID: "http.demo.x.v1", Kind: "http", Lifecycle: lifecycle, Codegen: codegen, Endpoints: EndpointsMeta{HTTP: h}}
	}
	cases := []struct {
		name string
		c    *ContractMeta
		want HTTPAuthModeViolation
	}{
		{"nil", nil, HTTPAuthModeOK},
		{"non-http", &ContractMeta{ID: "event.x.v1", Kind: "event", Lifecycle: "active", Codegen: true}, HTTPAuthModeOK},
		{"draft out of scope", httpC("draft", true, &HTTPTransportMeta{}), HTTPAuthModeOK},
		{"non-codegen out of scope", httpC("active", false, &HTTPTransportMeta{}), HTTPAuthModeOK},
		{"modeless", httpC("active", true, &HTTPTransportMeta{}), HTTPAuthModeModeless},
		{
			"passwordResetExempt-only modeless",
			httpC("active", true, &HTTPTransportMeta{Auth: HTTPAuthMeta{PasswordResetExempt: true}}), HTTPAuthModeModeless,
		},
		{"abac ok", httpC("active", true, &HTTPTransportMeta{Permission: "config:read"}), HTTPAuthModeOK},
		{
			"opt-out with reason ok",
			httpC("active", true, &HTTPTransportMeta{Auth: HTTPAuthMeta{Public: true, Reason: "login"}}), HTTPAuthModeOK,
		},
		{
			"opt-out missing reason",
			httpC("active", true, &HTTPTransportMeta{Auth: HTTPAuthMeta{Public: true}}), HTTPAuthModeOptOutMissingReason,
		},
		{
			"reason without opt-out",
			httpC("active", true, &HTTPTransportMeta{Permission: "config:read", Auth: HTTPAuthMeta{Reason: "stray"}}),
			HTTPAuthModeReasonWithoutOptOut,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyHTTPAuthMode(tc.c); got != tc.want {
				t.Errorf("ClassifyHTTPAuthMode = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("ledgered modeless is OK", func(t *testing.T) {
		ids := HTTPAuthModeLedgerIDs()
		if len(ids) == 0 {
			t.Skip("ledger drained")
		}
		c := httpC("active", true, &HTTPTransportMeta{})
		c.ID = ids[0]
		if got := ClassifyHTTPAuthMode(c); got != HTTPAuthModeOK {
			t.Errorf("ledgered modeless contract = %v, want OK", got)
		}
	})
}
