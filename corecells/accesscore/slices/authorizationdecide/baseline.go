package authorizationdecide

// baseline.go — built-in baseline rule set for the ABAC PDP (PR-10a #1348).

import (
	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	runtimeauth "github.com/ghbvf/gocell/framework/runtime/auth"
)

// builtinBaseline is the package-level rule set returned by builtinBaselineRules.
// Defined once so callers that only need len() pay zero allocation cost and the
// evaluate loop and Authorize log read from the same backing array (F4 fix).
var builtinBaseline = []abac.Rule{
	{
		ID:         "baseline-audit-read-admin",
		Name:       "Baseline: allow admin/super-admin to read audit ledger",
		Effect:     authz.EffectAllow,
		Action:     []string{authz.PermAuditRead().String()},
		Conditions: []abac.Condition{adminOrSuperAdmin()},
	},
	{
		ID:         "baseline-system-read-admin",
		Name:       "Baseline: allow admin/super-admin to read runtime/system health (#1860)",
		Effect:     authz.EffectAllow,
		Action:     []string{authz.PermSystemRead().String()},
		Conditions: []abac.Condition{adminOrSuperAdmin()},
	},
	{
		// session:verify gates the accesscore sessionverifyrpc gRPC service
		// (grpc.auth.session.verify.v1, #1154 — first platform-cell gRPC service).
		// Token introspection is an admin/super-admin baseline action — same
		// action-scoped + role-conditioned shape as the system-read rule above. A
		// non-admin service caller is granted session:verify via a tenant policy
		// overlay, not the baseline.
		ID:         "baseline-session-verify-admin",
		Name:       "Baseline: allow admin/super-admin to introspect session tokens (#1154)",
		Effect:     authz.EffectAllow,
		Action:     []string{authz.PermSessionVerify().String()},
		Conditions: []abac.Condition{adminOrSuperAdmin()},
	},
	// configcore baseline (PR-10b #1348): one allow rule per migrated slice via
	// the shared adminOrSuperAdmin() condition. NOTE: the old configcore gates were
	// auth.AnyRole(RoleAdmin) — admin-only — so this WIDENS them to admit super-admin
	// (super-admin ⊇ admin; a deliberate relaxation, zero prod impact as superadmin
	// is not yet issued). See ADR §"Amendment: PR-10b" threat-model re-eval + ruling.
	// action-scoped + role-conditioned — same shape as the audit rule above.
	{
		ID:         "baseline-config-read-admin",
		Name:       "Baseline: allow admin/super-admin to read configuration",
		Effect:     authz.EffectAllow,
		Action:     []string{authz.PermConfigRead().String()},
		Conditions: []abac.Condition{adminOrSuperAdmin()},
	},
	{
		ID:         "baseline-config-write-admin",
		Name:       "Baseline: allow admin/super-admin to write configuration",
		Effect:     authz.EffectAllow,
		Action:     []string{authz.PermConfigWrite().String()},
		Conditions: []abac.Condition{adminOrSuperAdmin()},
	},
	{
		ID:         "baseline-config-publish-admin",
		Name:       "Baseline: allow admin/super-admin to publish/rollback configuration",
		Effect:     authz.EffectAllow,
		Action:     []string{authz.PermConfigPublish().String()},
		Conditions: []abac.Condition{adminOrSuperAdmin()},
	},
	{
		ID:         "baseline-flag-read-admin",
		Name:       "Baseline: allow admin/super-admin to read/evaluate feature flags",
		Effect:     authz.EffectAllow,
		Action:     []string{authz.PermFlagRead().String()},
		Conditions: []abac.Condition{adminOrSuperAdmin()},
	},
	{
		ID:         "baseline-flag-write-admin",
		Name:       "Baseline: allow admin/super-admin to write feature flags",
		Effect:     authz.EffectAllow,
		Action:     []string{authz.PermFlagWrite().String()},
		Conditions: []abac.Condition{adminOrSuperAdmin()},
	},
	// accesscore baseline (PR-10c #1348 + #1977 Batch B): one allow rule per migrated
	// permission via the shared adminOrSuperAdmin() condition. The old accesscore gates
	// were auth.AnyRole(RoleAdmin) / auth.SelfOr("id", RoleAdmin) — admin-only (HasRole
	// is a literal check, no hierarchy) — so routing them through adminOrSuperAdmin()
	// WIDENS them to admit super-admin (super-admin ⊇ admin; a deliberate relaxation,
	// zero prod impact as superadmin is not yet issued). Same as the PR-10b configcore
	// widening; see ADR §"Amendment: PR-10c" threat-model re-eval + ruling.
	// The self branch of the former SelfOr gates (user:read/write, role:read) is NOW a
	// baseline ownership rule (subject.sub == resource.id) rather than a Go short-circuit
	// in RequirePermissionOrSelf — see #1977 Batch B and the baseline-*-self rules below.
	// action-scoped + role-conditioned — same shape as the rules above.
	{
		ID:         "baseline-policy-read-admin",
		Name:       "Baseline: allow admin/super-admin to read ABAC policies",
		Effect:     authz.EffectAllow,
		Action:     []string{authz.PermPolicyRead().String()},
		Conditions: []abac.Condition{adminOrSuperAdmin()},
	},
	{
		ID:         "baseline-policy-write-admin",
		Name:       "Baseline: allow admin/super-admin to write ABAC policies",
		Effect:     authz.EffectAllow,
		Action:     []string{authz.PermPolicyWrite().String()},
		Conditions: []abac.Condition{adminOrSuperAdmin()},
	},
	{
		ID:         "baseline-user-read-admin",
		Name:       "Baseline: allow admin/super-admin to read user accounts",
		Effect:     authz.EffectAllow,
		Action:     []string{authz.PermUserRead().String()},
		Conditions: []abac.Condition{adminOrSuperAdmin()},
	},
	{
		ID:         "baseline-user-write-admin",
		Name:       "Baseline: allow admin/super-admin to manage user accounts",
		Effect:     authz.EffectAllow,
		Action:     []string{authz.PermUserWrite().String()},
		Conditions: []abac.Condition{adminOrSuperAdmin()},
	},
	{
		ID:         "baseline-role-read-admin",
		Name:       "Baseline: allow admin/super-admin to read user role assignments",
		Effect:     authz.EffectAllow,
		Action:     []string{authz.PermRoleRead().String()},
		Conditions: []abac.Condition{adminOrSuperAdmin()},
	},
	// registrycore admin-approval baseline (303-US7 #2238): approve/reject/retire are
	// admin lifecycle actions over runtime contract registrations. Same action-scoped
	// + role-conditioned shape as the config/policy/user coarse grants above — the
	// approval authority lives HERE as an ABAC condition, never as a handler role
	// literal (tenancy.md §"ABAC authz 接线"). registry:submit/read are NOT granted
	// here: their baseline grant is deferred to corebundle composition (open #2477;
	// NOT the closed US4 #2235); migrating their gate to the resolver path (this PR)
	// did not change that.
	{
		ID:         "baseline-registry-approve-admin",
		Name:       "Baseline: allow admin/super-admin to approve contract registrations",
		Effect:     authz.EffectAllow,
		Action:     []string{authz.PermRegistryApprove().String()},
		Conditions: []abac.Condition{adminOrSuperAdmin()},
	},
	{
		ID:         "baseline-registry-reject-admin",
		Name:       "Baseline: allow admin/super-admin to reject contract registrations",
		Effect:     authz.EffectAllow,
		Action:     []string{authz.PermRegistryReject().String()},
		Conditions: []abac.Condition{adminOrSuperAdmin()},
	},
	{
		ID:         "baseline-registry-retire-admin",
		Name:       "Baseline: allow admin/super-admin to retire contract registrations",
		Effect:     authz.EffectAllow,
		Action:     []string{authz.PermRegistryRetire().String()},
		Conditions: []abac.Condition{adminOrSuperAdmin()},
	},
	// Identity-ownership baseline rules (#1977 Batch B): subject.sub == resource.id
	// grants owner-scoped permissions (user:read, user:write, role:read) to the
	// owning subject. resource.id is the canonicalized path param forwarded by
	// RequirePermissionForResource. An empty resourceID makes resource.id not-found
	// → ownership condition unsatisfied → fail-closed (empty param ≠ self).
	// Semantics: self OR admin (either the ownership rule or the admin rule fires).
	// action-scoped (these 3 perms only) — ownership does not grant config/audit/etc.
	{
		ID:         "baseline-user-read-self",
		Name:       "Baseline: allow a user to read their own account (subject.sub == resource.id)",
		Effect:     authz.EffectAllow,
		Action:     []string{authz.PermUserRead().String()},
		Conditions: []abac.Condition{subjectIsResource()},
	},
	{
		ID:         "baseline-user-write-self",
		Name:       "Baseline: allow a user to write their own account (subject.sub == resource.id)",
		Effect:     authz.EffectAllow,
		Action:     []string{authz.PermUserWrite().String()},
		Conditions: []abac.Condition{subjectIsResource()},
	},
	{
		ID:         "baseline-role-read-self",
		Name:       "Baseline: allow a user to read their own role assignments (subject.sub == resource.id)",
		Effect:     authz.EffectAllow,
		Action:     []string{authz.PermRoleRead().String()},
		Conditions: []abac.Condition{subjectIsResource()},
	},
	// access:decide self-introspection baseline (#1863, BR-004). The decide endpoint
	// (POST /api/v1/access/decide) is gated by auth.RequirePermissionForSelf, which
	// forwards the caller's OWN subject as resource — so the self rule
	// (subject.sub == resource.id) grants any authenticated user the right to query
	// the PDP about themselves. This is a CONDITIONED grant (NOT an unconditional
	// allow-all): access:decide is registered as an owner-scoped action in
	// baseline_freeze_test.go, so BASELINE-OWNER-RULE-TENANT-FREEZE-01 holds its
	// allow surface to the closed {owner, admin} set — same shape as user:read /
	// role:read. The admin rule is dormant for the CURRENT endpoint: the
	// RequirePermissionForSelf gate always evaluates access:decide with resource ==
	// the caller's OWN subject, so the self rule alone admits every authenticated
	// caller through the gate (the admin rule adds nothing while resource==caller).
	// (Distinct resource: the handler's downstream Authorize for the *queried* action
	// uses the free-form req.Resource — but that evaluates THAT action's own rules,
	// not access:decide's.) The admin rule is required by the {1 owner, 1 admin}
	// closed-set invariant and is what will let admin/super-admin decide access:decide
	// about OTHER subjects once a future endpoint forwards a non-self subject as the
	// access:decide resource (ABAC §4.x).
	{
		ID:         "baseline-access-decide-self",
		Name:       "Baseline: allow a user to query their own authorization decisions (subject.sub == resource.id)",
		Effect:     authz.EffectAllow,
		Action:     []string{authz.PermAccessDecide().String()},
		Conditions: []abac.Condition{subjectIsResource()},
	},
	{
		ID:         "baseline-access-decide-admin",
		Name:       "Baseline: allow admin/super-admin to query authorization decisions",
		Effect:     authz.EffectAllow,
		Action:     []string{authz.PermAccessDecide().String()},
		Conditions: []abac.Condition{adminOrSuperAdmin()},
	},
	// device-ownership baseline (#2351, presence-backend prerequisite): device:read
	// gates the framework-owned http.devicestate.v1 serving handler
	// (auth.RequirePermissionForResource("id", PermDeviceRead()), cellmodules/deviceserving).
	// Living here is by the centralized-PDP-baseline convention: accesscore is the single
	// baseline source for ALL permissions regardless of consuming cell (config:read from
	// configcore, audit:read from auditcore, …); a framework-owned contract is no exception.
	// The self model is device-SELF ownership: a DEVICE principal (subject.kind == device)
	// whose subject.sub == resource.id reads its OWN state (#2400 review F1). Unlike the
	// kind-agnostic user/role self rules, this carries an explicit subject.kind == device
	// qualifier — a normal user whose subject UUID happens to equal a device id does NOT get
	// device-self read (defense-in-depth + zero-trust subject/resource separation, cf. k8s
	// SubjectAccessReview / SPIFFE distinguishing subject identity from resource). The
	// user-owns-device path (a user reading a device they OWN) is the future DELEGATED rule
	// (subject.sub == resource.owner via a device→owner PIP lookup), which lands with the
	// presence backend / device registry — NOT this self rule.
	// Enabling the grant now (before real presence data) is safe: the handler always returns
	// state "unknown" for any id (no device registry), so there is no device-existence oracle —
	// it lets the ownership model be tested + frozen before data lands, leaving the presence PR
	// to wire only the data-layer RowScope/tenant isolation. Closed {owner, admin} surface,
	// frozen by BASELINE-OWNER-RULE-TENANT-FREEZE-01 (baseline-device-read-self registered there
	// with the device-self two-condition shape).
	{
		ID:         "baseline-device-read-admin",
		Name:       "Baseline: allow admin/super-admin to read any device's presence state",
		Effect:     authz.EffectAllow,
		Action:     []string{authz.PermDeviceRead().String()},
		Conditions: []abac.Condition{adminOrSuperAdmin()},
	},
	{
		ID:         "baseline-device-read-self",
		Name:       "Baseline: allow a device to read its OWN presence state (subject.kind == device AND subject.sub == resource.id)",
		Effect:     authz.EffectAllow,
		Action:     []string{authz.PermDeviceRead().String()},
		Conditions: deviceSelfOwnership(),
	},
	// device:enroll baseline (#1904, EST enrollment front-end prerequisite):
	// device:enroll gates the cert-signing authorization path in the EST enrollment
	// handler. The self model is device-SELF enrollment: a DEVICE principal (e.g.
	// supplied via NewDeviceSubjectDescriptor + AuthorizeAs) whose sub matches the
	// resource id (the device id) may enroll ITS OWN certificate. Shape mirrors
	// device:read — same kind-gated device-self two-condition shape (#2400 F1) —
	// and is frozen by BASELINE-OWNER-RULE-TENANT-FREEZE-01 (registered as
	// baseline-device-enroll-self with the device-self two-condition shape).
	// Admin/super-admin may enroll any device's certificate via the admin rule.
	{
		ID:         "baseline-device-enroll-admin",
		Name:       "Baseline: allow admin/super-admin to enroll any device certificate",
		Effect:     authz.EffectAllow,
		Action:     []string{authz.PermDeviceEnroll().String()},
		Conditions: []abac.Condition{adminOrSuperAdmin()},
	},
	{
		ID:         "baseline-device-enroll-self",
		Name:       "Baseline: allow a device to enroll its OWN certificate (subject.kind == device AND subject.sub == resource.id)",
		Effect:     authz.EffectAllow,
		Action:     []string{authz.PermDeviceEnroll().String()},
		Conditions: deviceSelfOwnership(),
	},
}

