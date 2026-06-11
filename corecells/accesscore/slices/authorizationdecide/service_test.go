package authorizationdecide

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/runtime/auth"
)

const testTenantIDStr = "00000000-0000-0000-0000-000000000001"

var testTenantID = func() tenant.TenantID {
	t, err := tenant.ParseTenantID(testTenantIDStr)
	if err != nil {
		panic("authorizationdecide_test: invalid testTenantID: " + err.Error())
	}
	return t
}()

// fixedClockTime is the deterministic clock reading injected into evaluator
// environment-attribute tests. 2026-06-07 14:30 UTC → hour "14".
var fixedClockTime = time.Date(2026, 6, 7, 14, 30, 0, 0, time.UTC)

// --- test helpers ---------------------------------------------------------

func policyWith(id string, rules ...abac.Rule) *abac.Policy {
	return &abac.Policy{ID: id, TenantID: testTenantID, Name: id, Rules: rules}
}

func permitRule(id string, obl authz.Obligations, conds ...abac.Condition) abac.Rule {
	return abac.Rule{ID: id, Name: id, Effect: authz.EffectAllow, Conditions: conds, Obligations: obl}
}

func forbidRule(id string, conds ...abac.Condition) abac.Rule {
	return abac.Rule{ID: id, Name: id, Effect: authz.EffectDeny, Conditions: conds}
}

func cond(src abac.AttributeSource, key string, op abac.Operator, vals ...string) abac.Condition {
	return abac.Condition{Source: src, Key: key, Operator: op, Values: vals}
}

// reqCtx builds a request context carrying the test tenant and the given
// principal (the trusted subject-attribute source).
func reqCtx(p *auth.Principal) context.Context {
	ctx := ctxkeys.WithTenantID(context.Background(), testTenantIDStr)
	if p != nil {
		ctx = auth.WithPrincipal(ctx, p)
	}
	return ctx
}

func userPrincipal(claims map[string]string, roles ...string) *auth.Principal {
	return &auth.Principal{Kind: auth.PrincipalUser, Subject: "usr-1", TenantID: testTenantIDStr, Roles: roles, Claims: claims}
}

// newEngine builds the ABAC engine over the given policy store and clock.
func newEngine(t *testing.T, repo ports.PolicyRepository, clk clock.Clock) *Service {
	t.Helper()
	svc, err := NewService(clk, repo, slog.Default(), WithTxManager(outbox.DemoCellTxManager()))
	require.NoError(t, err)
	return svc
}

// memEngineWithPolicies seeds a mem policy store with the given policies and
// returns an engine over it (clock fixed to fixedClockTime).
func memEngineWithPolicies(t *testing.T, policies ...*abac.Policy) *Service {
	t.Helper()
	repo := mem.NewPolicyRepository()
	for _, p := range policies {
		require.NoError(t, repo.Create(context.Background(), testTenantID, p))
	}
	return newEngine(t, repo, clockmock.New(fixedClockTime))
}

// --- constructor guards ---------------------------------------------------

func TestNewService_NilPolicyRepo(t *testing.T) {
	tests := []struct {
		name string
		repo ports.PolicyRepository
	}{
		{"bare nil", nil},
		{"typed nil", (*mem.PolicyRepository)(nil)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewService(clock.Real(), tt.repo, slog.Default(),
				WithTxManager(outbox.DemoCellTxManager()))
			require.Error(t, err)
			var ecErr *errcode.Error
			require.ErrorAs(t, err, &ecErr)
			assert.Equal(t, errcode.KindInternal, ecErr.Kind)
		})
	}
}

func TestNewService_NilTxManager(t *testing.T) {
	_, err := NewService(clock.Real(), mem.NewPolicyRepository(), slog.Default())
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.KindInvalid, ecErr.Kind)
}

// --- decision matrix ------------------------------------------------------

