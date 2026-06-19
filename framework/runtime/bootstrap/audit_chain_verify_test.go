package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kauth "github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/audit"
)

// fakeChainVerifier is a test double for the unexported chainVerifier so the
// handler can be exercised without an admin pool. The real implementer is
// *audit.ChainVerifier (tested separately in runtime/audit).
type fakeChainVerifier struct {
	report audit.ChainVerifyReport
	err    error
	calls  int
}

func (f *fakeChainVerifier) VerifyAll(context.Context) (audit.ChainVerifyReport, error) {
	f.calls++
	return f.report, f.err
}

var _ chainVerifier = (*fakeChainVerifier)(nil)

// fakeVerifyDuration is a fixed report duration for the 200-response body assertion
// (TEST-TIME-LITERAL-01: durations are package-level consts, not inline literals).
const fakeVerifyDuration = 150 * time.Millisecond

// mountAuditChainVerify wires b's verify RouteGroup onto a fresh ServeMux via the
// real production auditChainVerifyRouteGroup().Register (reusing serveMuxRouteMux
// from projection_rebuild_test.go), asserting it targets the AdminListener.
func mountAuditChainVerify(t *testing.T, b *Bootstrap) *http.ServeMux {
	t.Helper()
	rg := b.auditChainVerifyRouteGroup()
	require.Equal(t, cell.AdminListener, rg.Listener, "verify endpoint must mount on the AdminListener")
	mux := http.NewServeMux()
	require.NoError(t, rg.Register(serveMuxRouteMux{mux}))
	return mux
}

// auditVerifyBootstrap returns a Bootstrap with the verify endpoint opted in and
// the given verifier wired.
func auditVerifyBootstrap(v chainVerifier) *Bootstrap {
	b := New(clock.Real(), WithAuditChainVerifyEndpoint())
	b.auditChainVerifier = v
	return b
}

func doVerify(mux *http.ServeMux) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/admin/v1/audit/chains/verify", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestAuditChainVerify_200_AllValid(t *testing.T) {
	t.Parallel()
	fake := &fakeChainVerifier{report: audit.ChainVerifyReport{
		TotalChains: 2,
		Duration:    fakeVerifyDuration,
		Results: []audit.ChainVerifyResult{
			{Namespace: "auditcore", TenantID: "t-a", TailSeq: 3, Valid: true, FirstInvalidSeq: -1},
			{Namespace: "bootstrap", TenantID: "", TailSeq: 2, Valid: true, FirstInvalidSeq: -1},
		},
	}}
	mux := mountAuditChainVerify(t, auditVerifyBootstrap(fake))

	rec := doVerify(mux)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 1, fake.calls)
	var got auditChainVerifyResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.True(t, got.Data.AllValid)
	assert.Equal(t, 2, got.Data.TotalChains)
	assert.Equal(t, int64(150), got.Data.DurationMs)
	assert.Empty(t, got.Data.Failures, "valid chains must not appear in failures")
}

func TestAuditChainVerify_200_WithFailures(t *testing.T) {
	t.Parallel()
	fake := &fakeChainVerifier{report: audit.ChainVerifyReport{
		TotalChains:   3,
		InvalidChains: 1,
		ErroredChains: 1,
		Results: []audit.ChainVerifyResult{
			{Namespace: "auditcore", TenantID: "t-ok", TailSeq: 2, Valid: true, FirstInvalidSeq: -1},
			{Namespace: "auditcore", TenantID: "t-bad", TailSeq: 4, Valid: false, FirstInvalidSeq: 2},
			{Namespace: "bootstrap", TenantID: "t-err", TailSeq: 1, Valid: false, FirstInvalidSeq: -1, Err: errors.New("infra")},
		},
	}}
	mux := mountAuditChainVerify(t, auditVerifyBootstrap(fake))

	rec := doVerify(mux)

	require.Equal(t, http.StatusOK, rec.Code, "a completed run with tampered chains is still 200")
	var got auditChainVerifyResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.False(t, got.Data.AllValid)
	assert.Equal(t, 1, got.Data.InvalidChains)
	assert.Equal(t, 1, got.Data.ErroredChains)
	require.Len(t, got.Data.Failures, 2, "only the invalid + errored chains are listed")

	byTenant := map[string]auditChainFailure{}
	for _, f := range got.Data.Failures {
		byTenant[f.TenantID] = f
	}
	assert.Equal(t, int64(2), byTenant["t-bad"].FirstInvalidSeq)
	assert.False(t, byTenant["t-bad"].Errored)
	assert.True(t, byTenant["t-err"].Errored, "the errored chain must carry errored=true")
	// The infra error text must NOT leak onto the wire (server-log only).
	assert.NotContains(t, rec.Body.String(), "infra")
}

