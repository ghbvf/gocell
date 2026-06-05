package scopedread

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/tenant"
)

const testTenant tenant.TenantID = "11111111-1111-1111-1111-111111111111"

// TestDoSetsScopeAndReturnsResult verifies that Do scopes the context to t (so a
// production TxManager would inject SET LOCAL app.tenant_id) and returns fn's
// value. outbox.DemoCellTxManager is a pass-through CellTxManager that runs fn
// with the same context, preserving the WithScope value for the assertion.
func TestDoSetsScopeAndReturnsResult(t *testing.T) {
	t.Parallel()
	var seenScope tenant.TenantID
	var seenOK bool
	got, err := Do(context.Background(), outbox.DemoCellTxManager(), testTenant,
		func(txCtx context.Context) (int, error) {
			seenScope, seenOK = tenant.ScopeFromContext(txCtx)
			return 42, nil
		})
	if err != nil {
		t.Fatalf("Do err = %v, want nil", err)
	}
	if got != 42 {
		t.Fatalf("Do result = %d, want 42", got)
	}
	if !seenOK || seenScope != testTenant {
		t.Fatalf("scope inside fn = (%q,%v), want (%q,true)", seenScope, seenOK, testTenant)
	}
}

// TestDoPropagatesError confirms fn's error surfaces (and the zero value is
// returned) — RunInTx rolls back, which is harmless for a read.
func TestDoPropagatesError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("boom")
	got, err := Do(context.Background(), outbox.DemoCellTxManager(), testTenant,
		func(context.Context) (string, error) {
			return "ignored", sentinel
		})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Do err = %v, want sentinel", err)
	}
	if got != "" {
		t.Fatalf("Do result on error = %q, want zero value", got)
	}
}
