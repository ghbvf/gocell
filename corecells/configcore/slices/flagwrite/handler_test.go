package flagwrite

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/corecells/configcore/configcoretest"
	"github.com/ghbvf/gocell/corecells/configcore/internal/domain"
	"github.com/ghbvf/gocell/corecells/configcore/internal/mem"
	"github.com/ghbvf/gocell/corecells/configcore/internal/testutil"
	kcell "github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/cell/celltest"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/errcode/errcodetest"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	create "github.com/ghbvf/gocell/generated/contracts/http/config/flags/create/v1"
	flagsdelete "github.com/ghbvf/gocell/generated/contracts/http/config/flags/delete/v1"
	toggle "github.com/ghbvf/gocell/generated/contracts/http/config/flags/toggle/v1"
	update "github.com/ghbvf/gocell/generated/contracts/http/config/flags/update/v1"
)

// allowAuthorizer / withAllowAuthorizer / withDenyAuthorizer build the PDP
// verdicts for tests. The authz.Allow/Deny construction lives in _test.go,
// which AUTHZ-DECISION-ALLOW-DENY-CALLER-01 sanctions for test doubles; the
// CapturingAuthorizer type and WithAuthorizer ctx wiring are shared via
// configcoretest (PR-10b #1348).
func allowAuthorizer() *configcoretest.CapturingAuthorizer {
	dec, err := authz.Allow(authz.Obligations{})
	if err != nil {
		panic("test allowAuthorizer: authz.Allow: " + err.Error())
	}
	return &configcoretest.CapturingAuthorizer{Decision: dec}
}

func withAllowAuthorizer(ctx context.Context) context.Context {
	return configcoretest.WithAuthorizer(ctx, allowAuthorizer())
}

func withDenyAuthorizer(ctx context.Context, reason string) context.Context {
	return configcoretest.WithAuthorizer(ctx, &configcoretest.CapturingAuthorizer{Decision: authz.Deny(reason)})
}

// PR464 P2.2: typed 404 / 409 envelope adapter regression coverage.
// fakeFlagRepoErr wraps mem.FlagRepository and overrides Update/Toggle/Delete
// to inject controlled errcode.Error responses, asserting each adapter's
// errors.As + ce.Code switch returns the typed envelope (not framework
// fallback) so codegen-declared status codes stay locked.

const testFlagwriteAdmin = "admin-test"

type stubFlagTxRunner struct{}

