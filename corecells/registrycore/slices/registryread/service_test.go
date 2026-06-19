package registryread

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/corecells/registrycore/internal/mem"
	"github.com/ghbvf/gocell/corecells/registrycore/internal/ports"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/projection"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	list "github.com/ghbvf/gocell/generated/contracts/http/registry/contract/list/v1"
)

// devCursorKey is a ≥32-byte test key for NewCursorCodec in unit tests.
var devCursorKey = []byte("registryread-test-key-32bytes-ok!") // 33 bytes

func mustCodec(t *testing.T) *query.CursorCodec {
	t.Helper()
	c, err := query.NewCursorCodec(devCursorKey)
	if err != nil {
		t.Fatalf("NewCursorCodec: %v", err)
	}
	return c
}

// newSvc builds a Service backed by the given store and a fresh test codec.
// RunModeDemo is used so stale cursors fail-open in unit tests (same as demo
// cell wiring).
func newSvc(t *testing.T, store ports.Registry) *Service {
	t.Helper()
	svc, err := NewService(store, mustCodec(t), query.RunModeDemo, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// newMemStore builds an empty in-memory registry.
func newMemStore(t *testing.T) *mem.Registry {
	t.Helper()
	return mem.NewRegistry(clockmock.New(testEpoch))
}

// seedStore creates registrations with the given IDs in tenant t.
func seedStore(ctx context.Context, t *testing.T, store ports.Registry, tnt tenant.TenantID, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if _, err := store.Create(ctx, tnt, registry.SubmitInput{ID: id, Kind: "http", Submitter: "cell-a"}); err != nil {
			t.Fatalf("seed %q: %v", id, err)
		}
	}
}

// tenantCtx returns a context carrying the given tenant UUID string and a test principal.
func tenantCtx(tenantStr string) context.Context {
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{
		Kind: auth.PrincipalUser, Subject: "admin-1", Roles: []string{auth.RoleAdmin}, AuthMethod: "test",
	})
	return ctxkeys.WithTenantID(ctx, tenantStr)
}

func mustList(t *testing.T, svc *Service, ctx context.Context, req *list.Request) list.List200JSONResponse {
	t.Helper()
	resp, err := svc.List(ctx, req)
	if err != nil {
		t.Fatalf("List: unexpected error %v", err)
	}
	ok, isOK := resp.(list.List200JSONResponse)
	if !isOK {
		t.Fatalf("List returned %T, want List200JSONResponse", resp)
	}
	return ok
}

// itemField extracts a string field from a projection.ResourceProjection by
// round-tripping through JSON. ResourceProjection is sealed (unexported data),
// so JSON serialization is the only way to read field values in tests.
func itemField(t *testing.T, item projection.ResourceProjection, key string) string {
	t.Helper()
	b, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("itemField: marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("itemField: unmarshal: %v", err)
	}
	v, ok := m[key]
	if !ok {
		t.Fatalf("itemField: key %q not found in projection; keys: %v", key, m)
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("itemField: key %q value is %T (%v), want string", key, v, v)
	}
	return s
}

// TestNewService_NilStore pins the required-dep fail-fast for store.
func TestNewService_NilStore(t *testing.T) {
	if _, err := NewService(nil, mustCodec(t), query.RunModeDemo, nil); err == nil {
		t.Fatal("NewService(nil store) must error (gocell:\"required\")")
	}
}

// TestNewService_NilCodec pins the required-dep fail-fast for codec.
func TestNewService_NilCodec(t *testing.T) {
	if _, err := NewService(newMemStore(t), nil, query.RunModeDemo, nil); err == nil {
		t.Fatal("NewService(nil codec) must error (gocell:\"required\")")
	}
}

// TestList_Empty: no registrations ⇒ empty (non-nil) page, hasMore=false.
func TestList_Empty(t *testing.T) {
	ctx := tenantCtx(testTenantStr)
	svc := newSvc(t, newMemStore(t))
	got := mustList(t, svc, ctx, &list.Request{})
	if got.Data == nil {
		t.Fatal("Data must be a non-nil empty slice (serializes as [])")
	}
	if len(got.Data) != 0 || got.HasMore || got.NextCursor != "" {
		t.Fatalf("empty list = %+v, want zero items / no more", got)
	}
}

