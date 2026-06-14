package audit_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/runtime/audit"
	"github.com/ghbvf/gocell/framework/runtime/audit/ledger"
)

// TestDualWriter_PhysicalIsolation_NoChainFork is the regression test for
// issue #1121. Two writers (auditcore relay + bootstrap observer) operating
// on independent namespaces must each produce a fully-valid chain; the
// aggregator MultiStore must return every committed entry from both chains.
//
// On the pre-fix wire (both writers sharing a single namespace), concurrent
// Appends could fork the HMAC chain. The physical-isolation design removes
// that whole class of failure by construction: separate namespaces mean
// separate MemStore instances with no shared chain state at all.
func TestDualWriter_PhysicalIsolation_NoChainFork(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 5, 27, 10, 0, 0, 0, time.UTC)

	// Build two protocol/store pairs on disjoint namespaces — mirrors the
	// production wire shape established by cellmodules/auditcore/module.go
	// after issue #1121.
	relayNS, err := ledger.ParseNamespaceID("auditcore")
	require.NoError(t, err)
	relayProto, err := ledger.NewProtocol(
		relayNS,
		[]byte("relay-chain-hmac-key-32-bytes!!!"),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	require.NoError(t, err)
	bootstrapProto, err := ledger.NewProtocol(
		audit.BootstrapNamespace(),
		[]byte("bootstrap-chain-hmac-key-32bytes!"),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	require.NoError(t, err)

	clk := clockmock.New(base)
	relayStore, err := ledger.NewMemStore(relayProto, clk)
	require.NoError(t, err)
	bootstrapStore, err := ledger.NewMemStore(bootstrapProto, clk)
	require.NoError(t, err)
	bootstrapWrapped, err := audit.NewBootstrapLedgerStore(bootstrapStore)
	require.NoError(t, err)

	const goroutinesPerChain = 5
	var wg sync.WaitGroup
	wg.Add(2 * goroutinesPerChain)
	errs := make(chan error, 2*goroutinesPerChain)

	// Relay writers: simulate auditcore.appender.Service.HandleEvent calling
	// store.Append on the relay chain.
	for i := 0; i < goroutinesPerChain; i++ {
		i := i
		go func() {
			defer wg.Done()
			entry := &ledger.Entry{
				EventID:   fmt.Sprintf("relay-evt-%d", i),
				EventType: "event.user.created.v1",
				ActorID:   fmt.Sprintf("user:%d", i),
				Timestamp: base.Add(time.Duration(i) * time.Millisecond),
				Payload:   []byte(fmt.Sprintf(`{"i":%d}`, i)),
			}
			if err := relayStore.Append(context.Background(), entry); err != nil {
				errs <- fmt.Errorf("relay append %d: %w", i, err)
			}
		}()
	}

	// Bootstrap writers: simulate the observer calling AppendBootstrapAuthFail
	// on the bootstrap chain.
	for i := 0; i < goroutinesPerChain; i++ {
		i := i
		go func() {
			defer wg.Done()
			if err := audit.AppendBootstrapAuthFail(
				context.Background(),
				bootstrapWrapped, clk,
				uuid.NewString(),
				audit.ReasonWrongCredentials,
				fmt.Sprintf("%064x", i+1), // valid 64-hex clientIpHash (observer hashes the IP)
			); err != nil {
				errs <- fmt.Errorf("bootstrap append %d: %w", i, err)
			}
		}()
	}

	wg.Wait()
	close(errs)
	for e := range errs {
		t.Errorf("concurrent append error: %v", e)
	}

	// Each chain individually must be fully valid.
	relayTail, err := relayStore.Tail(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(goroutinesPerChain), relayTail.SeqNo, "relay chain tail seq")
	assert.Equal(t, int64(goroutinesPerChain), relayTail.EntryCount)

	bootstrapTail, err := bootstrapWrapped.Tail(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(goroutinesPerChain), bootstrapTail.SeqNo, "bootstrap chain tail seq")
	assert.Equal(t, int64(goroutinesPerChain), bootstrapTail.EntryCount)

	validRelay, firstInvalidRelay, err := relayStore.Verify(context.Background(), 1, relayTail.SeqNo)
	require.NoError(t, err)
	assert.True(t, validRelay, "relay chain Verify must pass after concurrent writes; first invalid seq=%d", firstInvalidRelay)

	validBootstrap, firstInvalidBootstrap, err := bootstrapWrapped.Verify(context.Background(), 1, bootstrapTail.SeqNo)
	require.NoError(t, err)
	assert.True(t, validBootstrap, "bootstrap chain Verify must pass after concurrent writes; first invalid seq=%d", firstInvalidBootstrap)

	// MultiStore must surface every committed entry from both chains.
	multi, err := ledger.NewMultiStore(relayStore, bootstrapStore)
	require.NoError(t, err)
	multiVis, _ := tenant.NewRowVisibility(tenant.RowScopeTenant, "")
	all, err := multi.Query(context.Background(), tenant.TenantID(""), multiVis, ledger.AuditFilters{}, query.ListParams{
		Limit: 100,
		Sort:  ledger.QuerySort(),
	})
	require.NoError(t, err)
	assert.Len(t, all, 2*goroutinesPerChain,
		"MultiStore must return every entry from both chains (relay=%d + bootstrap=%d)",
		goroutinesPerChain, goroutinesPerChain)

	// Filter by event type to mirror the ssobff bug reproducer.
	bootstrapOnly, err := multi.Query(context.Background(), tenant.TenantID(""), multiVis,
		ledger.AuditFilters{EventType: "bootstrap.auth.fail"},
		query.ListParams{Limit: 100, Sort: ledger.QuerySort()})
	require.NoError(t, err)
	assert.Len(t, bootstrapOnly, goroutinesPerChain,
		"auditquery filter eventType=bootstrap.auth.fail must return every bootstrap entry")
}
