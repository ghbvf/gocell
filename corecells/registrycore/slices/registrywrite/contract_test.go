package registrywrite

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/corecells/registrycore/internal/mem"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/cell/celltest"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/governance"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
	"github.com/ghbvf/gocell/framework/pkg/httputil"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/tests/contracttest"
)

const contractID = "http.registry.contract.submit.v1"

// contractSubmitResolver mirrors the cellHTTPResolver for registrywrite contract
// tests: the contract-derived resolver maps the submit contract to registry:submit
// so RegisterRoutes installs the same PDP gate production uses (#2205, 303-US7).
var contractSubmitResolver = auth.NewStaticMethodPolicyResolver(map[string]string{
	contractID: "registry:submit",
})

// testEpoch is a fixed clock instant so submit timestamps are deterministic.
var testEpoch = mustTime("2026-06-18T00:00:00Z")

// mockAuthorizer is a test-only auth.Authorizer returning a fixed Decision
// (mock placement per go-standards.md §Naming — same-package test file).
type mockAuthorizer struct {
	decision authz.Decision
	err      error
}

func (m *mockAuthorizer) Authorize(context.Context, string, string, string) (authz.Decision, error) {
	return m.decision, m.err
}

func allowAuthorizer() *mockAuthorizer {
	dec, err := authz.Allow(authz.Obligations{})
	if err != nil {
		panic("allowAuthorizer: " + err.Error())
	}
	return &mockAuthorizer{decision: dec}
}

func denyAuthorizer(reason string) *mockAuthorizer {
	return &mockAuthorizer{decision: authz.Deny(reason)}
}

// newMux mounts the handler under the production-mirroring prefix
// (/api/v1/registry) so auth.Mount strips it off Contract.Path exactly as the
// cellgen route group does (prefix /api/v1/registry + mux.Route("/contracts")).
// RegisterRoutes installs the registry:submit RequirePermission policy, so the
// contract test exercises the same gate production uses. The returned mux holds a
// single Service over the in-memory store + real gate, so a duplicate submit
// through the same mux hits the real dedup path.
func newMux(t *testing.T) http.Handler {
	t.Helper()
	clk := clockmock.New(testEpoch)
	store := mem.NewRegistry(clk)
	gate := governance.NewRegistrationGate(registry.NewContractRegistrar(clk), clk)
	svc, err := NewService(store, gate, WithTxManager(persistence.WrapForCell(noopTxRunner{})))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	h := NewHandler(svc, contractSubmitResolver)
	mux := celltest.NewTestMux()
	mux.Route("/api/v1/registry", func(sub cell.RouteMux) {
		if err := h.RegisterRoutes(sub); err != nil {
			t.Fatalf("RegisterRoutes: %v", err)
		}
	})
	return mux
}

// adminCtx returns a context with an admin principal, a test tenant, and the
// given authorizer injected. Tests that don't need the authorizer can pass nil.
func adminCtx(authorizer auth.Authorizer) context.Context {
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{
		Kind: auth.PrincipalUser, Subject: "cell-a", Roles: []string{auth.RoleAdmin}, AuthMethod: "test",
	})
	ctx = ctxkeys.WithTenantID(ctx, testTenantStr)
	if authorizer != nil {
		ctx = auth.WithAuthorizer(ctx, authorizer)
	}
	return ctx
}

func postSubmit(t *testing.T, mux http.Handler, ctx context.Context, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/registry/contracts", bytes.NewReader([]byte(body))).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	return rec
}

// validBody is a full contract declaration that satisfies the gate's curated
// rule set (CH-01 ownerCell, CH-02 lifecycle, CH-03 schemaRefs.response, FMT-08
// id prefix, REG-01 endpoints.server).
//
//nolint:lll // JSON body literals cannot be split across lines without breaking encoding
const validBody = `{"id":"http.example.foo.v1","kind":"http","ownerCell":"registrycore","lifecycle":"active","endpoints":{"server":"registrycore"},"schemaRefs":{"response":"response.schema.json"}}`

