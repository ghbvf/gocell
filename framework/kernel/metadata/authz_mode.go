package metadata

import (
	"fmt"
	"sort"
	"strings"
)

// authz_mode.go is the single oracle for the #2020 "default ABAC, explicit
// opt-out" rule: every active codegen HTTP route MUST declare an AuthZ mode.
// Both the cellgen generate-time completeness gate (the Hard carrier) and the
// governance FMT-42 authoring check (the Medium defense-in-depth layer) classify
// HTTP contracts through these helpers, so the two never diverge.
//
// AuthZ mode taxonomy (reuses the existing 5 HTTPAuthMeta flags + permission, no
// new flag — see auth_combo.go for the mutex matrix):
//
//   - ABAC (default)   → endpoints.http.permission: <action>   (contract-derived PDP gate)
//   - NoAuth           → auth.public                            (JWT-exempt)
//   - ServiceOnly      → auth.clientsOnly                       (caller-cell allowlist)
//   - OperatorOnly     → auth.bootstrap                         (AdminListener operator basic)
//   - serviceOwned     → auth.serviceOwned                      (JWT authn, service-validated ownership)
//
// passwordResetExempt is a MODIFIER, not a mode (it may ride alongside permission
// or serviceOwned), so it does NOT make a route "mode-declared".
//
// This file is the single oracle (classification helpers + frozen migration ledger)
// for invariant HTTP-AUTHZ-MODE-MANDATORY-01; the machine guard is the archtest
// tools/archtest/http_authz_mode_mandatory_test.go (ledger no-stale + frozen) plus
// the cellgen completeness gate (the Hard carrier).

// HTTPAuthModeDeclared reports whether an HTTP contract declares an AuthZ mode:
// the ABAC default (endpoints.http.permission set) OR one of the explicit opt-out
// modes (public / serviceOwned / bootstrap / clientsOnly). A contract that
// declares none is "modeless" — an undeclared standard route the #2020 gate
// rejects (unless it is on the frozen migration ledger).
func HTTPAuthModeDeclared(h *HTTPTransportMeta) bool {
	if h == nil {
		return false
	}
	return h.Permission != "" || HTTPAuthModeIsOptOut(h.Auth)
}

// HTTPAuthModeIsOptOut reports whether the auth flags select an explicit non-ABAC
// opt-out mode (NoAuth / ServiceOnly / OperatorOnly / serviceOwned). These modes
// replace or delegate the route-level PDP gate, so each MUST carry a reason
// (auth.reason) documenting why ABAC does not apply.
func HTTPAuthModeIsOptOut(a HTTPAuthMeta) bool {
	return a.Public || a.ServiceOwned || a.Bootstrap || a.ClientsOnly
}

// HTTPAuthModeViolation classifies a #2020 mode-rule violation for one contract, or
// HTTPAuthModeOK when the contract is compliant or out of scope.
type HTTPAuthModeViolation int

const (
	// HTTPAuthModeOK = compliant, or out of scope (non-http / non-active / non-codegen /
	// ledgered modeless).
	HTTPAuthModeOK HTTPAuthModeViolation = iota
	// HTTPAuthModeModeless = no permission and no opt-out flag (and not ledgered).
	HTTPAuthModeModeless
	// HTTPAuthModeOptOutMissingReason = an opt-out flag is set but auth.reason is empty.
	HTTPAuthModeOptOutMissingReason
	// HTTPAuthModeReasonWithoutOptOut = auth.reason is set on an ABAC/standard route.
	HTTPAuthModeReasonWithoutOptOut
)

// Message returns the human-readable problem statement for a violation (empty for OK).
func (v HTTPAuthModeViolation) Message() string {
	switch v {
	case HTTPAuthModeModeless:
		return "declares no AuthZ mode; every active route must declare endpoints.http.permission " +
			"(ABAC default) or an explicit opt-out (public/bootstrap/clientsOnly/serviceOwned) " +
			"(#2020 default-ABAC, strict fail-closed)"
	case HTTPAuthModeOptOutMissingReason:
		return "sets an opt-out auth mode (public/bootstrap/clientsOnly/serviceOwned) but omits " +
			"endpoints.http.auth.reason (#2020 non-ABAC must justify)"
	case HTTPAuthModeReasonWithoutOptOut:
		return "sets endpoints.http.auth.reason without an opt-out auth mode (#2020: reason justifies " +
			"a non-ABAC opt-out; ABAC/standard routes must omit it)"
	default:
		return ""
	}
}