func TestAuthorize_DecisionMatrix(t *testing.T) {
	engPermit := cond(abac.SourceSubject, "department", abac.OpEquals, "eng")
	tests := []struct {
		name      string
		policies  []*abac.Policy
		principal *auth.Principal
		wantAllow bool
	}{
		{
			name:      "hit: permit condition satisfied",
			policies:  []*abac.Policy{policyWith("p1", permitRule("r1", authz.Obligations{}, engPermit))},
			principal: userPrincipal(map[string]string{"department": "eng"}),
			wantAllow: true,
		},
		{
			name: "forbid-wins: permit and forbid both match",
			policies: []*abac.Policy{policyWith(
				"p1",
				permitRule("allow", authz.Obligations{}, engPermit),
				forbidRule("deny", engPermit),
			)},
			principal: userPrincipal(map[string]string{"department": "eng"}),
			wantAllow: false,
		},
		{
			name:      "missing attribute fails closed",
			policies:  []*abac.Policy{policyWith("p1", permitRule("r1", authz.Obligations{}, engPermit))},
			principal: userPrincipal(map[string]string{"other": "x"}),
			wantAllow: false,
		},
		{
			name:      "default-deny: no policies",
			policies:  nil,
			principal: userPrincipal(map[string]string{"department": "eng"}),
			wantAllow: false,
		},
		{
			name: "subject.kind==device matches (posture matrix in TestAuthorize_DevicePosture)",
			policies: []*abac.Policy{policyWith("p1", permitRule("r1", authz.Obligations{},
				cond(abac.SourceSubject, "kind", abac.OpEquals, "device")))},
			principal: &auth.Principal{
				Kind: auth.PrincipalDevice, Subject: "dev-1", TenantID: testTenantIDStr,
				Claims: map[string]string{"device_trust": "managed"},
			},
			wantAllow: true,
		},
		{
			name: "resource condition fails closed (no source in PR-7)",
			policies: []*abac.Policy{policyWith("p1", permitRule("r1", authz.Obligations{},
				cond(abac.SourceResource, "classification", abac.OpEquals, "public")))},
			principal: userPrincipal(map[string]string{"department": "eng"}),
			wantAllow: false,
		},
		{
			name: "subject roles (multi-valued) satisfy in operator",
			policies: []*abac.Policy{policyWith("p1", permitRule("r1", authz.Obligations{},
				cond(abac.SourceSubject, "roles", abac.OpIn, "admin")))},
			principal: userPrincipal(nil, "viewer", "admin"),
			wantAllow: true,
		},
		{
			// roles attribute is present (found=true) but empty for a principal
			// with no roles, so "roles not_in {banned}" is satisfied (the empty
			// set contains no banned role) → permit grants. Documents that
			// negative operators over a PRESENT-but-empty multi-valued attribute
			// differ from a MISSING attribute (which fails closed).
			name: "empty roles satisfy not_in (present-but-empty, not missing)",
			policies: []*abac.Policy{policyWith("p1", permitRule("r1", authz.Obligations{},
				cond(abac.SourceSubject, "roles", abac.OpNotIn, "banned")))},
			principal: userPrincipal(nil),
			wantAllow: true,
		},
		{
			name: "forbid in a separate policy wins (cross-policy deny-overrides)",
			policies: []*abac.Policy{
				policyWith("permit-pol", permitRule("r1", authz.Obligations{}, engPermit)),
				policyWith("forbid-pol", forbidRule("r2", engPermit)),
			},
			principal: userPrincipal(map[string]string{"department": "eng"}),
			wantAllow: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng := memEngineWithPolicies(t, tt.policies...)
			dec, err := eng.Authorize(reqCtx(tt.principal), "usr-1", "/api/v1/x", "read")
			require.NoError(t, err)
			assert.Equal(t, tt.wantAllow, dec.IsAllow())
		})
	}
}

// --- device posture -------------------------------------------------------

