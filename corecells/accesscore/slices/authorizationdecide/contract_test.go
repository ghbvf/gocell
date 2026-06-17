package authorizationdecide

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/cell/celltest"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/tests/contracttest"
)

// decideTestSubject is the canonical subject UUID used across the decide
// contract tests. The PDP self rule (subject.sub == resource.id) and the
// RequirePermissionForSelf gate both canonicalize via ParseCanonicalUUID, so a
// canonical lowercase UUID is required for the ownership match to fire.
const decideTestSubject = "11111111-1111-1111-1111-111111111111"

// failingPolicyRepo is a PolicyRepository whose eval-path read (ListByTenant)
// fails, simulating an unreachable policy store. The other methods delegate to a
// real mem repo so construction-time validation passes. Service.Authorize wraps a
// ListByTenant error as KindUnavailable → HTTP 503 (service.go fail-closed).
type failingPolicyRepo struct{ ports.PolicyRepository }

func (failingPolicyRepo) ListByTenant(context.Context, tenant.TenantID) ([]*abac.Policy, error) {
	return nil, errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable, "policy store down")
}

// newDecideMux builds a full-path mux mounting the decide handler at the
// contract-declared route (/api/v1/access/decide), backed by a real PDP Service
// over the given policy repository (built-in baseline rules apply). It returns
// the mux and the Service so the per-request context can inject the SAME Service
// as the in-context Authorizer the RequirePermissionForSelf gate reads — so the
// access:decide self-gate is exercised end-to-end against the real baseline, not
// a stub. (auth.Mount wraps the handler with the route policy, which runs even on
// a TestMux; only listener-level JWT is absent here.)
func newDecideMux(t *testing.T, repo ports.PolicyRepository) (http.Handler, *Service) {
	t.Helper()
	svc, err := NewService(clock.Real(), repo, mem.NewResourceAttributeProvider(), slog.Default(),
		WithTxManager(outbox.DemoCellTxManager()))
	if err != nil {
		t.Fatalf("newDecideMux: NewService: %v", err)
	}
	mux := celltest.NewTestMux()
	h := NewHandler(svc)
	mux.Route("/api/v1/access/decide", func(s cell.RouteMux) {
		if err := h.RegisterRoutes(s); err != nil {
			t.Fatalf("newDecideMux: RegisterRoutes: %v", err)
		}
	})
	return mux, svc
}

// decideReqCtx builds a request context carrying the canonical test tenant, the
// given authenticated principal, and the PDP Service as the in-context Authorizer
// (what bootstrap.WithPrimaryAuthorizer injects in production). The RequirePermissionForSelf
// gate reads the Authorizer from context; the handler reads tenant + principal.
func decideReqCtx(svc *Service, roles []string) context.Context {
	ctx := ctxkeys.WithTenantID(auth.TestContext(decideTestSubject, roles), testTenantIDStr)
	return auth.WithAuthorizer(ctx, svc)
}

type decideResponse struct {
	Data struct {
		Allowed bool `json:"allowed"`
	} `json:"data"`
}

func decodeDecide(t *testing.T, rec *httptest.ResponseRecorder) decideResponse {
	t.Helper()
	var out decideResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode decide response: %v (body=%s)", err, rec.Body.String())
	}
	return out
}