func TestAuditChainVerify_500_OnVerifierError(t *testing.T) {
	t.Parallel()
	fake := &fakeChainVerifier{err: errcode.New(errcode.KindInternal, errcode.ErrInternal,
		"audit chain verify: enumerate chains failed")}
	mux := mountAuditChainVerify(t, auditVerifyBootstrap(fake))

	rec := doVerify(mux)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

// TestValidateAuditChainVerifyEndpoint covers the phase0 fail-fast: enabling the
// endpoint requires BOTH an AdminListener and an injected verifier; not opting in
// is a no-op.
func TestValidateAuditChainVerifyEndpoint(t *testing.T) {
	t.Parallel()

	t.Run("not opted in → ok", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, New(clock.Real()).validateAuditChainVerifyEndpoint())
	})

	t.Run("opted in without AdminListener → error", func(t *testing.T) {
		t.Parallel()
		b := New(clock.Real(), WithAuditChainVerifyEndpoint())
		b.auditChainVerifier = &fakeChainVerifier{}
		err := b.validateAuditChainVerifyEndpoint()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "AdminListener")
	})

	t.Run("opted in with AdminListener but no verifier → error", func(t *testing.T) {
		t.Parallel()
		b := New(clock.Real(),
			WithListener(cell.AdminListener, "127.0.0.1:0", []kauth.ListenerAuth{newTestOperatorAuth(t)}),
			WithAuditChainVerifyEndpoint())
		err := b.validateAuditChainVerifyEndpoint()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "verifier")
	})

	t.Run("opted in with AdminListener and verifier → ok", func(t *testing.T) {
		t.Parallel()
		b := New(clock.Real(),
			WithListener(cell.AdminListener, "127.0.0.1:0", []kauth.ListenerAuth{newTestOperatorAuth(t)}),
			WithAuditChainVerifyEndpoint())
		b.auditChainVerifier = &fakeChainVerifier{}
		require.NoError(t, b.validateAuditChainVerifyEndpoint())
	})
}

// TestAuditChainVerify_BehindAuthOperatorGate composes the real verify handler
// behind the real AdminListener operator gate, exactly as phase5 wires them: no
// credentials → 401 (handler never runs); correct operator credentials → 200.
func TestAuditChainVerify_BehindAuthOperatorGate(t *testing.T) {
	t.Parallel()
	fake := &fakeChainVerifier{report: audit.ChainVerifyReport{TotalChains: 0}}
	b := auditVerifyBootstrap(fake)
	mux := mountAuditChainVerify(t, b)

	mws, _, _, err := b.applyListenerAuthChain(cell.AdminListener, []kauth.ListenerAuth{newTestOperatorAuth(t)})
	require.NoError(t, err)
	require.Len(t, mws, 1, "AuthOperator installs exactly one listener middleware")
	guarded := mws[0](mux)

	do := func(setAuth func(*http.Request)) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/admin/v1/audit/chains/verify", nil)
		if setAuth != nil {
			setAuth(req)
		}
		rec := httptest.NewRecorder()
		guarded.ServeHTTP(rec, req)
		return rec
	}

	assert.Equal(t, http.StatusUnauthorized, do(nil).Code, "missing operator credentials → 401")
	assert.Equal(t, 0, fake.calls, "gate must block before the verify handler runs")

	rec := do(func(r *http.Request) { r.SetBasicAuth("ops", "s3cretpwd") })
	require.Equal(t, http.StatusOK, rec.Code, "correct operator credentials → 200")
	assert.Equal(t, 1, fake.calls, "authenticated request reaches the verify handler")
}