// TestAuthorize_DevicePosture is the F6 (Codex) guard: a device-posture policy
// must actually gate on the device_trust attribute (resolved from the device
// principal's claims), not merely on subject.kind. Missing or wrong posture must
// fail closed.
func TestAuthorize_DevicePosture(t *testing.T) {
	pol := policyWith("p1", permitRule("r1", authz.Obligations{},
		cond(abac.SourceSubject, "kind", abac.OpEquals, "device"),
		cond(abac.SourceSubject, "device_trust", abac.OpEquals, "managed")))
	device := func(claims map[string]string) *auth.Principal {
		return &auth.Principal{Kind: auth.PrincipalDevice, Subject: "dev-1", TenantID: testTenantIDStr, Claims: claims}
	}
	tests := []struct {
		name      string
		principal *auth.Principal
		wantAllow bool
	}{
		{"managed device permits", device(map[string]string{"device_trust": "managed"}), true},
		{"unmanaged device denied", device(map[string]string{"device_trust": "unmanaged"}), false},
		{"missing posture fails closed", device(nil), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng := memEngineWithPolicies(t, pol)
			dec, err := eng.Authorize(reqCtx(tt.principal), "dev-1", "/x", "read")
			require.NoError(t, err)
			assert.Equal(t, tt.wantAllow, dec.IsAllow())
		})
	}
}

// --- operators ------------------------------------------------------------

func TestAuthorize_Operators(t *testing.T) {
	tests := []struct {
		name      string
		op        abac.Operator
		ruleVals  []string
		claimVal  string
		wantAllow bool
	}{
		{"eq match", abac.OpEquals, []string{"eng"}, "eng", true},
		{"eq no-match", abac.OpEquals, []string{"eng"}, "sales", false},
		{"neq match", abac.OpNotEquals, []string{"eng"}, "sales", true},
		{"neq no-match", abac.OpNotEquals, []string{"eng"}, "eng", false},
		{"in match", abac.OpIn, []string{"eng", "ops"}, "ops", true},
		{"in no-match", abac.OpIn, []string{"eng", "ops"}, "sales", false},
		{"not_in match", abac.OpNotIn, []string{"eng", "ops"}, "sales", true},
		{"not_in no-match", abac.OpNotIn, []string{"eng", "ops"}, "eng", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pol := policyWith("p1", permitRule("r1", authz.Obligations{},
				cond(abac.SourceSubject, "department", tt.op, tt.ruleVals...)))
			eng := memEngineWithPolicies(t, pol)
			dec, err := eng.Authorize(reqCtx(userPrincipal(map[string]string{"department": tt.claimVal})), "usr-1", "/x", "read")
			require.NoError(t, err)
			assert.Equal(t, tt.wantAllow, dec.IsAllow())
		})
	}
}

// --- environment (clock-injected) -----------------------------------------

func TestAuthorize_EnvironmentTimeAttributes(t *testing.T) {
	wantHour := "14" // fixedClockTime hour
	wantDOW := strings.ToLower(fixedClockTime.Weekday().String())

	t.Run("env.hour matches injected clock", func(t *testing.T) {
		pol := policyWith("p1", permitRule("r1", authz.Obligations{},
			cond(abac.SourceEnvironment, "hour", abac.OpEquals, wantHour)))
		eng := memEngineWithPolicies(t, pol)
		dec, err := eng.Authorize(reqCtx(userPrincipal(nil)), "usr-1", "/x", "read")
		require.NoError(t, err)
		assert.True(t, dec.IsAllow())
	})

	t.Run("env.hour wrong value fails closed", func(t *testing.T) {
		pol := policyWith("p1", permitRule("r1", authz.Obligations{},
			cond(abac.SourceEnvironment, "hour", abac.OpEquals, "03")))
		eng := memEngineWithPolicies(t, pol)
		dec, err := eng.Authorize(reqCtx(userPrincipal(nil)), "usr-1", "/x", "read")
		require.NoError(t, err)
		assert.False(t, dec.IsAllow())
	})

	t.Run("env.day_of_week matches injected clock", func(t *testing.T) {
		pol := policyWith("p1", permitRule("r1", authz.Obligations{},
			cond(abac.SourceEnvironment, "day_of_week", abac.OpEquals, wantDOW)))
		eng := memEngineWithPolicies(t, pol)
		dec, err := eng.Authorize(reqCtx(userPrincipal(nil)), "usr-1", "/x", "read")
		require.NoError(t, err)
		assert.True(t, dec.IsAllow())
	})
}

// --- obligations ----------------------------------------------------------