// subjectIsResource returns the cross-attribute ABAC condition that checks
// subject.sub == resource.id (#1977 Batch B) — the id-equality ownership condition.
// Used STANDALONE by the kind-agnostic identity-ownership self rules (user:read/write,
// role:read, access:decide — a user owns its own account/roles/decisions). device-self
// (deviceSelfOwnership, #2400 F1) reuses it as the id-equality half, ANDed with an
// explicit subject.kind == device qualifier. Uses abac.OpEqualsAttr to
// compare the LHS (subject.sub) against the RHS (resource.id) at evaluation
// time — both resolved via attributeResolver, both fail-closed on not-found.
//
// "sub" is the canonical JWT claim key; resolveSubject maps both "sub" and
// "subject" to principal.Subject (and canonicalizes it via ParseCanonicalUUID
// so UUID case/format differences do not thwart the comparison). The RHS
// resource.id is set from the resourceID field in attributeResolver, which
// RequirePermissionForResource populates with the canonicalized path param.
// Fail-closed on absent resource.id is enforced in evaluator.go matchCondition
// (RHS attribute not-found → condition unsatisfied → ownership rule cannot grant).
func subjectIsResource() abac.Condition {
	return abac.Condition{
		Source:    abac.SourceSubject,
		Key:       "sub",
		Operator:  abac.OpEqualsAttr,
		RHSSource: abac.SourceResource,
		RHSKey:    "id",
	}
}