// ValidateProjectHTTPAuthModes is the #2020 CLI-entry project-level layer: it runs
// ClassifyHTTPAuthMode over EVERY contract in the project and returns an aggregated error
// naming each active codegen HTTP contract that violates the mandatory-mode rule. CLI
// codegen/verify entry points call it so a single run reports every offender up front
// (better UX than the render core's first-fail). The UNBYPASSABLE Hard gate is
// contractgen.buildHTTPSpec, which runs ClassifyHTTPAuthMode on every contract it renders
// regardless of entry point (mirrors k8s apiextensions object-level validation); this
// project scan, the per-cell cellgen serve-scan, and governance FMT-42 are same-oracle
// defense / authoring-time layers.
func ValidateProjectHTTPAuthModes(p *ProjectMeta) error {
	if p == nil {
		return nil
	}
	ids := make([]string, 0, len(p.Contracts))
	for id := range p.Contracts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var bad []string
	for _, id := range ids {
		if v := ClassifyHTTPAuthMode(p.Contracts[id]); v != HTTPAuthModeOK {
			bad = append(bad, fmt.Sprintf("  - %s: %s", id, v.Message()))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("#2020 AuthZ mode gate: %d HTTP contract(s) violate the mandatory-mode rule "+
			"(declare endpoints.http.permission or an explicit opt-out + auth.reason, or add to the "+
			"frozen migration ledger):\n%s", len(bad), strings.Join(bad, "\n"))
	}
	return nil
}

// ClassifyHTTPAuthMode is the single oracle for the #2020 mandatory-AuthZ-mode rule,
// shared by contractgen (the comprehensive generate-time Hard gate over every active
// codegen HTTP contract), cellgen (per-cell serve-scan defense), and governance FMT-42
// (the validate-time Medium layer). It is scoped to active+codegen+http: a contract
// outside that scope, or a ledgered modeless contract, is HTTPAuthModeOK.
func ClassifyHTTPAuthMode(c *ContractMeta) HTTPAuthModeViolation {
	if c == nil || c.Kind != "http" || c.Lifecycle != "active" || !c.Codegen {
		return HTTPAuthModeOK
	}
	h := c.Endpoints.HTTP
	if !HTTPAuthModeDeclared(h) {
		if IsHTTPAuthModeLedgered(c.ID) {
			return HTTPAuthModeOK
		}
		return HTTPAuthModeModeless
	}
	optOut := h != nil && HTTPAuthModeIsOptOut(h.Auth)
	hasReason := h != nil && strings.TrimSpace(h.Auth.Reason) != ""
	switch {
	case optOut && !hasReason:
		return HTTPAuthModeOptOutMissingReason
	case hasReason && !optOut:
		return HTTPAuthModeReasonWithoutOptOut
	default:
		return HTTPAuthModeOK
	}
}

// httpAuthModeMigrationLedger is the FROZEN set of active codegen HTTP contract
// IDs that predate the #2020 mandatory-AuthZ-mode rule and have NOT yet declared
// a mode (they keep a hand-wired RequirePermission gate in their owner cell).
// They are EXEMPT from the completeness gate so the framework mechanism lands
// without blocking on migrating them; migration to endpoints.http.permission is
// tracked by #2355 / #2358.
//
// FROZEN: a NEW contract MUST NOT be added here — author a mode instead. The set
// only shrinks.
//
// NO-STALE: when a ledgered contract declares a mode (migrated) or is removed,
// its entry MUST be deleted. TestHTTPAuthModeLedger_MatchesProjectModeless
// (tools/archtest, loads the real project) fails CI on a stale entry, driving the
// set monotonically to empty. Once empty, delete this map together with the gate's
// ledger-exemption branch (#2020 endgame).
//
// INVARIANT: HTTP-AUTHZ-MODE-MANDATORY-01.
//
// Grouped by migration wave for incremental sign-off; the authoritative ownerCell is
// each contract's contract.yaml, not these headers. Keep alphabetically sorted within
// a group. Removing entries only shrinks the live set, which the immutable frozen set
// in TestHTTPAuthModeLedger_FrozenSubset already permits — no size constant to update.
var httpAuthModeMigrationLedger = map[string]struct{}{
	// Wave #2355 — accesscore user/role(list,check)/policy + decide migrated to
	// contract-derived authz (#2355: endpoints.http.{permission,resource,selfScoped}).
	// Survivors: admin/audit/internal + role.assign/revoke (internal clientsOnly,
	// migrate with their own opt-out mode in a later wave).
	"http.admin.health.cells.v1":  {},
	"http.audit.list.v1":          {},
	"http.auth.role.assign.v1":    {},
	"http.auth.role.revoke.v1":    {},
	"http.config.internal.get.v1": {},

	// Wave #2358 — devicecore / registrycore.
	"http.device.command.ack.v1":           {},
	"http.device.command.dequeue.v1":       {},
	"http.device.command.enqueue-async.v1": {},
	"http.device.command.enqueue.v1":       {},
	"http.device.command.extend-lease.v1":  {},
	"http.device.command.report.v1":        {},
	"http.device.list.v1":                  {},
	"http.device.status.v1":                {},
	"http.devicestate.v1":                  {},
	"http.registry.contract.list.v1":       {},
	"http.registry.contract.submit.v1":     {},

	// examples/ (todoorder) — migrate to stay exemplary for external Cell authors.
	"http.order.confirm.v1":            {},
	"http.order.create.v1":             {},
	"http.order.get.v1":                {},
	"http.order.list.v1":               {},
	"http.order.projection-summary.v1": {},
}

// FROZEN guard: the ledger may only SHRINK within its #2020-landing ID set, never grow
// or swap. TestHTTPAuthModeLedger_FrozenSubset pins the initial 37-ID set and asserts
// the live ledger stays a subset — so neither "add a new modeless route AND ledger it"
// (grow) nor "migrate one out + ledger a different new one" (swap) can pass, both of
// which a size-only cap would miss. Migrating a route just removes its entry here; the
// frozen set in the test is immutable.

// IsHTTPAuthModeLedgered reports whether contractID is on the frozen #2020
// migration ledger (and therefore temporarily exempt from the mandatory-mode
// completeness gate).
func IsHTTPAuthModeLedgered(contractID string) bool {
	_, ok := httpAuthModeMigrationLedger[contractID]
	return ok
}

// HTTPAuthModeLedgerIDs returns the ledger contract IDs sorted, for the no-stale
// guard and diagnostics.
func HTTPAuthModeLedgerIDs() []string {
	out := make([]string, 0, len(httpAuthModeMigrationLedger))
	for id := range httpAuthModeMigrationLedger {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