func postDecide(t *testing.T, h http.Handler, ctx context.Context, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/access/decide", strings.NewReader(body))
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestHttpAuthDecideV1_Allow: an admin querying an admin-baseline action gets
// data.allowed=true via the built-in baseline (no tenant policy seeded).
func TestHttpAuthDecideV1_Allow(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.auth.decide.v1")
	h, svc := newDecideMux(t, mem.NewPolicyRepository())

	rec := postDecide(t, h, decideReqCtx(svc, []string{auth.RoleAdmin}), `{"action":"audit:read"}`)
	c.ValidateHTTPResponseRecorder(t, rec)
	if got := decodeDecide(t, rec); !got.Data.Allowed {
		t.Fatalf("admin audit:read: expected allowed=true, got false")
	}
}

// TestHttpAuthDecideV1_Deny: a non-admin querying an admin-only action gets a
// successful 200 with data.allowed=false (a policy deny is NOT an HTTP error).
func TestHttpAuthDecideV1_Deny(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.auth.decide.v1")
	h, svc := newDecideMux(t, mem.NewPolicyRepository())

	rec := postDecide(t, h, decideReqCtx(svc, []string{"user"}), `{"action":"audit:read"}`)
	c.ValidateHTTPResponseRecorder(t, rec)
	if rec.Code != http.StatusOK {
		t.Fatalf("policy deny must be 200 (allowed=false), got %d", rec.Code)
	}
	if got := decodeDecide(t, rec); got.Data.Allowed {
		t.Fatalf("non-admin audit:read: expected allowed=false, got true")
	}
}

// TestHttpAuthDecideV1_AllowSelfResource: a user querying an owner-scoped action
// for their OWN subject (resource == self) is allowed by the baseline ownership
// rule (subject.sub == resource.id). Exercises the optional resource field.
func TestHttpAuthDecideV1_AllowSelfResource(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.auth.decide.v1")
	h, svc := newDecideMux(t, mem.NewPolicyRepository())

	body := `{"action":"user:read","resource":"` + decideTestSubject + `"}`
	rec := postDecide(t, h, decideReqCtx(svc, []string{"user"}), body)
	c.ValidateHTTPResponseRecorder(t, rec)
	if got := decodeDecide(t, rec); !got.Data.Allowed {
		t.Fatalf("self user:read (resource==self): expected allowed=true, got false")
	}
}

// TestHttpAuthDecideV1_UnknownAction: an action that is not a registered
// permission fails closed with 400 before reaching the PDP.
func TestHttpAuthDecideV1_UnknownAction(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.auth.decide.v1")
	h, svc := newDecideMux(t, mem.NewPolicyRepository())

	rec := postDecide(t, h, decideReqCtx(svc, []string{"user"}), `{"action":"not:a:real:perm"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown action: expected 400, got %d", rec.Code)
	}
	c.ValidateErrorResponse(t, http.StatusBadRequest, rec.Body.Bytes())
}

// TestHttpAuthDecideV1_BadBody: a missing required action and an unknown extra
// field (additionalProperties:false — the schema guarantee that the wire carries
// no subject/context) are both rejected by the generated schema validator (400).
func TestHttpAuthDecideV1_BadBody(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.auth.decide.v1")
	h, svc := newDecideMux(t, mem.NewPolicyRepository())

	for name, body := range map[string]string{
		"missing action":    `{}`,
		"empty action":      `{"action":""}`,
		"forbidden subject": `{"action":"audit:read","subject":"22222222-2222-2222-2222-222222222222"}`,
		"malformed json":    `{"action":`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := postDecide(t, h, decideReqCtx(svc, []string{"user"}), body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s: expected 400, got %d (body=%s)", name, rec.Code, rec.Body.String())
			}
			c.ValidateErrorResponse(t, http.StatusBadRequest, rec.Body.Bytes())
		})
	}
}

// TestHttpAuthDecideV1_MissingPrincipal: with no authenticated principal the
// handler fails closed with 401 (the subject is the decision subject).
func TestHttpAuthDecideV1_MissingPrincipal(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.auth.decide.v1")
	h, svc := newDecideMux(t, mem.NewPolicyRepository())

	// Authorizer + tenant present, but NO principal → the gate fails closed (401)
	// on the principal check before the handler runs.
	ctx := auth.WithAuthorizer(ctxkeys.WithTenantID(context.Background(), testTenantIDStr), svc)
	rec := postDecide(t, h, ctx, `{"action":"audit:read"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing principal: expected 401, got %d", rec.Code)
	}
	c.ValidateErrorResponse(t, http.StatusUnauthorized, rec.Body.Bytes())
}

// TestHttpAuthDecideV1_StoreUnavailable: an unreachable policy store surfaces as
// 503 (fail-closed), not a silent allow/deny.
func TestHttpAuthDecideV1_StoreUnavailable(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.auth.decide.v1")
	h, svc := newDecideMux(t, failingPolicyRepo{mem.NewPolicyRepository()})

	rec := postDecide(t, h, decideReqCtx(svc, []string{auth.RoleAdmin}), `{"action":"audit:read"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("store unavailable: expected 503, got %d", rec.Code)
	}
	c.ValidateErrorResponse(t, http.StatusServiceUnavailable, rec.Body.Bytes())
}

// TestHttpAuthDecideV1_ResponseSchema locks the response shape: a wrong-shaped
// body must be rejected by the contract response schema.
func TestHttpAuthDecideV1_ResponseSchema(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.auth.decide.v1")
	c.MustRejectResponse(t, []byte(`{"data":{"wrong":"shape"}}`))
	c.MustRejectResponse(t, []byte(`{"data":{}}`))
}