// TestList_SingleItem: one registration projects to a wire item whose state is
// the sealed RegistrationState spelling.
func TestList_SingleItem(t *testing.T) {
	ctx := tenantCtx(testTenantStr)
	tnt, _ := tenant.ParseTenantID(testTenantStr)
	store := newMemStore(t)
	seedStore(ctx, t, store, tnt, "http.example.foo.v1")
	svc := newSvc(t, store)

	got := mustList(t, svc, ctx, &list.Request{})
	if len(got.Data) != 1 {
		t.Fatalf("len(Data) = %d, want 1", len(got.Data))
	}
	it := got.Data[0]
	if id := itemField(t, it, "id"); id != "http.example.foo.v1" {
		t.Errorf("item.id = %q, want http.example.foo.v1", id)
	}
	if kind := itemField(t, it, "kind"); kind != "http" {
		t.Errorf("item.kind = %q, want http", kind)
	}
	if sub := itemField(t, it, "submitter"); sub != "cell-a" {
		t.Errorf("item.submitter = %q, want cell-a", sub)
	}
	if state := itemField(t, it, "state"); state != registry.StateSubmitted().String() {
		t.Errorf("item.state = %q, want %q (sealed)", state, registry.StateSubmitted().String())
	}
}

// TestList_Pagination: id-ordered cursor paging across two pages using opaque
// HMAC-signed cursor (stable, not guessable).
func TestList_Pagination(t *testing.T) {
	ctx := tenantCtx(testTenantStr)
	tnt, _ := tenant.ParseTenantID(testTenantStr)
	store := newMemStore(t)
	seedStore(ctx, t, store, tnt, "c1", "c2", "c3")
	svc := newSvc(t, store)

	first := mustList(t, svc, ctx, &list.Request{Limit: 2})
	if len(first.Data) != 2 {
		t.Fatalf("page1: len(Data) = %d, want 2", len(first.Data))
	}
	if !first.HasMore {
		t.Fatal("page1: HasMore must be true")
	}
	if first.NextCursor == "" {
		t.Fatal("page1: NextCursor must be non-empty")
	}
	id0 := itemField(t, first.Data[0], "id")
	id1 := itemField(t, first.Data[1], "id")
	if id0 != "c1" || id1 != "c2" {
		t.Fatalf("page1 ids = %q,%q, want c1,c2", id0, id1)
	}

	// Use the opaque cursor from page 1 to fetch page 2.
	second := mustList(t, svc, ctx, &list.Request{Limit: 2, Cursor: first.NextCursor})
	if len(second.Data) != 1 || second.HasMore {
		t.Fatalf("page2: len(Data)=%d hasMore=%v, want 1 item hasMore=false", len(second.Data), second.HasMore)
	}
	if id := itemField(t, second.Data[0], "id"); id != "c3" {
		t.Fatalf("page2 id = %q, want c3", id)
	}
	if second.NextCursor != "" {
		t.Fatalf("page2: NextCursor must be empty on last page, got %q", second.NextCursor)
	}
}

// TestList_LimitClamped: limit > 500 is clamped to 500 by PageParams.Normalize.
func TestList_LimitClamped(t *testing.T) {
	ctx := tenantCtx(testTenantStr)
	tnt, _ := tenant.ParseTenantID(testTenantStr)
	store := newMemStore(t)
	// seed fewer than 500 items; just verify response doesn't error and
	// hasMore is false (all fit within clamped 500 limit)
	seedStore(ctx, t, store, tnt, "a1", "a2")
	svc := newSvc(t, store)

	got := mustList(t, svc, ctx, &list.Request{Limit: 10000})
	if len(got.Data) != 2 || got.HasMore {
		t.Fatalf("limit-clamped list = %+v, want 2 items hasMore=false", got)
	}
}