func (stubFlagTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

type fakeFlagRepoErr struct {
	*mem.FlagRepository
	updateErr error
	toggleErr error
	deleteErr error
}

func (f *fakeFlagRepoErr) Update(
	_ context.Context, _ tenant.TenantID, _ string, _ int, _ bool, _ int, _ string,
) (*domain.FeatureFlag, error) {
	return nil, f.updateErr
}

func (f *fakeFlagRepoErr) Toggle(_ context.Context, _ tenant.TenantID, _ string, _ int, _ bool) (*domain.FeatureFlag, error) {
	return nil, f.toggleErr
}

func (f *fakeFlagRepoErr) Delete(_ context.Context, _ tenant.TenantID, _ string, _ int) (*domain.FeatureFlag, error) {
	return nil, f.deleteErr
}

func adminCtx() context.Context {
	return configcoretest.CtxWithTenant(auth.TestContext(testFlagwriteAdmin, []string{auth.RoleAdmin}))
}

func newFlagAdapters(t *testing.T, updateErr, toggleErr, deleteErr error) (UpdateAdapter, ToggleAdapter, FlagDeleteAdapter) {
	t.Helper()
	repo := &fakeFlagRepoErr{
		FlagRepository: mem.NewFlagRepository(clock.Real()),
		updateErr:      updateErr,
		toggleErr:      toggleErr,
		deleteErr:      deleteErr,
	}
	svc, err := NewService(clock.Real(), repo, slog.Default(), WithTxManager(persistence.WrapForCell(stubFlagTxRunner{})))
	require.NoError(t, err)
	return UpdateAdapter{S: svc}, ToggleAdapter{S: svc}, FlagDeleteAdapter{S: svc}
}

// --- Update typed envelope ---

func TestFlagUpdateAdapter_NotFound_Returns404Typed(t *testing.T) {
	updateAd, _, _ := newFlagAdapters(t,
		errcode.New(errcode.KindNotFound, errcode.ErrFlagNotFound, "flag not found"),
		nil, nil)
	resp, err := updateAd.Update(adminCtx(), &update.Request{
		Key: "missing", Enabled: true, RolloutPercentage: 100, Description: "d", ExpectedVersion: 1,
	})
	require.NoError(t, err)
	typed, ok := resp.(update.Update404ErrorResponse)
	require.True(t, ok, "expected Update404ErrorResponse, got %T", resp)
	assert.Equal(t, errcode.ErrFlagNotFound, typed.Body.Code)
}

func TestFlagUpdateAdapter_VersionConflict_Returns409Typed(t *testing.T) {
	updateAd, _, _ := newFlagAdapters(t,
		errcode.New(errcode.KindConflict, errcode.ErrVersionConflict, "concurrent update detected; reload and retry"),
		nil, nil)
	resp, err := updateAd.Update(adminCtx(), &update.Request{
		Key: "stale", Enabled: true, RolloutPercentage: 100, Description: "d", ExpectedVersion: 1,
	})
	require.NoError(t, err)
	typed, ok := resp.(update.Update409ErrorResponse)
	require.True(t, ok, "expected Update409ErrorResponse, got %T", resp)
	assert.Equal(t, errcode.ErrVersionConflict, typed.Body.Code)
}

// --- Toggle typed envelope ---

func TestFlagToggleAdapter_NotFound_Returns404Typed(t *testing.T) {
	_, toggleAd, _ := newFlagAdapters(t, nil,
		errcode.New(errcode.KindNotFound, errcode.ErrFlagNotFound, "flag not found"), nil)
	resp, err := toggleAd.Toggle(adminCtx(), &toggle.Request{
		Key: "missing", Enabled: true, ExpectedVersion: 1,
	})
	require.NoError(t, err)
	typed, ok := resp.(toggle.Toggle404ErrorResponse)
	require.True(t, ok, "expected Toggle404ErrorResponse, got %T", resp)
	assert.Equal(t, errcode.ErrFlagNotFound, typed.Body.Code)
}

func TestFlagToggleAdapter_VersionConflict_Returns409Typed(t *testing.T) {
	_, toggleAd, _ := newFlagAdapters(t, nil,
		errcode.New(errcode.KindConflict, errcode.ErrVersionConflict, "concurrent update detected; reload and retry"), nil)
	resp, err := toggleAd.Toggle(adminCtx(), &toggle.Request{
		Key: "stale", Enabled: true, ExpectedVersion: 1,
	})
	require.NoError(t, err)
	typed, ok := resp.(toggle.Toggle409ErrorResponse)
	require.True(t, ok, "expected Toggle409ErrorResponse, got %T", resp)
	assert.Equal(t, errcode.ErrVersionConflict, typed.Body.Code)
}

// --- Delete typed envelope ---

func TestFlagDeleteAdapter_NotFound_Returns404Typed(t *testing.T) {
	_, _, deleteAd := newFlagAdapters(t, nil, nil,
		errcode.New(errcode.KindNotFound, errcode.ErrFlagNotFound, "flag not found"))
	resp, err := deleteAd.Delete(adminCtx(), &flagsdelete.Request{Key: "missing", ExpectedVersion: 1})
	require.NoError(t, err)
	typed, ok := resp.(flagsdelete.Delete404ErrorResponse)
	require.True(t, ok, "expected Delete404ErrorResponse, got %T", resp)
	assert.Equal(t, errcode.ErrFlagNotFound, typed.Body.Code)
}

func TestFlagDeleteAdapter_VersionConflict_Returns409Typed(t *testing.T) {
	_, _, deleteAd := newFlagAdapters(t, nil, nil,
		errcode.New(errcode.KindConflict, errcode.ErrVersionConflict, "concurrent update detected; reload and retry"))
	resp, err := deleteAd.Delete(adminCtx(), &flagsdelete.Request{Key: "stale", ExpectedVersion: 1})
	require.NoError(t, err)
	typed, ok := resp.(flagsdelete.Delete409ErrorResponse)
	require.True(t, ok, "expected Delete409ErrorResponse, got %T", resp)
	assert.Equal(t, errcode.ErrVersionConflict, typed.Body.Code)
}

// --- F6: missing-tenant → typed 403 (not 500) ---

// adminCtxNoTenant carries an admin Principal but NO TenantID, so
// Service.tenant.FromContext fails and the adapter must map it to a typed 403.
func adminCtxNoTenant() context.Context {
	return auth.TestContext(testFlagwriteAdmin, []string{auth.RoleAdmin})
}

func newCreateAdapter(t *testing.T) CreateAdapter {
	t.Helper()
	svc, err := NewService(clock.Real(), mem.NewFlagRepository(clock.Real()), slog.Default(),
		WithTxManager(persistence.WrapForCell(stubFlagTxRunner{})))
	require.NoError(t, err)
	return CreateAdapter{S: svc}
}

func TestFlagCreateAdapter_MissingTenant_Returns403Typed(t *testing.T) {
	createAd := newCreateAdapter(t)
	enabled := true
	resp, err := createAd.Create(adminCtxNoTenant(), &create.Request{
		Key: "dark-mode", Enabled: &enabled, RolloutPercentage: 100, Description: "d",
	})
	require.NoError(t, err)
	typed, ok := resp.(create.Create403ErrorResponse)
	require.True(t, ok, "expected Create403ErrorResponse, got %T", resp)
	assert.Equal(t, errcode.ErrAuthForbidden, typed.Body.Code)
}

func TestFlagUpdateAdapter_MissingTenant_Returns403Typed(t *testing.T) {
	updateAd, _, _ := newFlagAdapters(t, nil, nil, nil)
	resp, err := updateAd.Update(adminCtxNoTenant(), &update.Request{
		Key: "dark-mode", Enabled: true, RolloutPercentage: 100, Description: "d", ExpectedVersion: 1,
	})
	require.NoError(t, err)
	typed, ok := resp.(update.Update403ErrorResponse)
	require.True(t, ok, "expected Update403ErrorResponse, got %T", resp)
	assert.Equal(t, errcode.ErrAuthForbidden, typed.Body.Code)
}

// --- HTTP-level PDP-based authz tests (flag:write-gated (PDP)) ---

const flagwriteBasePath = "/api/v1/flags"

// testFlagWriteResolver mirrors the cellHTTPResolver for flagwrite handler tests.
var testFlagWriteResolver = auth.NewStaticMethodPolicyResolver(map[string]string{
	"http.config.flags.create.v1": "flag:write",
	"http.config.flags.update.v1": "flag:write",
	"http.config.flags.toggle.v1": "flag:write",
	"http.config.flags.delete.v1": "flag:write",
})

// setupFlagwriteHTTPHandler builds an HTTP handler for flagwrite HTTP-level tests.
// It uses NewHandler with the contract-derived resolver (#2205) — the same
// construction path as production.
func setupFlagwriteHTTPHandler(t *testing.T) http.Handler {
	t.Helper()
	repo := mem.NewFlagRepository(clock.Real())
	svc, err := NewService(clock.Real(), repo, slog.Default(),
		WithTxManager(persistence.WrapForCell(&testutil.NoopTxRunner{})))
	if err != nil {
		t.Fatalf("setupFlagwriteHTTPHandler: %v", err)
	}
	mux := celltest.NewTestMux()
	mux.Route(flagwriteBasePath, func(sub kcell.RouteMux) {
		if err := NewHandler(svc, testFlagWriteResolver).RegisterRoutes(sub); err != nil {
			t.Fatalf("RegisterRoutes: %v", err)
		}
	})
	return mux
}

// TestFlagwriteHandler_PDPDeny_Returns403 asserts that a PDP-deny Authorizer in
// ctx surfaces as 403 ERR_AUTH_FORBIDDEN on every flagwrite endpoint — confirms
// flag:write-gated (PDP) behavior after the role-literal → permission-based
// migration (PR-10b #1348).
func TestFlagwriteHandler_PDPDeny_Returns403(t *testing.T) {
	handler := setupFlagwriteHTTPHandler(t)

	asDenyFlagwrite := func(req *http.Request) *http.Request {
		ctx := withDenyAuthorizer(
			configcoretest.CtxWithTenant(auth.TestContext(testFlagwriteAdmin, []string{auth.RoleAdmin})),
			"policy deny",
		)
		return req.WithContext(ctx)
	}

	tests := []struct {
		name string
		req  *http.Request
	}{
		{"create deny", func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, flagwriteBasePath+"/",
				strings.NewReader(`{"key":"k","enabled":false,"rolloutPercentage":0,"description":"d"}`))
			r.Header.Set("Content-Type", "application/json")
			return r
		}()},
		{"update deny", func() *http.Request {
			r := httptest.NewRequest(http.MethodPut, flagwriteBasePath+"/k",
				strings.NewReader(`{"enabled":true,"rolloutPercentage":50,"description":"d","expectedVersion":1}`))
			r.Header.Set("Content-Type", "application/json")
			return r
		}()},
		{"toggle deny", func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, flagwriteBasePath+"/k/toggle",
				strings.NewReader(`{"enabled":true,"expectedVersion":1}`))
			r.Header.Set("Content-Type", "application/json")
			return r
		}()},
		{"delete deny", httptest.NewRequest(http.MethodDelete, flagwriteBasePath+"/k?expectedVersion=1", http.NoBody)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, asDenyFlagwrite(tc.req))
			errcodetest.AssertWireCode(t, w, http.StatusForbidden, errcode.ErrAuthForbidden)
		})
	}
}