func TestAuthorize_ObligationsPropagation(t *testing.T) {
	obl := authz.Obligations{RowScope: tenant.RowScopeSelf, FieldMask: authz.FieldMask{Fields: []string{"ssn"}}}
	pol := policyWith("p1", permitRule("r1", obl,
		cond(abac.SourceSubject, "department", abac.OpEquals, "eng")))
	eng := memEngineWithPolicies(t, pol)

	dec, err := eng.Authorize(reqCtx(userPrincipal(map[string]string{"department": "eng"})), "usr-1", "/x", "read")
	require.NoError(t, err)
	require.True(t, dec.IsAllow())
	got := dec.Obligations()
	assert.Equal(t, tenant.RowScopeSelf, got.RowScope)
	assert.Equal(t, []string{"ssn"}, got.FieldMask.Fields)
}

func TestAuthorize_ObligationsMerge_NarrowestRowScope_UnionFieldMask(t *testing.T) {
	// Two matching permits: merge RowScope narrowest (self < tenant) and
	// FieldMask union.
	pol := policyWith(
		"p1",
		permitRule("wide", authz.Obligations{RowScope: tenant.RowScopeTenant, FieldMask: authz.FieldMask{Fields: []string{"a"}}},
			cond(abac.SourceSubject, "department", abac.OpEquals, "eng")),
		permitRule("narrow", authz.Obligations{RowScope: tenant.RowScopeSelf, FieldMask: authz.FieldMask{Fields: []string{"b"}}},
			cond(abac.SourceSubject, "department", abac.OpEquals, "eng")),
	)
	eng := memEngineWithPolicies(t, pol)
	dec, err := eng.Authorize(reqCtx(userPrincipal(map[string]string{"department": "eng"})), "usr-1", "/x", "read")
	require.NoError(t, err)
	require.True(t, dec.IsAllow())
	got := dec.Obligations()
	assert.Equal(t, tenant.RowScopeSelf, got.RowScope, "narrowest non-zero RowScope wins")
	assert.ElementsMatch(t, []string{"a", "b"}, got.FieldMask.Fields, "FieldMask is the union")
}

// --- fail-closed infra paths ----------------------------------------------

// errPolicyRepo returns err from every method (store-down simulation).
type errPolicyRepo struct{ err error }

func (r errPolicyRepo) Create(context.Context, tenant.TenantID, *abac.Policy) error { return r.err }

func (r errPolicyRepo) Update(_ context.Context, _ tenant.TenantID, _ string, _ int, _ *abac.Policy) (*abac.Policy, error) {
	return nil, r.err
}

func (r errPolicyRepo) Delete(_ context.Context, _ tenant.TenantID, _ string, _ int) (*abac.Policy, error) {
	return nil, r.err
}

func (r errPolicyRepo) GetByID(context.Context, tenant.TenantID, string) (*abac.Policy, error) {
	return nil, r.err
}

func (r errPolicyRepo) ListByTenant(context.Context, tenant.TenantID) ([]*abac.Policy, error) {
	return nil, r.err
}

func (r errPolicyRepo) RepoReady(context.Context) error { return r.err }

func TestAuthorize_StoreDown_DeniesUnavailable(t *testing.T) {
	eng := newEngine(t, errPolicyRepo{err: errors.New("connection refused")}, clockmock.New(fixedClockTime))
	dec, err := eng.Authorize(reqCtx(userPrincipal(map[string]string{"department": "eng"})), "usr-1", "/x", "read")

	require.Error(t, err, "store failure must surface an error")
	assert.False(t, dec.IsAllow(), "store failure must fail closed (non-Allow)")
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.KindUnavailable, ecErr.Kind, "store failure → KindUnavailable (503)")
}

func TestAuthorize_NoTenant_Denies(t *testing.T) {
	eng := memEngineWithPolicies(t)
	// No tenant in ctx (principal present but no tenant key).
	dec, err := eng.Authorize(auth.WithPrincipal(context.Background(), userPrincipal(nil)), "usr-1", "/x", "read")
	require.Error(t, err, "missing tenant scope must surface an error")
	assert.False(t, dec.IsAllow(), "missing tenant must fail closed")
}

