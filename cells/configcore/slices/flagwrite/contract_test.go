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

	"github.com/ghbvf/gocell/cells/configcore/internal/mem"
	"github.com/ghbvf/gocell/cells/configcore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/tests/contracttest"
)

const testAdminSubject = "admin-test"

// newContractMux registers flagwrite routes under the canonical API prefix
// by delegating to Handler.RegisterRoutes — the same single source of truth
// production wiring uses (cells/configcore/cell.go). Inlining policy
// wrappers here would re-open the policy-drift surface the P0 fix closed:
// any change to the required role would land in RegisterRoutes and silently
// desync from contract tests. TestMux.Route mirrors production chi so
// auth.Mount strips the prefix off Contract.Path exactly as production does.
func newContractMux(svc *Service) http.Handler {
	h := NewHandler(svc)
	mux := celltest.NewTestMux()
	mux.Route("/api/v1/flags", func(sub cell.RouteMux) {
		if err := h.RegisterRoutes(sub); err != nil {
			panic("RegisterRoutes: " + err.Error())
		}
	})
	return mux
}

func newContractService(t *testing.T) *Service {
	t.Helper()
	repo := mem.NewFlagRepository(clock.Real())
	svc, err := NewService(repo, slog.Default(), clock.Real(),
		WithTxManager(&testutil.NoopTxRunner{}))
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// --- Create contract test ---

func TestHttpConfigFlagsCreateV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.config.flags.create.v1")
	svc := newContractService(t)
	mux := newContractMux(svc)

	c.ValidateRequest(t, []byte(`{"key":"my-flag","enabled":false,"rolloutPercentage":0,"description":"test"}`))
	c.MustRejectRequest(t, []byte(`{"key":"k","extra":"field"}`))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path,
		strings.NewReader(`{"key":"my-flag","enabled":false,"rolloutPercentage":0,"description":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext(testAdminSubject, []string{auth.RoleAdmin}))
	mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body)
	c.ValidateHTTPResponseRecorder(t, rec)
}

// --- Update contract test ---

func TestHttpConfigFlagsUpdateV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.config.flags.update.v1")
	svc := newContractService(t)

	// Seed a flag first.
	_, err := svc.Create(testAdminCtx(), CreateInput{Key: "upd-flag", Description: "seed"})
	require.NoError(t, err)

	mux := newContractMux(svc)

	c.ValidateRequest(t, []byte(`{"enabled":true,"rolloutPercentage":50,"description":"updated"}`))
	c.MustRejectRequest(t, []byte(`{"enabled":true,"extra":"bad"}`))
	// PUT is full replacement: schema must reject bodies missing any of the
	// three required fields. Catches any future drift that relaxes required
	// constraints in request.schema.json.
	c.MustRejectRequest(t, []byte(`{"enabled":true,"rolloutPercentage":50}`))    // missing description
	c.MustRejectRequest(t, []byte(`{"enabled":true,"description":"x"}`))         // missing rolloutPercentage
	c.MustRejectRequest(t, []byte(`{"rolloutPercentage":50,"description":"x"}`)) // missing enabled

	path := strings.ReplaceAll(c.HTTP.Path, "{key}", "upd-flag")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, path,
		strings.NewReader(`{"enabled":true,"rolloutPercentage":50,"description":"updated"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext(testAdminSubject, []string{auth.RoleAdmin}))
	mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body)
	c.ValidateHTTPResponseRecorder(t, rec)

	// Runtime range guard: rolloutPercentage outside [0, 100] must 400 even
	// when the schema is bypassed. Covers both bounds.
	for _, bad := range []string{
		`{"enabled":true,"rolloutPercentage":-1,"description":"d"}`,
		`{"enabled":true,"rolloutPercentage":101,"description":"d"}`,
	} {
		recBad := httptest.NewRecorder()
		reqBad := httptest.NewRequest(c.HTTP.Method, path, strings.NewReader(bad))
		reqBad.Header.Set("Content-Type", "application/json")
		reqBad = reqBad.WithContext(auth.TestContext(testAdminSubject, []string{auth.RoleAdmin}))
		mux.ServeHTTP(recBad, reqBad)
		assert.Equal(t, http.StatusBadRequest, recBad.Code,
			"PUT with out-of-range rolloutPercentage must 400; body %q got %s",
			bad, recBad.Body)
	}
}

// --- Toggle contract test ---

func TestHttpConfigFlagsToggleV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.config.flags.toggle.v1")
	svc := newContractService(t)

	_, err := svc.Create(testAdminCtx(), CreateInput{Key: "tgl-flag", Description: "seed"})
	require.NoError(t, err)

	mux := newContractMux(svc)

	c.ValidateRequest(t, []byte(`{"enabled":true}`))
	c.MustRejectRequest(t, []byte(`{"enabled":true,"extra":"bad"}`))

	path := strings.ReplaceAll(c.HTTP.Path, "{key}", "tgl-flag")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, path,
		strings.NewReader(`{"enabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext(testAdminSubject, []string{auth.RoleAdmin}))
	mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body)
	c.ValidateHTTPResponseRecorder(t, rec)
}

// --- Delete contract test ---

func TestHttpConfigFlagsDeleteV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.config.flags.delete.v1")
	svc := newContractService(t)

	_, err := svc.Create(testAdminCtx(), CreateInput{Key: "del-flag", Description: "seed"})
	require.NoError(t, err)

	mux := newContractMux(svc)

	path := strings.ReplaceAll(c.HTTP.Path, "{key}", "del-flag")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, path, http.NoBody)
	req = req.WithContext(auth.TestContext(testAdminSubject, []string{auth.RoleAdmin}))
	mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNoContent, rec.Code, "body: %s", rec.Body)
}

func testAdminCtx() context.Context {
	return auth.TestContext(testAdminSubject, []string{auth.RoleAdmin})
}