// deviceSelfOwnership returns the two-condition device-SELF ownership shape (#2400 F1):
// subject.kind == "device" AND subject.sub == resource.id. Unlike the kind-agnostic
// subjectIsResource() shape used by the user/role/access self rules, device-self requires
// the principal to BE a device — a normal user whose subject UUID equals a device id does
// NOT match. The user-owns-device path is the future delegated rule (subject.sub ==
// resource.owner via a device→owner PIP lookup), not this self rule. The condition ORDER
// is fixed [kind, sub==id] to match the frozen shape in BASELINE-OWNER-RULE-TENANT-FREEZE-01.
// The "device" literal is sourced from PrincipalDevice.String() so it matches exactly what
// attributeResolver.resolveSubject("kind") emits (principal.Kind.String()).
func deviceSelfOwnership() []abac.Condition {
	return []abac.Condition{
		{
			Source:   abac.SourceSubject,
			Key:      "kind",
			Operator: abac.OpEquals,
			Values:   []string{runtimeauth.PrincipalDevice.String()},
		},
		subjectIsResource(),
	}
}

// adminOrSuperAdmin is the shared baseline condition: subject.roles ∈
// {admin, super-admin}. Extracted because all current baseline rules share this
// same admin+super-admin condition (≥3 identical literals → constant, per
// go-standards). For audit this matched the prior gate exactly; for configcore it
// widens the prior admin-only gate (see configcore baseline note above + the ADR).
// Role constants come from runtime/auth so the values match exactly what
// attributeResolver.resolveSubject emits for the "roles" key (r.principal.Roles).
func adminOrSuperAdmin() abac.Condition {
	return abac.Condition{
		Source:   abac.SourceSubject,
		Key:      "roles",
		Operator: abac.OpIn,
		Values:   []string{runtimeauth.RoleAdmin, runtimeauth.RoleSuperAdmin},
	}
}