// TestList_TenantIsolation: registrations created under tenant A are not visible
// to a context scoped to tenant B.
func TestList_TenantIsolation(t *testing.T) {
	const tenantA = "00000000-0000-0000-0000-000000000001"
	const tenantB = "00000000-0000-0000-0000-000000000002"

	store := newMemStore(t)
	tntA, _ := tenant.ParseTenantID(tenantA)
	ctxA := tenantCtx(tenantA)
	seedStore(ctxA, t, store, tntA, "http.example.foo.v1", "http.example.bar.v1")
	svc := newSvc(t, store)

	// tenant B context should see no registrations.
	ctxB := tenantCtx(tenantB)
	got := mustList(t, svc, ctxB, &list.Request{})
	if len(got.Data) != 0 {
		t.Fatalf("tenant B sees %d items from tenant A, want 0", len(got.Data))
	}
}

// TestList_MissingTenant: no tenant in context → service returns 403.
func TestList_MissingTenant(t *testing.T) {
	svc := newSvc(t, newMemStore(t))
	ctx := context.Background() // no tenant, no principal
	resp, err := svc.List(ctx, &list.Request{})
	if err != nil {
		t.Fatalf("List returned unexpected error %v, want 403 response", err)
	}
	if _, ok := resp.(list.List403ErrorResponse); !ok {
		t.Fatalf("List without tenant returned %T, want List403ErrorResponse", resp)
	}
}

// seedAndAdvance seeds one registration and transitions it through the given
// states in order. Used to set up multi-state fixtures in service tests.
func seedAndAdvance(
	ctx context.Context, t *testing.T, store *mem.Registry, tnt tenant.TenantID,
	id string, states ...registry.RegistrationState,
) {
	t.Helper()
	if _, err := store.Create(ctx, tnt, registry.SubmitInput{ID: id, Kind: "http", Submitter: "cell-a"}); err != nil {
		t.Fatalf("seed %q: %v", id, err)
	}
	for _, st := range states {
		if _, err := store.Transition(ctx, tnt, registry.AdvanceInput{ID: id, To: st, Actor: "system"}); err != nil {
			t.Fatalf("transition %q → %v: %v", id, st, err)
		}
	}
}

// TestList_StateFilter_SubmittedOnly: state=submitted returns only submitted registrations.
func TestList_StateFilter_SubmittedOnly(t *testing.T) {
	ctx := tenantCtx(testTenantStr)
	tnt, _ := tenant.ParseTenantID(testTenantStr)
	store := newMemStore(t)
	// "a" stays submitted; "b" advances to probing.
	seedStore(ctx, t, store, tnt, "a")
	seedAndAdvance(ctx, t, store, tnt, "b", registry.StateProbing())

	svc := newSvc(t, store)
	got := mustList(t, svc, ctx, &list.Request{State: "submitted"})
	if len(got.Data) != 1 {
		t.Fatalf("state=submitted: len(Data) = %d, want 1", len(got.Data))
	}
	if id := itemField(t, got.Data[0], "id"); id != "a" {
		t.Errorf("state=submitted: id = %q, want a", id)
	}
}

// TestList_StateFilter_NoMatch: state with no matching registrations → empty page.
func TestList_StateFilter_NoMatch(t *testing.T) {
	ctx := tenantCtx(testTenantStr)
	tnt, _ := tenant.ParseTenantID(testTenantStr)
	store := newMemStore(t)
	seedStore(ctx, t, store, tnt, "a") // only submitted; no approved rows

	svc := newSvc(t, store)
	got := mustList(t, svc, ctx, &list.Request{State: "approved"})
	if len(got.Data) != 0 {
		t.Fatalf("state=approved no match: len(Data) = %d, want 0", len(got.Data))
	}
	if got.HasMore {
		t.Fatal("state=approved no match: HasMore must be false")
	}
}