// TestAuthorize_NoPrincipal_FailsClosed is the F1 (Codex) guard: with a tenant
// present but NO authenticated principal, even an unconditional or environment-
// only permit must not grant — those rule shapes never resolve a subject, so the
// PDP must fail closed at entry.
func TestAuthorize_NoPrincipal_FailsClosed(t *testing.T) {
	tests := []struct {
		name string
		rule abac.Rule
	}{
		{"unconditional permit", permitRule("r1", authz.Obligations{})},
		{"environment-only permit", permitRule("r1", authz.Obligations{},
			cond(abac.SourceEnvironment, "hour", abac.OpEquals, "14"))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			eng := memEngineWithPolicies(t, policyWith("p1", tt.rule))
			// tenant present, NO principal in ctx.
			ctx := ctxkeys.WithTenantID(context.Background(), testTenantIDStr)
			dec, err := eng.Authorize(ctx, "usr-1", "/x", "read")
			require.Error(t, err, "no authenticated principal must fail closed with an error")
			var ecErr *errcode.Error
			require.ErrorAs(t, err, &ecErr)
			assert.Equal(t, errcode.KindPermissionDenied, ecErr.Kind)
			assert.False(t, dec.IsAllow(), "no principal must never grant, even on an unconditional/env-only permit")
		})
	}
}

// --- RLS scoping ----------------------------------------------------------

// scopeCapturingPolicyRepo records the tenant scope present when ListByTenant
// runs, proving the policy read is wrapped in a scoped transaction.
type scopeCapturingPolicyRepo struct {
	inner         ports.PolicyRepository
	capturedScope tenant.TenantID
	capturedOK    bool
}

func (r *scopeCapturingPolicyRepo) Create(ctx context.Context, t tenant.TenantID, p *abac.Policy) error {
	return r.inner.Create(ctx, t, p)
}

func (r *scopeCapturingPolicyRepo) Update(
	ctx context.Context, t tenant.TenantID, id string, expectedVersion int, p *abac.Policy,
) (*abac.Policy, error) {
	return r.inner.Update(ctx, t, id, expectedVersion, p)
}

func (r *scopeCapturingPolicyRepo) Delete(ctx context.Context, t tenant.TenantID, id string, expectedVersion int) (*abac.Policy, error) {
	return r.inner.Delete(ctx, t, id, expectedVersion)
}

func (r *scopeCapturingPolicyRepo) GetByID(ctx context.Context, t tenant.TenantID, id string) (*abac.Policy, error) {
	return r.inner.GetByID(ctx, t, id)
}

func (r *scopeCapturingPolicyRepo) ListByTenant(ctx context.Context, t tenant.TenantID) ([]*abac.Policy, error) {
	r.capturedScope, r.capturedOK = tenant.ScopeFromContext(ctx)
	return r.inner.ListByTenant(ctx, t)
}

func (r *scopeCapturingPolicyRepo) RepoReady(ctx context.Context) error {
	return r.inner.RepoReady(ctx)
}

// TestAuthorize_IsRLSScoped asserts the policy load runs inside a tenant-scoped
// transaction (scopedtx.Do), so PR-8's RLS-protected PG policy table is satisfied.
func TestAuthorize_IsRLSScoped(t *testing.T) {
	cap := &scopeCapturingPolicyRepo{inner: mem.NewPolicyRepository()}
	eng := newEngine(t, cap, clockmock.New(fixedClockTime))

	_, err := eng.Authorize(reqCtx(userPrincipal(nil)), "usr-1", "/x", "read")
	require.NoError(t, err)

	assert.True(t, cap.capturedOK, "ListByTenant must run inside a scoped tx (tenant.ScopeFromContext set)")
	assert.Equal(t, testTenantID, cap.capturedScope, "policy read scope must equal the request tenant")
}

// --- conformance ----------------------------------------------------------

