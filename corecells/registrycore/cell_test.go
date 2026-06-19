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
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
	"github.com/ghbvf/gocell/framework/pkg/query"
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
// all three slice handlers the generated route group references.
func TestInitInternal_WiresHandlers(t *testing.T) {
	c := newCell()
	rec := cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo)
	if err := c.initInternal(context.Background(), rec); err != nil {
		t.Fatalf("initInternal: %v", err)
	}
	if c.writeHandler == nil || c.readHandler == nil || c.adminHandler == nil {
		t.Fatal("slice handlers nil after initInternal — generated route group would nil-deref")
	}
	// All slices must be registered into the BaseCell inventory (OwnedSlices),
	// mirroring configcore/auditcore/accesscore — declared slice set ↔ runtime
	// inventory stay in sync.
	owned := c.OwnedSlices()
	ids := map[string]bool{}
	for _, s := range owned {
		ids[s.ID()] = true
	}
	if len(owned) != 3 || !ids["registrywrite"] || !ids["registryread"] || !ids["registryadmin"] {
		t.Fatalf("OwnedSlices() = %d %v, want 3 (registrywrite + registryread + registryadmin)", len(owned), ids)
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

// TestInitInternal_DurableMode_NilCursorCodec_Errors pins that durable mode without
// an injected CursorCodec is a startup error (fail-closed, mirrors auditcore/
// configcore). A real TxManager is NOT required for this guard — the codec check
// fires first; the test supplies a demo TxManager to isolate the codec guard.
func TestInitInternal_DurableMode_NilCursorCodec_Errors(t *testing.T) {
	c := New(clockmock.New(testEpoch),
		WithTxManager(outbox.DemoCellTxManager()), // isolate codec guard
	)
	rec := cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDurable)
	err := c.initInternal(context.Background(), rec)
	if err == nil {
		t.Fatal("initInternal(durable, nil codec) must return error (fail-closed)")
	}
}

// TestInitInternal_DurableMode_DemoTxManager_Errors pins that durable mode with a
// demo (noop) TxManager is rejected by outbox.CheckNotNoop — an assembly that
// forgets to wire a real TxManager must fail at Init() time. A real codec is NOT
// required because the TxManager guard runs before the codec guard.
func TestInitInternal_DurableMode_DemoTxManager_Errors(t *testing.T) {
	// Build a real cursor codec so only the TxManager guard fires.
	devKey := []byte("registrycore-cell-test-key-32bytes!")
	codec, err := query.NewCursorCodec(devKey)
	if err != nil {
		t.Fatalf("NewCursorCodec: %v", err)
	}
	c := New(clockmock.New(testEpoch),
		WithTxManager(outbox.DemoCellTxManager()),
		WithCursorCodec(codec),
	)
	rec := cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDurable)
	err = c.initInternal(context.Background(), rec)
	if err == nil {
		t.Fatal("initInternal(durable, demo txManager) must return error (CheckNotNoop)")
	}
}

// cellTestTxRunner is a non-Nooper CellTxManager for durable-guard tests: it passes
// outbox.CheckNotNoop (unlike DemoCellTxManager) so a durable test can advance past
// the txManager guard and exercise a later guard.
type cellTestTxRunner struct{}

func (cellTestTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

// TestInitInternal_DurableMode_NilStore_Errors pins that durable mode without an
// injected registry store is a startup error (fail-closed, 303-US7 review F1): the
// in-memory store (process-local state, always-ready probe) must not back a durable
// assembly. A real tx + cursor are supplied so the store guard (last) is the one
// that fires.
func TestInitInternal_DurableMode_NilStore_Errors(t *testing.T) {
	devKey := []byte("registrycore-cell-test-key-32bytes!")
	codec, err := query.NewCursorCodec(devKey)
	if err != nil {
		t.Fatalf("NewCursorCodec: %v", err)
	}
	c := New(clockmock.New(testEpoch),
		WithTxManager(persistence.WrapForCell(cellTestTxRunner{})), // non-Nooper → passes CheckNotNoop
		WithCursorCodec(codec),
		// no WithRegistry → the store guard must fire
	)
	rec := cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDurable)
	if err := c.initInternal(context.Background(), rec); err == nil {
		t.Fatal("initInternal(durable, no registry store) must return error (fail-closed)")
	}
}

// TestInitInternal_DemoMode_Fallbacks_Succeed pins that demo mode with no injected
// TxManager or CursorCodec succeeds by falling back to the built-in defaults.
// This is the normal in-mem / CI topology.
func TestInitInternal_DemoMode_Fallbacks_Succeed(t *testing.T) {
	c := New(clockmock.New(testEpoch)) // no options — demo fallbacks apply
	rec := cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo)
	if err := c.initInternal(context.Background(), rec); err != nil {
		t.Fatalf("initInternal(demo, no opts) must succeed: %v", err)
	}
	if c.writeHandler == nil || c.readHandler == nil {
		t.Fatal("handlers nil after demo fallback initInternal")
	}
}
