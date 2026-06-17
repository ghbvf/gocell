package authz

// Permission is a sealed, closed-set authorization action identifier.
//
// A Permission names WHAT a request wants to do (e.g. "audit:read"), decoupled
// from WHO may do it (a role). It is the value a route declares via
// auth.RequirePermission(...) and the value carried as the `action` into a
// PDP (auth.Authorizer.Authorize). Keeping it a distinct type — not a bare
// string — is the point: a role-name string literal (e.g. "admin") cannot be
// passed where a Permission is expected, so the #914 migration cannot regress
// into a role-literal gate by accident.
//
// # Sealed construction (closed value set)
//
// The single field `s` is unexported and the only minter is the unexported
// newPermission, called solely from the package-level var declarations below.
// Outside this package there is NO way to construct a Permission:
// authz.Permission{s: "anything"} is a compile error (unexported field), and
// there is no exported constructor or Unmarshal. The set of Permissions that
// can ever exist is therefore exactly the exported Perm* vars in this file — a
// closed, audited registry. The zero value (Permission{}) is invalid; IsZero()
// reports it and downstream consumers fail closed on it.
//
// # AI-robust Grade
//
// Hard — "string-typed concept funnel" + "sealed construction" from the
// ai-robust.md Hard 范本目录. Upstream: newPermission is the sole minter,
// reachable only through this file's registry. Downstream: auth.RequirePermission
// is the sole route consumer. A new permission cannot be introduced without
// adding a perm* var + accessor here (which the registry test pins), so the
// action vocabulary cannot drift via scattered string literals.
//
// Permissions are exposed as ACCESSOR FUNCTIONS (e.g. PermAuditRead()), never as
// exported vars. An exported var would be reassignable from outside
// (authz.PermAuditRead = Permission{} could zero the global registry value — the
// F6 gap); a function declaration cannot be reassigned, so the registry value is
// immutable to every external package. This makes the "sealed value" claim true
// by the type system (compile error to reassign), not merely by convention.
//
// Growth: each PR in the #1348/#1894 migration series adds one perm* var +
// accessor per resource:action class — PR-10a (audit:read), PR-10b (5 config/flag
// perms), PR-10c (5 accesscore perms), PR-10d (8 iotdevice + todoorder perms).
// The accessor-function-over-private-singleton shape (not exported var) ensures
// the registry value is immutable to external packages: reassigning a func is a
// compile error, so the "sealed value" claim is enforced by the type system, not
// convention. The closed set grows only here — a new permission cannot be
// introduced without adding a perm* var + accessor in this file (which the
// registry test pins via allPermissions).
type Permission struct {
	s string
}

// newPermission is the sole minter of Permission values. It is unexported and
// must be called only from the package-level perm* var declarations in this
// file, which is what makes the exported set closed and audited.
func newPermission(s string) Permission {
	return Permission{s: s}
}

// permAuditRead is the package-private singleton backing the PermAuditRead()
// accessor. Unexported so no external package can reassign it.
var permAuditRead = newPermission("audit:read")

// PermAuditRead returns the permission authorizing reading the audit ledger
// across actors within the caller's tenant (the gate auditquery's "query other
// actors" branch consults). Migrated from the role-literal
// auth.AnyRole(RoleAdmin, RoleSuperAdmin) gate in #914 (PR-10a).
//
// It is an accessor function (not an exported var) so the registry value is
// immutable to external packages — reassigning a func is a compile error.
func PermAuditRead() Permission {
	return permAuditRead
}

// permSystemRead is the package-private singleton backing the PermSystemRead()
// accessor. Unexported so no external package can reassign it.
var permSystemRead = newPermission("system:read")

// PermSystemRead returns the permission authorizing reads of runtime/system
// observability state — the gate for the aggregated cell-health endpoint
// http.admin.health.cells.v1 (#1860). It is a first-class, enumerable action
// distinct from audit:read: observability access is not audit-ledger access.
// The PDP baseline grants it to admin / super-admin (see authorizationdecide
// baseline.go), mirroring the prior role gate without a role-literal in code.
//
// It is an accessor function (not an exported var) so the registry value is
// immutable to external packages — reassigning a func is a compile error.
func PermSystemRead() Permission {
	return permSystemRead
}

