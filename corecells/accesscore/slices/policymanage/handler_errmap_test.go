package policymanage

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/corecells/internal/testoutbox"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	policyCreate "github.com/ghbvf/gocell/generated/contracts/http/policy/create/v1"
	policyDelete "github.com/ghbvf/gocell/generated/contracts/http/policy/delete/v1"
	policyGet "github.com/ghbvf/gocell/generated/contracts/http/policy/get/v1"
	policyList "github.com/ghbvf/gocell/generated/contracts/http/policy/list/v1"
	policyUpdate "github.com/ghbvf/gocell/generated/contracts/http/policy/update/v1"
)

const (
	testErrMapTenantStr = "40000000-0000-0000-0000-000000000001"
	testErrMapSubject   = "errmap-admin"
)

func testErrMapCtx() context.Context {
	return ctxkeys.WithTenantID(
		auth.TestContext(testErrMapSubject, []string{auth.RoleAdmin}),
		testErrMapTenantStr,
	)
}

// fixedKindRepo is a PolicyRepository stub that returns a fixed errcode.Kind
// on every mutating call (Create/Update/Delete) and Get.
type fixedKindRepo struct {
	kind errcode.Kind
	code errcode.Code
	msg  string
}

func newFixedKindRepo(kind errcode.Kind) *fixedKindRepo {
	return &fixedKindRepo{
		kind: kind,
		code: errcode.ErrValidationFailed,
		msg:  "policymanage: fixed-kind stub error",
	}
}

func (r *fixedKindRepo) Create(_ context.Context, _ tenant.TenantID, _ *abac.Policy) (*abac.Policy, error) {
	return nil, errcode.New(r.kind, r.code, r.msg)
}

func (r *fixedKindRepo) Update(_ context.Context, _ tenant.TenantID, _ string, _ int, _ *abac.Policy) (*abac.Policy, error) {
	return nil, errcode.New(r.kind, r.code, r.msg)
}

func (r *fixedKindRepo) Delete(_ context.Context, _ tenant.TenantID, _ string, _ int) (*abac.Policy, error) {
	return nil, errcode.New(r.kind, r.code, r.msg)
}

func (r *fixedKindRepo) GetByID(_ context.Context, _ tenant.TenantID, _ string) (*abac.Policy, error) {
	return nil, errcode.New(r.kind, r.code, r.msg)
}

func (r *fixedKindRepo) ListByTenant(_ context.Context, _ tenant.TenantID) ([]*abac.Policy, error) {
	return nil, errcode.New(r.kind, r.code, r.msg)
}

func (r *fixedKindRepo) RepoReady(_ context.Context) error { return nil }

// newSvcWithRepo builds a Service backed by the provided repo and a noop emitter.
func newSvcWithRepo(t testing.TB, repo fixedKindRepoIface) *Service {
	t.Helper()
	writer := &recordingWriter{}
	svc, err := NewService(clock.Real(), repo, testCursorCodec, slog.Default(), query.RunModeProd,
		WithEmitter(outbox.WrapEmitterForCell(testoutbox.MustEmitter(t, writer))),
		WithTxManager(persistence.WrapForCell(&stubPolicyTxRunner{})))
	require.NoError(t, err)
	return svc
}

// fixedKindRepoIface is the PolicyRepository interface the service depends on
// (alias avoids importing the ports package directly in this test file).
type fixedKindRepoIface = interface {
	Create(context.Context, tenant.TenantID, *abac.Policy) (*abac.Policy, error)
	Update(context.Context, tenant.TenantID, string, int, *abac.Policy) (*abac.Policy, error)
	Delete(context.Context, tenant.TenantID, string, int) (*abac.Policy, error)
	GetByID(context.Context, tenant.TenantID, string) (*abac.Policy, error)
	ListByTenant(context.Context, tenant.TenantID) ([]*abac.Policy, error)
	RepoReady(context.Context) error
}

// --- mapCreateError coverage ---

// TestMapCreateError_Default exercises the default branch (400) in mapCreateError
// via a repo that returns KindInternal (not in any explicit case).
func TestMapCreateError_Default(t *testing.T) {
	ctx := testErrMapCtx()
	svc := newSvcWithRepo(t, newFixedKindRepo(errcode.KindInternal))
	ad := CreateAdapter{s: svc}

	resp, err := ad.Create(ctx, &policyCreate.Request{
		Name:  "P",
		Rules: []*policyCreate.RequestRulesItem{{ID: "r1", Name: "N", Effect: "allow"}},
	})
	require.NoError(t, err)
	_, ok := resp.(policyCreate.Create400ErrorResponse)
	assert.True(t, ok, "KindInternal must fall through to Create400ErrorResponse, got %T", resp)
}

// TestMapCreateError_Unauthenticated exercises the KindUnauthenticated → 401 branch.
func TestMapCreateError_Unauthenticated(t *testing.T) {
	ctx := testErrMapCtx()
	svc := newSvcWithRepo(t, newFixedKindRepo(errcode.KindUnauthenticated))
	ad := CreateAdapter{s: svc}

	resp, err := ad.Create(ctx, &policyCreate.Request{
		Name:  "P",
		Rules: []*policyCreate.RequestRulesItem{{ID: "r1", Name: "N", Effect: "allow"}},
	})
	require.NoError(t, err)
	_, ok := resp.(policyCreate.Create401ErrorResponse)
	assert.True(t, ok, "KindUnauthenticated must yield Create401ErrorResponse, got %T", resp)
}

// --- mapGetError coverage ---

