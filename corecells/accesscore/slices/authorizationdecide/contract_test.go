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
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	decidegen "github.com/ghbvf/gocell/generated/contracts/http/auth/decide/v1"
	"github.com/ghbvf/gocell/tests/contracttest"
)

// testResolver returns a MethodPolicyResolver seeded with the authorizationdecide
// contract→action map, mirroring the cellgen-wired resolver in cell_init.go.
func testResolver() authz.MethodPolicyResolver {
	return auth.NewStaticMethodPolicyResolver(map[string]string{
		"http.auth.decide.v1": "access:decide",
	})
}

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
	h := NewHandler(svc, testResolver())
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

// TestHttpAuthDecideV1_SelfResource_NonCanonicalUUID is the F1 regression (#1863
// review): a self query whose resource is a non-canonical UUID — uppercase-dashed
// or 32-char compact — must still match the ownership rule, because the handler
// canonicalizes req.Resource via httputil.ParseCanonicalUUID before forwarding to
// the PDP (mirroring the gate's own canonicalization in RequirePermissionForSelf).
// Pre-fix the raw non-canonical resource never equals the canonical lowercase
// subject, so the user:read self rule (subject.sub == resource.id) misfires and the
// API wrongly returns allowed=false for a route that would in fact allow.
func TestHttpAuthDecideV1_SelfResource_NonCanonicalUUID(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.auth.decide.v1")
	h, svc := newDecideMux(t, mem.NewPolicyRepository())

	// A canonical lowercase subject WITH hex letters so the uppercase form differs.
	const canonicalSubject = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	ctx := auth.WithAuthorizer(
		ctxkeys.WithTenantID(auth.TestContext(canonicalSubject, []string{"user"}), testTenantIDStr), svc)

	for name, resource := range map[string]string{
		"uppercase dashed": strings.ToUpper(canonicalSubject),
		"compact":          strings.ReplaceAll(canonicalSubject, "-", ""),
	} {
		t.Run(name, func(t *testing.T) {
			body := `{"action":"user:read","resource":"` + resource + `"}`
			rec := postDecide(t, h, ctx, body)
			c.ValidateHTTPResponseRecorder(t, rec)
			if got := decodeDecide(t, rec); !got.Data.Allowed {
				t.Fatalf("self user:read (resource=%q, %s): expected allowed=true, got false", resource, name)
			}
		})
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

// TestHttpAuthDecideV1_Forbidden covers the two contract-declared 403 fail-closed
// paths, both produced by the RequirePermissionForSelf gate (not the handler):
//   - no Authorizer wired → "policy engine not wired" deny;
//   - missing tenant scope → the gate's access:decide Authorize fails closed.
func TestHttpAuthDecideV1_Forbidden(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.auth.decide.v1")

	t.Run("no authorizer wired", func(t *testing.T) {
		h, _ := newDecideMux(t, mem.NewPolicyRepository())
		// principal + tenant, but NO Authorizer in context → gate fails closed (403).
		ctx := ctxkeys.WithTenantID(auth.TestContext(decideTestSubject, []string{"user"}), testTenantIDStr)
		rec := postDecide(t, h, ctx, `{"action":"audit:read"}`)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("no authorizer: expected 403, got %d", rec.Code)
		}
		c.ValidateErrorResponse(t, http.StatusForbidden, rec.Body.Bytes())
	})

	t.Run("missing tenant scope", func(t *testing.T) {
		h, svc := newDecideMux(t, mem.NewPolicyRepository())
		// principal + Authorizer, but NO tenant → the gate's access:decide Authorize
		// fails closed (tenant.FromContext error → KindPermissionDenied → 403).
		ctx := auth.WithAuthorizer(auth.TestContext(decideTestSubject, []string{"user"}), svc)
		rec := postDecide(t, h, ctx, `{"action":"audit:read"}`)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("missing tenant: expected 403, got %d", rec.Code)
		}
		c.ValidateErrorResponse(t, http.StatusForbidden, rec.Body.Bytes())
	})
}

// TestDecideAdapter_Decide_Direct covers the two DecideAdapter.Decide branches the
// RequirePermissionForSelf gate makes unreachable through the mounted mux (the gate
// rejects a missing principal / a failed PDP before the handler runs) but which stay
// load-bearing defense-in-depth: the handler is the decision subject's last fail-
// closed line. Exercised by invoking the adapter directly, without the route gate.
func TestDecideAdapter_Decide_Direct(t *testing.T) {
	newSvc := func(t *testing.T, repo ports.PolicyRepository) *Service {
		t.Helper()
		svc, err := NewService(clock.Real(), repo, mem.NewResourceAttributeProvider(), slog.Default(),
			WithTxManager(outbox.DemoCellTxManager()))
		if err != nil {
			t.Fatalf("NewService: %v", err)
		}
		return svc
	}

	t.Run("missing principal fails closed 401", func(t *testing.T) {
		// Tenant present but NO principal in ctx → the handler's defense-in-depth 401
		// (the subject is the decision subject; absent → fail closed, not a PDP query).
		ctx := ctxkeys.WithTenantID(context.Background(), testTenantIDStr)
		resp, err := DecideAdapter{newSvc(t, mem.NewPolicyRepository())}.Decide(ctx, &decidegen.Request{Action: "audit:read"})
		if err != nil {
			t.Fatalf("expected typed 401 response, got err: %v", err)
		}
		if _, ok := resp.(decidegen.Decide401ErrorResponse); !ok {
			t.Fatalf("expected Decide401ErrorResponse, got %T", resp)
		}
	})

	t.Run("Authorize error flows through as (nil, err)", func(t *testing.T) {
		// Principal + tenant present so we pass the principal + action gates and reach
		// Authorize, whose policy store fails (KindUnavailable) → the handler returns
		// (nil, err) for httputil.WriteError to map by errcode.Kind.
		ctx := ctxkeys.WithTenantID(auth.TestContext(decideTestSubject, []string{auth.RoleAdmin}), testTenantIDStr)
		svc := newSvc(t, failingPolicyRepo{mem.NewPolicyRepository()})
		resp, err := DecideAdapter{svc}.Decide(ctx, &decidegen.Request{Action: "audit:read"})
		if err == nil {
			t.Fatalf("expected Authorize error to flow through, got resp %T", resp)
		}
		if resp != nil {
			t.Fatalf("expected nil response on the error path, got %T", resp)
		}
	})
}

// TestHttpAuthDecideV1_ResponseSchema locks the response shape: a body missing the
// required allowed verdict must be rejected by the contract response schema.
//
// F4 (#1863 review) note — the "only return allowed, never leak Decision.Reason()"
// boundary is NOT enforced via additionalProperties:false here: response schemas are
// `lenient` by ADR-202605031600 (V1-RESPONSE-EVOLVE) so the wire stays forward-
// compatible, and verify-schema-policy.sh forbids additionalProperties:false on them.
// The boundary is instead machine-locked one layer stronger, at the codegen type:
// the generated ResponseData carries a single Allowed bool field (no reason), so the
// handler structurally cannot serialize a reason — see TestDecideAdapter_Decide_Direct
// and the Decide200JSONResponse construction in handler.go.
func TestHttpAuthDecideV1_ResponseSchema(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.auth.decide.v1")
	c.MustRejectResponse(t, []byte(`{"data":{"wrong":"shape"}}`))
	c.MustRejectResponse(t, []byte(`{"data":{}}`))
}
