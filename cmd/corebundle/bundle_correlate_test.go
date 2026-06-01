package main

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

// fakeCorrelateQueryStore is a minimal no-op implementation of ledger.QueryStore
// for buildCorrelateOption unit tests. It does not import any adapter.
type fakeCorrelateQueryStore struct{}

func (f *fakeCorrelateQueryStore) Query(_ context.Context, _ ledger.AuditFilters, _ query.ListParams) ([]*ledger.Entry, error) {
	return nil, nil
}

var _ ledger.QueryStore = (*fakeCorrelateQueryStore)(nil)

// TestBuildCorrelateOption_NilStore_Error asserts that passing a bare-nil
// ledger.QueryStore to buildCorrelateOption returns a non-nil error. auditcore
// is a fixed corebundle cell; a nil store is a composition wiring bug.
func TestBuildCorrelateOption_NilStore_Error(t *testing.T) {
	opt, err := buildCorrelateOption(nil)
	if err == nil {
		t.Fatal("expected non-nil error for nil store, got nil")
	}
	if opt != nil {
		t.Fatalf("expected nil option on error, got non-nil")
	}
}

// TestBuildCorrelateOption_TypedNilStore_Error asserts that a typed-nil
// ledger.QueryStore (non-nil interface holding a nil pointer) is also rejected.
// This guards against the typed-nil footgun that bare == nil checks miss.
func TestBuildCorrelateOption_TypedNilStore_Error(t *testing.T) {
	var store ledger.QueryStore = (*fakeCorrelateQueryStore)(nil)
	opt, err := buildCorrelateOption(store)
	if err == nil {
		t.Fatal("expected non-nil error for typed-nil store, got nil")
	}
	if opt != nil {
		t.Fatalf("expected nil option on error, got non-nil")
	}
}

// TestBuildCorrelateOption_OK asserts that a valid ledger.QueryStore produces a
// non-nil bootstrap.Option and nil error.
func TestBuildCorrelateOption_OK(t *testing.T) {
	store := &fakeCorrelateQueryStore{}
	opt, err := buildCorrelateOption(store)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opt == nil {
		t.Fatal("expected non-nil bootstrap.Option, got nil")
	}
}