// TestFlagwriteHandler_NoAuthorizer_FailClosed_Returns403 asserts that a
// request with a valid principal but no Authorizer in ctx is fail-closed to 403
// — required by the flag:write-gated (PDP) contract: no PDP wired = deny.
func TestFlagwriteHandler_NoAuthorizer_FailClosed_Returns403(t *testing.T) {
	handler := setupFlagwriteHTTPHandler(t)

	asAdminNoPDP := func(req *http.Request) *http.Request {
		// Principal + tenant present, but no Authorizer injected.
		ctx := configcoretest.CtxWithTenant(auth.TestContext(testFlagwriteAdmin, []string{auth.RoleAdmin}))
		return req.WithContext(ctx)
	}

	tests := []struct {
		name string
		req  *http.Request
	}{
		{"create no-pdp", func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, flagwriteBasePath+"/",
				strings.NewReader(`{"key":"k","enabled":false,"rolloutPercentage":0,"description":"d"}`))
			r.Header.Set("Content-Type", "application/json")
			return r
		}()},
		{"update no-pdp", func() *http.Request {
			r := httptest.NewRequest(http.MethodPut, flagwriteBasePath+"/k",
				strings.NewReader(`{"enabled":true,"rolloutPercentage":50,"description":"d","expectedVersion":1}`))
			r.Header.Set("Content-Type", "application/json")
			return r
		}()},
		{"toggle no-pdp", func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, flagwriteBasePath+"/k/toggle",
				strings.NewReader(`{"enabled":true,"expectedVersion":1}`))
			r.Header.Set("Content-Type", "application/json")
			return r
		}()},
		{"delete no-pdp", httptest.NewRequest(http.MethodDelete, flagwriteBasePath+"/k?expectedVersion=1", http.NoBody)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, asAdminNoPDP(tc.req))
			errcodetest.AssertWireCode(t, w, http.StatusForbidden, errcode.ErrAuthForbidden)
		})
	}
}

