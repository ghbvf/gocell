package scopedtx

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/tenant"
)

// capturingTxRunner is a TxRunner that also satisfies the tenantScoper
// interface checked by internalCellTxManager.ApplyTenantScope. RunInTx
// delegates to outbox.DemoTxRunner; ApplyTenantScope records its argument.
// Wrap with persistence.WrapForCell to get a sealed CellTxManager.
type capturingTxRunner struct {
	outbox.DemoTxRunner
	capturedTenantStr string
	applyErr          error
	captureMode       bool
}

func (r *capturingTxRunner) ApplyTenantScope(_ context.Context, tenantStr string) error {
	if r.captureMode {
		r.capturedTenantStr = tenantStr
		return nil
	}
	return r.applyErr
}

const testTenant tenant.TenantID = "11111111-1111-1111-1111-111111111111"

// TestDoSetsScopeAndReturnsResult verifies that Do scopes the context to t (so
// a production TxManager would inject SET LOCAL app.tenant_id) and returns
// fn's value. outbox.DemoCellTxManager is a pass-through CellTxManager that
// runs fn with the same context, preserving the WithScope value for the
// assertion.
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
// returned) — RunInTx rolls back on error.
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

// TestApplyScope_PassesTenantString verifies that ApplyScope forwards the
// correct canonical UUID string (t.String()) to the underlying
// CellTxManager.ApplyTenantScope.
func TestApplyScope_PassesTenantString(t *testing.T) {
	t.Parallel()
	runner := &capturingTxRunner{captureMode: true}
	tx := persistence.WrapForCell(runner)
	if err := ApplyScope(context.Background(), tx, testTenant); err != nil {
		t.Fatalf("ApplyScope err = %v, want nil", err)
	}
	if runner.capturedTenantStr != testTenant.String() {
		t.Fatalf("tenantStr = %q, want %q", runner.capturedTenantStr, testTenant.String())
	}
}

// TestApplyScope_PropagatesError verifies that an error from
// CellTxManager.ApplyTenantScope is returned unchanged.
func TestApplyScope_PropagatesError(t *testing.T) {
	t.Parallel()
	wantErr := errcode.New(errcode.KindInternal, errcode.ErrInternal, "no ambient tx")
	runner := &capturingTxRunner{applyErr: wantErr}
	tx := persistence.WrapForCell(runner)
	err := ApplyScope(context.Background(), tx, testTenant)
	if !errors.Is(err, wantErr) {
		t.Fatalf("ApplyScope err = %v, want wrapped sentinel", err)
	}
}