// TestAuthorizerConformance runs the core decision scenarios against the
// in-memory policy store. The same suite is enrolled against the PG store by
// TestAuthorizerConformance_PG (authorizer_conformance_integration_test.go,
// `-tags=integration`), so the engine is proven to behave identically across
// policy-store backends — including the persisted-JSON decode and the
// scopedtx → SET LOCAL → ListByTenant read path exercised by a real
// adapterpg.TxManager. (RLS *enforcement* under a restricted role is proven
// separately by the FORCE-RLS policies guards: schema_guard verifyRLS,
// rls_force negative-drop, and the PolicyRepository CrossTenant conformance.)
func TestAuthorizerConformance(t *testing.T) {
	runAuthorizerConformance(t, func(t *testing.T) (ports.PolicyRepository, persistence.CellTxManager) {
		return mem.NewPolicyRepository(), outbox.DemoCellTxManager()
	})
}

// runAuthorizerConformance runs the core decision scenarios against a policy
// store + tx manager produced by factory (called once per sub-test so each gets
// a fresh, isolated store). The provided CellTxManager wires the engine's
// scoped policy read: mem passes the demo tx manager, PG passes a real
// adapterpg.TxManager so the scopedtx SET LOCAL path is genuinely exercised.
func runAuthorizerConformance(t *testing.T, factory func(t *testing.T) (ports.PolicyRepository, persistence.CellTxManager)) {
	t.Helper()
	build := func(t *testing.T, policies ...*abac.Policy) *Service {
		repo, txm := factory(t)
		for _, p := range policies {
			require.NoError(t, repo.Create(context.Background(), testTenantID, p))
		}
		svc, err := NewService(clockmock.New(fixedClockTime), repo, slog.Default(), WithTxManager(txm))
		require.NoError(t, err)
		return svc
	}
	engPermit := cond(abac.SourceSubject, "department", abac.OpEquals, "eng")

	t.Run("permit", func(t *testing.T) {
		eng := build(t, policyWith("p1", permitRule("r1", authz.Obligations{}, engPermit)))
		dec, err := eng.Authorize(reqCtx(userPrincipal(map[string]string{"department": "eng"})), "u", "/x", "read")
		require.NoError(t, err)
		assert.True(t, dec.IsAllow())
	})

	t.Run("forbid-wins", func(t *testing.T) {
		eng := build(t, policyWith("p1", permitRule("a", authz.Obligations{}, engPermit), forbidRule("d", engPermit)))
		dec, err := eng.Authorize(reqCtx(userPrincipal(map[string]string{"department": "eng"})), "u", "/x", "read")
		require.NoError(t, err)
		assert.False(t, dec.IsAllow())
	})

	t.Run("default-deny", func(t *testing.T) {
		eng := build(t)
		dec, err := eng.Authorize(reqCtx(userPrincipal(map[string]string{"department": "eng"})), "u", "/x", "read")
		require.NoError(t, err)
		assert.False(t, dec.IsAllow())
	})
}

// --- evaluator white-box units (branches not reachable via seeded policies) ---

// TestMatchCondition_UnknownOperator_FailsClosed covers the matchCondition
// default branch: a Policy.Validate would reject an invalid Operator, so the
// only way to reach it is a direct call. An unknown operator must be fail-closed.
func TestMatchCondition_UnknownOperator_FailsClosed(t *testing.T) {
	r := attributeResolver{principal: userPrincipal(map[string]string{"department": "eng"})}
	c := abac.Condition{Source: abac.SourceSubject, Key: "department", Operator: abac.Operator(99), Values: []string{"eng"}}
	assert.False(t, matchCondition(c, r), "unknown operator must be fail-closed (condition unsatisfied)")
}

// TestMergeObligations_SkipsZeroRowScope_UnionsFieldMask covers the zero-RowScope
// skip + FieldMask union/dedup in mergeObligations.
func TestMergeObligations_SkipsZeroRowScope_UnionsFieldMask(t *testing.T) {
	merged := mergeObligations([]authz.Obligations{
		{RowScope: 0, FieldMask: authz.FieldMask{Fields: []string{"a"}}},
		{RowScope: tenant.RowScopeTenant, FieldMask: authz.FieldMask{Fields: []string{"b", "a"}}},
		{RowScope: tenant.RowScopeSelf},
	})
	assert.Equal(t, tenant.RowScopeSelf, merged.RowScope, "zero RowScope skipped; narrowest non-zero wins")
	assert.ElementsMatch(t, []string{"a", "b"}, merged.FieldMask.Fields, "FieldMask is the deduped union")
}
