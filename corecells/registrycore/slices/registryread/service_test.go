package registryread

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/registry"
	list "github.com/ghbvf/gocell/generated/contracts/http/registry/contract/list/v1"
)

func mustTime(s string) time.Time {
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic("mustTime: " + err.Error())
	}
	return ts
}

func seed(t *testing.T, ids ...string) *registry.ContractRegistrar {
	t.Helper()
	r := registry.NewContractRegistrar(clockmock.New(testEpoch))
	for _, id := range ids {
		if _, err := r.Submit(registry.SubmitInput{ID: id, Kind: "http", Submitter: "cell-a"}); err != nil {
			t.Fatalf("seed %q: %v", id, err)
		}
	}
	return r
}

func mustList(t *testing.T, svc *Service, req *list.Request) list.List200JSONResponse {
	t.Helper()
	resp, err := svc.List(context.Background(), req)
	if err != nil {
		t.Fatalf("List: unexpected error %v", err)
	}
	ok, isOK := resp.(list.List200JSONResponse)
	if !isOK {
		t.Fatalf("List returned %T, want List200JSONResponse", resp)
	}
	return ok
}

// TestNewService_NilRegistrar pins the required-dep fail-fast.
func TestNewService_NilRegistrar(t *testing.T) {
	if _, err := NewService(nil); err == nil {
		t.Fatal("NewService(nil) must error (gocell:\"required\")")
	}
}

// TestList_Empty: no registrations ⇒ empty (non-nil) page, hasMore=false.
func TestList_Empty(t *testing.T) {
	svc, err := NewService(registry.NewContractRegistrar(clockmock.New(testEpoch)))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	got := mustList(t, svc, &list.Request{})
	if got.Data == nil {
		t.Fatal("Data must be a non-nil empty slice (serializes as [])")
	}
	if len(got.Data) != 0 || got.HasMore || got.NextCursor != "" {
		t.Fatalf("empty list = %+v, want zero items / no more", got)
	}
}

// TestList_SingleItem: one registration projects to a wire item whose state is the
// sealed RegistrationState spelling.
func TestList_SingleItem(t *testing.T) {
	svc, err := NewService(seed(t, "http.example.foo.v1"))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	got := mustList(t, svc, &list.Request{})
	if len(got.Data) != 1 {
		t.Fatalf("len(Data) = %d, want 1", len(got.Data))
	}
	it := got.Data[0]
	if it.ID != "http.example.foo.v1" || it.Kind != "http" || it.Submitter != "cell-a" {
		t.Errorf("item = %+v, want id/kind/submitter http.example.foo.v1/http/cell-a", it)
	}
	if it.State != registry.StateSubmitted().String() {
		t.Errorf("item.State = %q, want %q (sealed)", it.State, registry.StateSubmitted().String())
	}
}

// TestList_Pagination: id-ordered cursor paging across two pages.
func TestList_Pagination(t *testing.T) {
	svc, err := NewService(seed(t, "c1", "c2", "c3"))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	first := mustList(t, svc, &list.Request{Limit: 2})
	if len(first.Data) != 2 || !first.HasMore || first.NextCursor != "c2" {
		t.Fatalf("page1 = %+v, want [c1,c2] hasMore=true nextCursor=c2", first)
	}
	if first.Data[0].ID != "c1" || first.Data[1].ID != "c2" {
		t.Fatalf("page1 ids = %q,%q, want c1,c2", first.Data[0].ID, first.Data[1].ID)
	}
	second := mustList(t, svc, &list.Request{Limit: 2, Cursor: first.NextCursor})
	if len(second.Data) != 1 || second.HasMore || second.Data[0].ID != "c3" {
		t.Fatalf("page2 = %+v, want [c3] hasMore=false", second)
	}
}

// TestList_CursorAtEnd: a cursor equal to the last id yields an empty final page
// (sort.Search lands past the end), not an error or a wrapped page.
func TestList_CursorAtEnd(t *testing.T) {
	svc, err := NewService(seed(t, "c1", "c2", "c3"))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	got := mustList(t, svc, &list.Request{Cursor: "c3"})
	if len(got.Data) != 0 || got.HasMore || got.NextCursor != "" {
		t.Fatalf("cursor-at-end page = %+v, want empty / no more", got)
	}
}

// TestEffectiveLimit pins the default + 500 ceiling (go-standards §列表分页 limit≤500)
// without seeding 500 rows.
func TestEffectiveLimit(t *testing.T) {
	cases := []struct {
		in   int64
		want int
	}{
		{0, defaultLimit}, {-1, defaultLimit}, {1, 1}, {50, 50}, {500, 500}, {501, maxLimit}, {10000, maxLimit},
	}
	for _, tc := range cases {
		if got := effectiveLimit(tc.in); got != tc.want {
			t.Errorf("effectiveLimit(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