// TestMapGetError_Unauthenticated exercises the KindUnauthenticated → 401 branch.
func TestMapGetError_Unauthenticated(t *testing.T) {
	ctx := testErrMapCtx()
	svc := newSvcWithRepo(t, newFixedKindRepo(errcode.KindUnauthenticated))
	ad := GetAdapter{s: svc}

	resp, err := ad.Get(ctx, &policyGet.Request{ID: "pol-x"})
	require.NoError(t, err)
	_, ok := resp.(policyGet.Get401ErrorResponse)
	assert.True(t, ok, "KindUnauthenticated must yield Get401ErrorResponse, got %T", resp)
}

// TestMapGetError_Default exercises the default branch (400) via KindInternal.
func TestMapGetError_Default(t *testing.T) {
	ctx := testErrMapCtx()
	svc := newSvcWithRepo(t, newFixedKindRepo(errcode.KindInternal))
	ad := GetAdapter{s: svc}

	resp, err := ad.Get(ctx, &policyGet.Request{ID: "pol-x"})
	require.NoError(t, err)
	_, ok := resp.(policyGet.Get400ErrorResponse)
	assert.True(t, ok, "KindInternal must fall through to Get400ErrorResponse, got %T", resp)
}

// --- mapUpdateError coverage ---

// TestMapUpdateError_Unauthenticated exercises KindUnauthenticated → 401.
func TestMapUpdateError_Unauthenticated(t *testing.T) {
	ctx := testErrMapCtx()
	svc := newSvcWithRepo(t, newFixedKindRepo(errcode.KindUnauthenticated))
	ad := UpdateAdapter{s: svc}

	resp, err := ad.Update(ctx, &policyUpdate.Request{
		ID:              "pol-x",
		Name:            "X",
		Rules:           []*policyUpdate.RequestRulesItem{{ID: "r1", Name: "N", Effect: "allow"}},
		ExpectedVersion: 1,
	})
	require.NoError(t, err)
	_, ok := resp.(policyUpdate.Update401ErrorResponse)
	assert.True(t, ok, "KindUnauthenticated must yield Update401ErrorResponse, got %T", resp)
}

// TestMapUpdateError_Default exercises the default (400) branch via KindInternal.
func TestMapUpdateError_Default(t *testing.T) {
	ctx := testErrMapCtx()
	svc := newSvcWithRepo(t, newFixedKindRepo(errcode.KindInternal))
	ad := UpdateAdapter{s: svc}

	resp, err := ad.Update(ctx, &policyUpdate.Request{
		ID:              "pol-x",
		Name:            "X",
		Rules:           []*policyUpdate.RequestRulesItem{{ID: "r1", Name: "N", Effect: "allow"}},
		ExpectedVersion: 1,
	})
	require.NoError(t, err)
	_, ok := resp.(policyUpdate.Update400ErrorResponse)
	assert.True(t, ok, "KindInternal must fall through to Update400ErrorResponse, got %T", resp)
}

// --- mapDeleteError coverage ---

// TestMapDeleteError_Unauthenticated exercises KindUnauthenticated → 401.
func TestMapDeleteError_Unauthenticated(t *testing.T) {
	ctx := testErrMapCtx()
	svc := newSvcWithRepo(t, newFixedKindRepo(errcode.KindUnauthenticated))
	ad := DeleteAdapter{s: svc}

	resp, err := ad.Delete(ctx, &policyDelete.Request{ID: "pol-x", ExpectedVersion: 1})
	require.NoError(t, err)
	_, ok := resp.(policyDelete.Delete401ErrorResponse)
	assert.True(t, ok, "KindUnauthenticated must yield Delete401ErrorResponse, got %T", resp)
}

// TestMapDeleteError_Default exercises the default (400) branch via KindInternal.
func TestMapDeleteError_Default(t *testing.T) {
	ctx := testErrMapCtx()
	svc := newSvcWithRepo(t, newFixedKindRepo(errcode.KindInternal))
	ad := DeleteAdapter{s: svc}

	resp, err := ad.Delete(ctx, &policyDelete.Request{ID: "pol-x", ExpectedVersion: 1})
	require.NoError(t, err)
	_, ok := resp.(policyDelete.Delete400ErrorResponse)
	assert.True(t, ok, "KindInternal must fall through to Delete400ErrorResponse, got %T", resp)
}

// --- mapListError coverage ---

// TestMapListError_Unauthenticated exercises KindUnauthenticated → 401.
func TestMapListError_Unauthenticated(t *testing.T) {
	ctx := testErrMapCtx()
	svc := newSvcWithRepo(t, newFixedKindRepo(errcode.KindUnauthenticated))
	ad := ListAdapter{s: svc}

	resp, err := ad.List(ctx, &policyList.Request{Limit: 10})
	require.NoError(t, err)
	_, ok := resp.(policyList.List401ErrorResponse)
	assert.True(t, ok, "KindUnauthenticated must yield List401ErrorResponse, got %T", resp)
}

// TestMapListError_Default exercises the default (400) branch via KindInternal.
func TestMapListError_Default(t *testing.T) {
	ctx := testErrMapCtx()
	svc := newSvcWithRepo(t, newFixedKindRepo(errcode.KindInternal))
	ad := ListAdapter{s: svc}

	resp, err := ad.List(ctx, &policyList.Request{Limit: 10})
	require.NoError(t, err)
	_, ok := resp.(policyList.List400ErrorResponse)
	assert.True(t, ok, "KindInternal must fall through to List400ErrorResponse, got %T", resp)
}
