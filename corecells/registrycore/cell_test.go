package registrycore

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/cell/celltest"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
	"github.com/ghbvf/gocell/framework/runtime/auth"
)

// testTenantStr is the canonical tenant UUID the cell-loop test scopes submit +
// list to (submit/list are tenant-scoped via ports.Registry since 303-US6).
const testTenantStr = "00000000-0000-0000-0000-000000000001"

var testEpoch = func() time.Time {
	ts, err := time.Parse(time.RFC3339, "2026-06-18T00:00:00Z")
	if err != nil {
		panic(err)
	}
	return ts
}()

func newCell() *RegistryCore { return New(clockmock.New(testEpoch)) }

func TestNew_Identity(t *testing.T) {
	c := newCell()
	if c == nil {
		t.Fatal("New() returned nil")
	}
	if c.ID() != "registrycore" {
		t.Fatalf("ID() = %q, want registrycore", c.ID())
	}
	if got := c.ConsistencyLevel().String(); got != "L1" {
		t.Fatalf("ConsistencyLevel() = %q, want L1", got)
	}
	var _ cell.Cell = c // compile-time: RegistryCore satisfies cell.Cell
}

// TestInitInternal_WiresHandlers pins that the hand-written init hook constructs
// both slice handlers the generated route group references.
func TestInitInternal_WiresHandlers(t *testing.T) {
	c := newCell()
	rec := cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo)
	if err := c.initInternal(context.Background(), rec); err != nil {
		t.Fatalf("initInternal: %v", err)
	}
	if c.writeHandler == nil || c.readHandler == nil {
		t.Fatal("slice handlers nil after initInternal — generated route group would nil-deref")
	}
	// Both slices must be registered into the BaseCell inventory (OwnedSlices),
	// mirroring configcore/auditcore/accesscore — declared slice set ↔ runtime
	// inventory stay in sync.
	owned := c.OwnedSlices()
	ids := map[string]bool{}
	for _, s := range owned {
		ids[s.ID()] = true
	}
	if len(owned) != 2 || !ids["registrywrite"] || !ids["registryread"] {
		t.Fatalf("OwnedSlices() = %d %v, want 2 (registrywrite + registryread)", len(owned), ids)
	}
}

// TestInit_RegistersRegistryRouteGroup pins that the cell registers exactly one
// route group on the PrimaryListener under the distinct /api/v1/registry prefix
// (configcore owns bare /api/v1, so registrycore MUST own its own segment).
func TestInit_RegistersRegistryRouteGroup(t *testing.T) {
	c := newCell()
	rec := cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo)
	if err := c.Init(context.Background(), rec); err != nil {
		t.Fatalf("Init: %v", err)
	}
	groups := rec.Snapshot().RouteGroups
	if len(groups) != 1 {
		t.Fatalf("RouteGroups = %d, want 1", len(groups))
	}
	if groups[0].Listener != cell.PrimaryListener {
		t.Fatalf("listener = %v, want PrimaryListener", groups[0].Listener)
	}
	if groups[0].Prefix != "/api/v1/registry" {
		t.Fatalf("prefix = %q, want /api/v1/registry", groups[0].Prefix)
	}
}

func allowCtx() context.Context {
	dec, err := authz.Allow(authz.Obligations{})
	if err != nil {
		panic(err)
	}
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{
		Kind: auth.PrincipalUser, Subject: "cell-a", Roles: []string{auth.RoleAdmin}, AuthMethod: "test",
	})
	ctx = ctxkeys.WithTenantID(ctx, testTenantStr)
	return auth.WithAuthorizer(ctx, allowAuthorizer{dec})
}

type allowAuthorizer struct{ dec authz.Decision }

func (a allowAuthorizer) Authorize(context.Context, string, string, string) (authz.Decision, error) {
	return a.dec, nil
}

// TestSubmitListLoop_ThroughCellHandlers drives the cell's OWN two handlers (which
// share the single registrar built in initInternal) over a test mux: a submitted
// contract is visible in a subsequent list. This is the minimal-usable-surface
// proof (spec US4 "最小可用面") that submit and list share one in-mem registrar.
func TestSubmitListLoop_ThroughCellHandlers(t *testing.T) {
	c := newCell()
	rec := cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo)
	if err := c.initInternal(context.Background(), rec); err != nil {
		t.Fatalf("initInternal: %v", err)
	}
	mux := celltest.NewTestMux()
	mux.Route("/api/v1/registry", func(sub cell.RouteMux) {
		if err := c.writeHandler.RegisterRoutes(sub); err != nil {
			t.Fatalf("write RegisterRoutes: %v", err)
		}
		if err := c.readHandler.RegisterRoutes(sub); err != nil {
			t.Fatalf("read RegisterRoutes: %v", err)
		}
	})
	ctx := allowCtx()

	const submitBody = `{"id":"http.example.foo.v1","kind":"http","ownerCell":"registrycore",` +
		`"lifecycle":"active","endpoints":{"server":"registrycore"},` +
		`"schemaRefs":{"response":"response.schema.json"}}`
	postRec := httptest.NewRecorder()
	postReq := httptest.NewRequest(http.MethodPost, "/api/v1/registry/contracts",
		bytes.NewReader([]byte(submitBody))).WithContext(ctx)
	postReq.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusCreated {
		t.Fatalf("submit status = %d, want 201; body=%s", postRec.Code, postRec.Body.String())
	}

	getRec := httptest.NewRecorder()
	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/registry/contracts", nil).WithContext(ctx)
	mux.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200; body=%s", getRec.Code, getRec.Body.String())
	}
	var body struct {
		Data []struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"data"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode list body: %v; body=%s", err, getRec.Body.String())
	}
	if len(body.Data) != 1 || body.Data[0].ID != "http.example.foo.v1" {
		t.Fatalf("list = %+v, want the submitted contract http.example.foo.v1", body.Data)
	}
	if body.Data[0].State != registry.StateSubmitted().String() {
		t.Fatalf("submitted contract state = %q, want %q (sealed)", body.Data[0].State, registry.StateSubmitted().String())
	}
}