// builtinBaselineRules returns the tenant-agnostic built-in baseline rule set.
//
// Built-in baseline reproducing the existing role→endpoint gate. action-scoped +
// role-conditioned or ownership-conditioned — NOT a downgrade allow-all
// (FR-011 fail-closed governs missing-attr/store-err; the baseline governs the
// default policy set; the two do not conflict). One rule per migrated endpoint;
// PR-10b/PR-10c/PR-10d (#1977 Batch B) extend it.
//
// Current baseline: PermAuditRead() (PR-10a) + the 5 configcore permissions
// (PR-10b: config:read/write/publish, flag:read/write) + the 5 accesscore
// permissions (PR-10c: policy:read/write, user:read/write, role:read) for
// admin/super-admin; + 3 identity-ownership rules (#1977 Batch B:
// user:read/write, role:read for subject.sub == resource.id); + 2 access:decide
// rules (#1863: self subject.sub == resource.id + admin) for the PDP
// self-introspection endpoint; + 2 device:read rules (#2351/#2400: device-self
// subject.kind == device AND subject.sub == resource.id + admin) for the framework-owned
// devicestate serving handler (presence-backend prerequisite); + 2 device:enroll
// rules (#1904: device-self subject.kind == device AND subject.sub == resource.id
// + admin) for the EST enrollment cert-signing authorization path. Each rule is
// action-scoped so a baseline allow for one permission never leaks to another.
//
// Returns the package-level builtinBaseline slice directly (no allocation).
func builtinBaselineRules() []abac.Rule {
	return builtinBaseline
}
