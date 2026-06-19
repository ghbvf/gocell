package metadata

import "sort"

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
// INVARIANT: HTTP-AUTHZ-MODE-MANDATORY-01.

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
// its entry MUST be deleted. TestHTTPAuthModeLedger_NoStale (governance package,
// loads the real project) fails CI on a stale entry, driving the set monotonically
// to empty. Once empty, delete this map together with the gate's ledger-exemption
// branch (#2020 endgame).
//
// INVARIANT: HTTP-AUTHZ-MODE-MANDATORY-01.
var httpAuthModeMigrationLedger = map[string]struct{}{
	// Seeded from the cellgen completeness gate over the real project; see
	// TestHTTPAuthModeLedger_NoStale. Keep alphabetically sorted. Each entry is a
	// route with a hand-wired RequirePermission gate (or a yet-to-be-classified
	// internal/admin route) that has not yet declared its mode in contract.yaml.
	// Platform corecells (accesscore/devicecore/registrycore/auditcore) migrate to
	// endpoints.http.permission via #2355 / #2358; the examples/ subset (http.order.*)
	// migrates separately to stay exemplary for external Cell authors.
	"http.admin.health.cells.v1":           {},
	"http.audit.list.v1":                   {},
	"http.auth.decide.v1":                  {},
	"http.auth.role.assign.v1":             {},
	"http.auth.role.check.v1":              {},
	"http.auth.role.list.v1":               {},
	"http.auth.role.revoke.v1":             {},
	"http.auth.user.change-password.v1":    {},
	"http.auth.user.create.v1":             {},
	"http.auth.user.delete.v1":             {},
	"http.auth.user.get.v1":                {},
	"http.auth.user.lock.v1":               {},
	"http.auth.user.patch.v1":              {},
	"http.auth.user.unlock.v1":             {},
	"http.auth.user.update.v1":             {},
	"http.config.internal.get.v1":          {},
	"http.device.command.ack.v1":           {},
	"http.device.command.dequeue.v1":       {},
	"http.device.command.enqueue-async.v1": {},
	"http.device.command.enqueue.v1":       {},
	"http.device.command.extend-lease.v1":  {},
	"http.device.command.report.v1":        {},
	"http.device.list.v1":                  {},
	"http.device.status.v1":                {},
	"http.devicestate.v1":                  {},
	"http.order.confirm.v1":                {},
	"http.order.create.v1":                 {},
	"http.order.get.v1":                    {},
	"http.order.list.v1":                   {},
	"http.order.projection-summary.v1":     {},
	"http.policy.create.v1":                {},
	"http.policy.delete.v1":                {},
	"http.policy.get.v1":                   {},
	"http.policy.list.v1":                  {},
	"http.policy.update.v1":                {},
	"http.registry.contract.list.v1":       {},
	"http.registry.contract.submit.v1":     {},
}

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