// TestList_StateFilter_InvalidState: unknown state value → 400.
func TestList_StateFilter_InvalidState(t *testing.T) {
	ctx := tenantCtx(testTenantStr)
	svc := newSvc(t, newMemStore(t))

	// Pass an unrecognized state string — parseStateFilter (registry.ParseState)
	// is the sole membership guard (no queryParam enum), returning a 400 response.
	resp, err := svc.List(ctx, &list.Request{State: "bogus-state"})
	if err != nil {
		t.Fatalf("List returned unexpected error %v, want 400 response", err)
	}
	if _, ok := resp.(list.List400ErrorResponse); !ok {
		t.Fatalf("List with invalid state returned %T, want List400ErrorResponse", resp)
	}
}

// TestList_StateFilter_WithPagination: state filter + cursor pagination returns
// only the filtered state across pages.
func TestList_StateFilter_WithPagination(t *testing.T) {
	ctx := tenantCtx(testTenantStr)
	tnt, _ := tenant.ParseTenantID(testTenantStr)
	store := newMemStore(t)
	// 3 submitted; 1 probing — only submitted rows should appear with filter.
	seedStore(ctx, t, store, tnt, "c1", "c2", "c3")
	seedAndAdvance(ctx, t, store, tnt, "d1", registry.StateProbing())

	svc := newSvc(t, store)

	first := mustList(t, svc, ctx, &list.Request{Limit: 2, State: "submitted"})
	if len(first.Data) != 2 {
		t.Fatalf("page1 state filter: len(Data) = %d, want 2", len(first.Data))
	}
	if !first.HasMore {
		t.Fatal("page1 state filter: HasMore must be true (3 submitted rows, limit=2)")
	}
	if first.NextCursor == "" {
		t.Fatal("page1 state filter: NextCursor must be non-empty")
	}

	// Page 2: use cursor from page 1 with the same state filter.
	second := mustList(t, svc, ctx, &list.Request{Limit: 2, Cursor: first.NextCursor, State: "submitted"})
	if len(second.Data) != 1 || second.HasMore {
		t.Fatalf("page2 state filter: len(Data)=%d hasMore=%v, want 1 item, hasMore=false", len(second.Data), second.HasMore)
	}
	if id := itemField(t, second.Data[0], "id"); id != "c3" {
		t.Errorf("page2 state filter: id = %q, want c3", id)
	}
}

// TestList_CursorStateBinding: a cursor issued for state=A is rejected by
// state=B (cursor scope mismatch → ErrCursorInvalid returned as error).
//
// The cursor HMAC payload embeds the QueryContext fingerprint which includes the
// state value. A cursor issued for state=submitted has a different fingerprint
// than a request with no state filter; the framework's ValidateCursorScope
// returns ErrCursorInvalid, which ExecutePagedQuery propagates as a (nil, err)
// pair — the caller (production handler) maps this to a 400 via WriteError.
func TestList_CursorStateBinding(t *testing.T) {
	ctx := tenantCtx(testTenantStr)
	tnt, _ := tenant.ParseTenantID(testTenantStr)
	store := newMemStore(t)
	// Seed two submitted registrations so a non-empty first page has a NextCursor.
	seedStore(ctx, t, store, tnt, "a", "b")

	svc := newSvc(t, store)

	// Get a cursor under state=submitted (limit=1 → hasMore, cursor encodes state scope).
	first := mustList(t, svc, ctx, &list.Request{Limit: 1, State: "submitted"})
	if first.NextCursor == "" {
		t.Fatal("expected NextCursor from state=submitted page1")
	}

	// Attempt to use that cursor under a different state (no state filter).
	// The scope mismatch (state "submitted" vs state "") must return ErrCursorInvalid.
	_, err := svc.List(ctx, &list.Request{Limit: 1, Cursor: first.NextCursor})
	if err == nil {
		t.Fatal("cross-state cursor replay: expected ErrCursorInvalid error, got nil")
	}
	var ce *errcode.Error
	if !errors.As(err, &ce) || ce.Code != errcode.ErrCursorInvalid {
		t.Errorf("cross-state cursor replay: got error %v, want ErrCursorInvalid", err)
	}
}