// TestContractSubmitServe_RequestSchema validates the request body schema
// (positive + per-constraint negatives) independent of the handler.
func TestContractSubmitServe_RequestSchema(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), contractID)
	c.ValidateRequest(t, []byte(validBody))
	// ownerCell / lifecycle are now required in the schema.
	c.MustRejectRequest(t, []byte(`{"kind":"http","ownerCell":"registrycore","lifecycle":"active"}`))          // missing id
	c.MustRejectRequest(t, []byte(`{"id":"x","ownerCell":"registrycore","lifecycle":"active"}`))               // missing kind
	c.MustRejectRequest(t, []byte(`{"id":"x","kind":"nope","ownerCell":"registrycore","lifecycle":"active"}`)) // kind not in enum
	// additionalProperties:false — extra field rejected
	c.MustRejectRequest(t, []byte(
		`{"id":"x","kind":"http","ownerCell":"registrycore","lifecycle":"active","extra":"f"}`,
	))
	c.MustRejectRequest(t, []byte(`{"id":"","kind":"http","ownerCell":"registrycore","lifecycle":"active"}`)) // id minLength
	c.MustRejectRequest(t, []byte(`{"id":"x","kind":"http","lifecycle":"active"}`))                           // missing ownerCell
	c.MustRejectRequest(t, []byte(`{"id":"x","kind":"http","ownerCell":"registrycore"}`))                     // missing lifecycle
	c.MustRejectRequest(t, []byte(`{"id":"x","kind":"http","ownerCell":"registrycore","lifecycle":"nope"}`))  // lifecycle not in enum
}

// TestContractSubmitServe_OK: an authenticated principal (allow PDP) submitting a
// valid contract declaration gets 201 whose body satisfies the response schema.
func TestContractSubmitServe_OK(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), contractID)
	rec := postSubmit(t, newMux(t), adminCtx(allowAuthorizer()), validBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateHTTPResponseRecorder(t, rec)
}

// TestContractSubmitServe_Unauthenticated: no principal ⇒ RequirePermission 401.
func TestContractSubmitServe_Unauthenticated(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), contractID)
	rec := postSubmit(t, newMux(t), context.Background(), validBody)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
}

// TestContractSubmitServe_Forbidden: authenticated but the PDP denies
// registry:submit ⇒ 403.
func TestContractSubmitServe_Forbidden(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), contractID)
	rec := postSubmit(t, newMux(t), adminCtx(denyAuthorizer("no registry:submit")), validBody)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
}

// TestContractSubmitServe_BadRequest: a malformed JSON body ⇒ 400 (handler decode).
func TestContractSubmitServe_BadRequest(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), contractID)
	rec := postSubmit(t, newMux(t), adminCtx(allowAuthorizer()), `{not json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
}

// TestContractSubmitServe_Duplicate: submitting the same id twice through the same
// service ⇒ the second is a real 409 (store dedup), not a fabricated branch.
func TestContractSubmitServe_Duplicate(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), contractID)
	mux := newMux(t)
	ctx := adminCtx(allowAuthorizer())
	if rec := postSubmit(t, mux, ctx, validBody); rec.Code != http.StatusCreated {
		t.Fatalf("first submit status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	rec := postSubmit(t, mux, ctx, validBody)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate submit status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
}

// TestContractSubmitServe_PayloadTooLarge: a body exceeding the JSON decode limit
// ⇒ 413, the contract's declared payload-too-large response.
func TestContractSubmitServe_PayloadTooLarge(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), contractID)
	// A huge id string pushes the body past httputil.DefaultDecodeJSONLimit (1 MiB).
	huge := strings.Repeat("a", int(httputil.DefaultDecodeJSONLimit)+1024)
	body := `{"id":"` + huge + `","kind":"http","ownerCell":"registrycore","lifecycle":"active"}`
	rec := postSubmit(t, newMux(t), adminCtx(allowAuthorizer()), body)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
}

// TestContractSubmitServe_GateReject: a schema-valid payload that passes JSON
// decode but fails the gate (missing endpoints.server → REG-01) returns 400.
func TestContractSubmitServe_GateReject(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), contractID)
	// Valid JSON per schema (ownerCell + lifecycle present), but missing
	// endpoints.server → REG-01 rejects at the gate layer.
	//
	//nolint:lll // JSON body literal
	body := `{"id":"http.noprovider.v1","kind":"http","ownerCell":"registrycore","lifecycle":"active","schemaRefs":{"response":"response.schema.json"}}`
	rec := postSubmit(t, newMux(t), adminCtx(allowAuthorizer()), body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (gate reject); body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
}