// configcore permissions (PR-10b #1348). Each mirrors the role-literal
// auth.AnyRole(RoleAdmin) gate it replaces, one resource:action per slice's
// shared route policy. Naming follows the resource:action convention of the
// audit:read seed (ref: AWS-IAM service:Action, Spring-Security
// hasAuthority("resource:action")). Same accessor-func-over-private-singleton
// shape as PermAuditRead — reassignment is a compile error (Hard immutability).
var (
	permConfigRead    = newPermission("config:read")
	permConfigWrite   = newPermission("config:write")
	permConfigPublish = newPermission("config:publish")
	permFlagRead      = newPermission("flag:read")
	permFlagWrite     = newPermission("flag:write")
)

// PermConfigRead authorizes reading configuration entries (configread slice:
// GET config/{key}, GET config). Migrated from auth.AnyRole(RoleAdmin) in PR-10b.
func PermConfigRead() Permission { return permConfigRead }

// PermConfigWrite authorizes mutating configuration entries (configwrite slice:
// create/update/delete). "write" folds create+update+delete per the AWS-IAM
// Write access-level grouping the prior uniform admin gate already implied.
func PermConfigWrite() Permission { return permConfigWrite }

// PermConfigPublish authorizes the config publish/rollback lifecycle
// (configpublish slice). A domain verb distinct from CRUD write, mirroring the
// dedicated publish endpoints' own admin gate.
func PermConfigPublish() Permission { return permConfigPublish }

// PermFlagRead authorizes reading/evaluating feature flags (featureflag slice:
// GET flag/{key}, GET flags, POST evaluate). Evaluate is a read-only computation.
func PermFlagRead() Permission { return permFlagRead }

// PermFlagWrite authorizes mutating feature flags (flagwrite slice:
// create/update/toggle/delete).
func PermFlagWrite() Permission { return permFlagWrite }

// accesscore permissions (PR-10c #1348). Each mirrors the role-literal
// auth.AnyRole(RoleAdmin) / auth.SelfOr("id", RoleAdmin) gate it replaces in the
// accesscore policymanage / identitymanage / rbaccheck slices. Same
// resource:action convention + accessor-func-over-private-singleton shape as the
// configcore perms (reassignment is a compile error → Hard immutability). "write"
// folds create/update/delete (plus lock/unlock/change-password for user) per the
// AWS-IAM Write access-level grouping the prior uniform admin gate already implied.
var (
	permPolicyRead  = newPermission("policy:read")
	permPolicyWrite = newPermission("policy:write")
	permUserRead    = newPermission("user:read")
	permUserWrite   = newPermission("user:write")
	permRoleRead    = newPermission("role:read")
)

// PermPolicyRead authorizes reading ABAC policies (policymanage slice: GET
// policies/{id}, GET policies). Migrated from auth.AnyRole(RoleAdmin) in PR-10c.
func PermPolicyRead() Permission { return permPolicyRead }

// PermPolicyWrite authorizes mutating ABAC policies (policymanage slice:
// create/update/delete). Migrated from auth.AnyRole(RoleAdmin) in PR-10c.
func PermPolicyWrite() Permission { return permPolicyWrite }

// PermUserRead authorizes reading a user account (identitymanage slice: GET
// users/{id}). The gate uses auth.RequirePermissionForResource("id", PermUserRead())
// which forwards the id path param to the PDP as resource; the PDP baseline
// ownership rule (subject.sub == resource.id, #1977 Batch B) grants self-access
// without a Go short-circuit. Non-self reads require admin/super-admin baseline.
// Migrated from auth.SelfOr("id", RoleAdmin) → RequirePermissionOrSelf (PR-10c) →
// RequirePermissionForResource (#1977).
func PermUserRead() Permission { return permUserRead }

// PermUserWrite authorizes mutating a user account (identitymanage slice:
// create/update/patch/delete/lock/unlock/change-password). "write" folds the
// account-management verbs the prior admin/self gates already grouped; the
// owner-scoped verbs (update/patch/change-password) use
// auth.RequirePermissionForResource — self-access decided by PDP baseline
// ownership rule (#1977 Batch B).
// Migrated from auth.AnyRole(RoleAdmin) / auth.SelfOr("id", RoleAdmin) in PR-10c.
func PermUserWrite() Permission { return permUserWrite }

