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

	"github.com/ghbvf/gocell/cells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/outbox"
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
		require.NoError(t, repo.Save(context.Background(), testTenantID, p))
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
			name:      "nil principal fails closed on subject condition",
			policies:  []*abac.Policy{policyWith("p1", permitRule("r1", authz.Obligations{}, engPermit))},
			principal: nil,
			wantAllow: false,
		},
		{
			name: "device posture: subject.kind==device permits",
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

func (r errPolicyRepo) Save(context.Context, tenant.TenantID, *abac.Policy) error { return r.err }
func (r errPolicyRepo) GetByID(context.Context, tenant.TenantID, string) (*abac.Policy, error) {
	return nil, r.err
}

func (r errPolicyRepo) ListByTenant(context.Context, tenant.TenantID) ([]*abac.Policy, error) {
	return nil, r.err
}
func (r errPolicyRepo) Delete(context.Context, tenant.TenantID, string) error { return r.err }

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

// --- RLS scoping ----------------------------------------------------------

// scopeCapturingPolicyRepo records the tenant scope present when ListByTenant
// runs, proving the policy read is wrapped in a scoped transaction.
type scopeCapturingPolicyRepo struct {
	inner         ports.PolicyRepository
	capturedScope tenant.TenantID
	capturedOK    bool
}

func (r *scopeCapturingPolicyRepo) Save(ctx context.Context, t tenant.TenantID, p *abac.Policy) error {
	return r.inner.Save(ctx, t, p)
}

func (r *scopeCapturingPolicyRepo) GetByID(ctx context.Context, t tenant.TenantID, id string) (*abac.Policy, error) {
	return r.inner.GetByID(ctx, t, id)
}

func (r *scopeCapturingPolicyRepo) ListByTenant(ctx context.Context, t tenant.TenantID) ([]*abac.Policy, error) {
	r.capturedScope, r.capturedOK = tenant.ScopeFromContext(ctx)
	return r.inner.ListByTenant(ctx, t)
}

func (r *scopeCapturingPolicyRepo) Delete(ctx context.Context, t tenant.TenantID, id string) error {
	return r.inner.Delete(ctx, t, id)
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

// TestAuthorizerConformance runs the core decision scenarios against a policy
// store produced by a factory. PR-8 reuses this with the PG store to prove the
// engine behaves identically across policy-store backends.
func TestAuthorizerConformance(t *testing.T) {
	runAuthorizerConformance(t, func() ports.PolicyRepository { return mem.NewPolicyRepository() })
}

func runAuthorizerConformance(t *testing.T, newRepo func() ports.PolicyRepository) {
	t.Helper()
	seed := func(repo ports.PolicyRepository, policies ...*abac.Policy) {
		for _, p := range policies {
			require.NoError(t, repo.Save(context.Background(), testTenantID, p))
		}
	}
	engPermit := cond(abac.SourceSubject, "department", abac.OpEquals, "eng")

	t.Run("permit", func(t *testing.T) {
		repo := newRepo()
		seed(repo, policyWith("p1", permitRule("r1", authz.Obligations{}, engPermit)))
		eng := newEngine(t, repo, clockmock.New(fixedClockTime))
		dec, err := eng.Authorize(reqCtx(userPrincipal(map[string]string{"department": "eng"})), "u", "/x", "read")
		require.NoError(t, err)
		assert.True(t, dec.IsAllow())
	})

	t.Run("forbid-wins", func(t *testing.T) {
		repo := newRepo()
		seed(repo, policyWith("p1", permitRule("a", authz.Obligations{}, engPermit), forbidRule("d", engPermit)))
		eng := newEngine(t, repo, clockmock.New(fixedClockTime))
		dec, err := eng.Authorize(reqCtx(userPrincipal(map[string]string{"department": "eng"})), "u", "/x", "read")
		require.NoError(t, err)
		assert.False(t, dec.IsAllow())
	})

	t.Run("default-deny", func(t *testing.T) {
		repo := newRepo()
		eng := newEngine(t, repo, clockmock.New(fixedClockTime))
		dec, err := eng.Authorize(reqCtx(userPrincipal(map[string]string{"department": "eng"})), "u", "/x", "read")
		require.NoError(t, err)
		assert.False(t, dec.IsAllow())
	})
}