// TestFlagwriteHandler_ActionPin_FlagWrite pins that every flagwrite endpoint
// sends action "flag:write" to the PDP. This is the only guard that catches an
// endpoint↔permission misbinding the role-agnostic baseline (admin allowed for
// every flag perm) would mask (PR-10b #1348).
func TestFlagwriteHandler_ActionPin_FlagWrite(t *testing.T) {
	repo := mem.NewFlagRepository(clock.Real())
	svc, err := NewService(clock.Real(), repo, slog.Default(),
		WithTxManager(persistence.WrapForCell(&testutil.NoopTxRunner{})))
	require.NoError(t, err)

	// Seed a flag for update/toggle/delete endpoints.
	_, err = svc.Create(
		withAllowAuthorizer(configcoretest.CtxWithTenant(auth.TestContext(testFlagwriteAdmin, []string{auth.RoleAdmin}))),
		CreateInput{Key: "pin-flag", Description: "action-pin seed"},
	)
	require.NoError(t, err)

	mux := celltest.NewTestMux()
	mux.Route(flagwriteBasePath, func(sub kcell.RouteMux) {
		if err := NewHandler(svc, testFlagWriteResolver).RegisterRoutes(sub); err != nil {
			t.Fatalf("RegisterRoutes: %v", err)
		}
	})

	endpoints := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{
			"create", http.MethodPost, flagwriteBasePath + "/",
			`{"key":"new-flag","enabled":false,"rolloutPercentage":0,"description":"d"}`,
		},
		{
			"update", http.MethodPut, flagwriteBasePath + "/pin-flag",
			`{"enabled":true,"rolloutPercentage":50,"description":"d","expectedVersion":1}`,
		},
		{
			"toggle", http.MethodPost, flagwriteBasePath + "/pin-flag/toggle",
			`{"enabled":true,"expectedVersion":1}`,
		},
		{"delete", http.MethodDelete, flagwriteBasePath + "/pin-flag?expectedVersion=1", ""},
	}

	for _, ep := range endpoints {
		t.Run(ep.name, func(t *testing.T) {
			cap := allowAuthorizer()
			ctx := configcoretest.WithAuthorizer(
				configcoretest.CtxWithTenant(auth.TestContext(testFlagwriteAdmin, []string{auth.RoleAdmin})),
				cap,
			)
			var reqBody *strings.Reader
			if ep.body != "" {
				reqBody = strings.NewReader(ep.body)
			} else {
				reqBody = strings.NewReader("")
			}
			req := httptest.NewRequest(ep.method, ep.path, reqBody)
			if ep.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			req = req.WithContext(ctx)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			// HTTP response code pre-assertion: gate must pass (not 401/403) and the
			// endpoint must be routed (not 404). Mirrors configread/configpublish action-pin style.
			require.NotEqual(t, http.StatusUnauthorized, w.Code,
				"flagwrite gate must not block admin with allow Authorizer (endpoint=%s)", ep.name)
			require.NotEqual(t, http.StatusForbidden, w.Code,
				"flagwrite gate must not deny admin with allow Authorizer (endpoint=%s)", ep.name)
			require.NotEqual(t, http.StatusNotFound, w.Code,
				"flagwrite endpoint must be routed (endpoint=%s)", ep.name)
			assert.Equal(t, "flag:write", cap.GotAction,
				"flagwrite gate must request action flag:write to PDP; endpoint %s got %q — "+
					"a misbinding to a different perm would be masked by baseline allow-all-admin",
				ep.name, cap.GotAction)
		})
	}
}