// PermRoleRead authorizes reading a user's role assignments (rbaccheck slice:
// GET roles/{userID}, GET roles/{userID}/{roleName}). The gate uses
// auth.RequirePermissionForResource("userID", PermRoleRead()) which forwards the
// userID path param to the PDP as resource; the PDP baseline ownership rule
// (subject.sub == resource.id, #1977 Batch B) grants self-access via PDP.
// Migrated from auth.SelfOr("userID", RoleAdmin) → RequirePermissionOrSelf
// (PR-10c) → RequirePermissionForResource (#1977).
func PermRoleRead() Permission { return permRoleRead }

// permSessionVerify is the package-private singleton backing the
// PermSessionVerify() accessor. Unexported so no external package can reassign it.
var permSessionVerify = newPermission("session:verify")

// PermSessionVerify returns the permission authorizing service-to-service
// introspection of an access/session token via the accesscore sessionverifyrpc
// gRPC service (grpc.auth.session.verify.v1, #1154 — the first platform-cell gRPC
// service). It is a first-class, enumerable action distinct from user:read /
// system:read: token introspection reads live session state and is its own
// auditable authorization class. The gRPC per-method PDP gate (#2008) resolves it
// from endpoints.grpc.methods[].permission and the PDP baseline grants it to
// admin / super-admin (see authorizationdecide baseline.go). A non-admin service
// caller is granted it via a tenant policy overlay, not the baseline.
//
// It is an accessor function (not an exported var) so the registry value is
// immutable to external packages — reassigning a func is a compile error.
func PermSessionVerify() Permission { return permSessionVerify }

// examples/iotdevice permissions (PR-10d #1894). The iotdevice example owns its
// own lightweight PDP (cells/devicecell/authorizer.go) whose baseline grants
// these actions; the platform registry stays the SOLE minter (the Permission
// seal forbids minting outside this package), so example actions enroll here
// like every other action. Each is one orthogonal (resource, read|write,
// coarse|ownership) class — read vs write and coarse vs ownership are NEVER
// folded into one Permission, because a coarse-grant rule would otherwise
// bypass an ownership rule sharing the same action (same closed-set discipline
// as the accesscore owner-action grant surface). Same accessor-func-over-private
// -singleton shape as the corecells perms (reassignment is a compile error →
// Hard immutability). Migrated from auth.AnyRole / auth.SelfOr / the hand-rolled
// gRPC role gate the devicecell slices used pre-PR-10d.
var (
	permDeviceCommand = newPermission("device:command")
	permDeviceConsume = newPermission("device:consume")
	permDeviceRead    = newPermission("device:read")
	permDeviceList    = newPermission("device:list")
)

// PermDeviceCommand authorizes dispatching a command to a device (devicecommand
// enqueue / enqueue-async HTTP routes + the devicecommandrpc IssueCommand /
// WatchCommands gRPC methods). Baseline grants it to admin / operator.
func PermDeviceCommand() Permission { return permDeviceCommand }

// PermDeviceConsume authorizes a device acting on its own command lifecycle
// (devicecommand dequeue / report / ack / extend-lease). The gate uses
// auth.RequirePermissionForResource("id", PermDeviceConsume()); baseline grants
// it to the device itself (subject.sub == resource.id) or to admin / operator.
func PermDeviceConsume() Permission { return permDeviceConsume }

// PermDeviceRead authorizes reading a device's status (devicestatus). The gate
// uses auth.RequirePermissionForResource("id", PermDeviceRead()); baseline grants
// it to the device itself (subject.sub == resource.id) or to admin / operator.
func PermDeviceRead() Permission { return permDeviceRead }

// PermDeviceList authorizes listing all devices (devicelist). Baseline grants it
// to admin only.
func PermDeviceList() Permission { return permDeviceList }

// examples/todoorder permissions (PR-10d #1894). The todoorder example owns its
// own lightweight PDP (cells/ordercell/authorizer.go); same registry/seal +
// orthogonal-class rationale as the iotdevice perms above. create / list are
// coarse (role:customer); read / update are owner-scoped (subject.sub ==
// order.owner, owner supplied by a PIP lookup over the order repository).
var (
	permOrderCreate = newPermission("order:create")
	permOrderList   = newPermission("order:list")
	permOrderRead   = newPermission("order:read")
	permOrderUpdate = newPermission("order:update")
)

