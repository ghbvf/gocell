package health

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
)

// TestReadyz_Singleflight_DedupsConcurrentRequests verifies that a burst of
// concurrent /readyz calls share one probe execution. The probe's call
// counter must stay small (≤ 2) even with many concurrent HTTP requests,
// and every response body must carry a consistent aggregate status.
//
// The tolerance of 2 (rather than 1) accounts for a follow-up request that
// lands after the first call's probe pass has just completed — singleflight
// only coalesces in-flight duplicates, not back-to-back requests.
func TestReadyz_Singleflight_DedupsConcurrentRequests(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test-sf", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	var callCount atomic.Int32
	h := New(asm, WithVerboseDisabled(), WithDeadline(2*time.Second))
	// Cooperative slow probe so the first call holds the singleflight slot
	// open long enough to absorb the rest of the burst.
	h.RegisterChecker("slow", func(ctx context.Context) error {
		callCount.Add(1)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(80 * time.Millisecond):
			return nil
		}
	})

	const concurrency = 16
	var wg sync.WaitGroup
	wg.Add(concurrency)
	bodies := make([][]byte, concurrency)
	for i := 0; i < concurrency; i++ {
		i := i
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			h.ReadyzHandler().ServeHTTP(rec, req)
			bodies[i] = rec.Body.Bytes()
		}()
	}
	wg.Wait()

	assert.LessOrEqualf(t, callCount.Load(), int32(2),
		"singleflight must dedup concurrent probes; got %d probe executions for %d requests",
		callCount.Load(), concurrency)

	// Every response must parse and carry a status field — exact value not
	// asserted because singleflight guarantees all sharers see the same
	// result per execution.
	for i, body := range bodies {
		var parsed map[string]any
		require.NoErrorf(t, json.Unmarshal(body, &parsed), "body[%d] = %s", i, string(body))
		_, ok := parsed["status"]
		assert.Truef(t, ok, "response %d missing status field: %s", i, string(body))
	}
}

// TestReadyz_Singleflight_SeparateKeysForVerboseVsAggregate guards against a
// regression where verbose and non-verbose bursts share the same
// singleflight key and return each other's response shape.
func TestReadyz_Singleflight_SeparateKeysForVerboseVsAggregate(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test-sf-keys", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm)
	h.SetVerboseToken(testVerboseToken)
	h.RegisterChecker("db", func(_ context.Context) error { return nil })

	plainRec := httptest.NewRecorder()
	h.ReadyzHandler().ServeHTTP(plainRec,
		httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, http.StatusOK, plainRec.Code)
	var plain map[string]any
	require.NoError(t, json.Unmarshal(plainRec.Body.Bytes(), &plain))
	_, plainHasCells := plain["cells"]
	assert.False(t, plainHasCells, "plain response must not leak verbose cells")

	verboseRec := httptest.NewRecorder()
	h.ReadyzHandler().ServeHTTP(verboseRec, newVerboseRequest("/readyz?verbose=true"))
	require.Equal(t, http.StatusOK, verboseRec.Code)
	var verbose map[string]any
	require.NoError(t, json.Unmarshal(verboseRec.Body.Bytes(), &verbose))
	_, verboseHasCells := verbose["cells"]
	assert.True(t, verboseHasCells, "verbose response must include cells")
}