// PermOrderCreate authorizes creating an order (ordercreate). Baseline grants it
// to role:customer (the created order's owner is the authenticated subject).
func PermOrderCreate() Permission { return permOrderCreate }

// PermOrderList authorizes the collection-level reads that return an aggregate,
// not a single owned order (orderquery list + orderprojection summary). Baseline
// grants it to role:customer. Per-row owner filtering on list is a data-layer PEP
// (RowScope), tracked for PR-11/12 — out of this route-gate migration's scope.
func PermOrderList() Permission { return permOrderList }

// PermOrderRead authorizes reading a single order (orderquery get/{id}). The gate
// uses auth.RequirePermissionForResource("id", PermOrderRead()); baseline grants
// it only to the order owner (subject.sub == order.owner, owner via PIP lookup).
func PermOrderRead() Permission { return permOrderRead }

// PermOrderUpdate authorizes confirming/mutating a single order
// (orderconfirm PATCH /{id}/status). The gate uses
// auth.RequirePermissionForResource("id", PermOrderUpdate()); baseline grants it
// only to the order owner (subject.sub == order.owner, owner via PIP lookup).
func PermOrderUpdate() Permission { return permOrderUpdate }

// allPermissions is the closed registry of every Permission that exists. It
// backs Permissions() and lets tests pin the closed set (anti-vacuity: a new
// perm* var that is not added here is caught by the registry test).
var allPermissions = []Permission{
	permAuditRead,
	permSystemRead,
	permConfigRead,
	permConfigWrite,
	permConfigPublish,
	permFlagRead,
	permFlagWrite,
	permPolicyRead,
	permPolicyWrite,
	permUserRead,
	permUserWrite,
	permRoleRead,
	permSessionVerify,
	permDeviceCommand,
	permDeviceConsume,
	permDeviceRead,
	permDeviceList,
	permOrderCreate,
	permOrderList,
	permOrderRead,
	permOrderUpdate,
}

// String returns the action spelling carried into a PDP and stored in policy
// Action targets (e.g. "audit:read"). The zero value returns "" — never a valid
// action — so a forged/zero Permission cannot match a real policy rule.
func (p Permission) String() string {
	return p.s
}

// IsZero reports whether p is the invalid zero value (no minter ran). Consumers
// that receive a Permission from an untyped path should reject IsZero() to fail
// closed.
func (p Permission) IsZero() bool {
	return p.s == ""
}

// Permissions returns a copy of the closed Permission registry. Intended for
// tests and conformance enumeration; the returned slice is independent so a
// caller cannot mutate the registry.
func Permissions() []Permission {
	out := make([]Permission, len(allPermissions))
	copy(out, allPermissions)
	return out
}

// permissionByString indexes the closed registry by action spelling for O(1)
// resolution. It is built from allPermissions, so it cannot drift from the
// closed set (a new perm* var that enrolls in allPermissions is automatically
// resolvable; one that forgets is invisible here too — the same single source).
var permissionByString = func() map[string]Permission {
	m := make(map[string]Permission, len(allPermissions))
	for _, p := range allPermissions {
		m[p.s] = p
	}
	return m
}()

// PermissionByName resolves an action string (e.g. "device:command") back to its
// sealed Permission singleton from the closed registry. It RESOLVES, it does not
// MINT: the returned value is one of the pre-constructed allPermissions entries,
// so the seal is preserved — there is still no way to construct a Permission
// outside this file's registry. An unknown or empty string yields the zero
// Permission + ok=false, so a consumer that receives a Permission from an untyped
// path (e.g. the gRPC method permission overlay, #2008) fails closed: the
// registrar rejects ok=false at registration rather than gating on a forged action.
func PermissionByName(s string) (Permission, bool) {
	p, ok := permissionByString[s]
	return p, ok
}

// IsKnownPermissionString reports whether s is the action spelling of a registered
// Permission. It is the static closed-set predicate the gRPC per-method overlay
// governance check (FMT-41) uses to reject a typo'd permission at validate time,
// before it can reach the runtime gate.
func IsKnownPermissionString(s string) bool {
	_, ok := permissionByString[s]
	return ok
}
