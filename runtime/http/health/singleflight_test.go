package health

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// TestReadyz_Singleflight_DedupsConcurrentRequests verifies that a burst of
// concurrent /readyz calls share one probe execution. The probe execution
// counter must stay strictly less than the number of requests even with a
// large concurrent burst.
//
// Test shape follows golang.org/x/sync/singleflight TestDoDupSuppress:
//   - WaitGroup barrier so every goroutine is parked at the same instant
//     before the probe is invoked
//   - atomic counter on probe entry
//   - loose assertion `0 < calls < n` rather than a magic tolerance — the
//     exact call count depends on scheduling and is not part of the
//     contract; what matters is that dedup actually happens.
//
// ref: golang.org/x/sync/singleflight/singleflight_test.go#TestDoDupSuppress
func TestReadyz_Singleflight_DedupsConcurrentRequests(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test-sf", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	var callCount atomic.Int32
	// probeRelease gates every probe execution so the first goroutine into
	// singleflight blocks and every subsequent caller finds the slot held.
	// Closing the channel at the end of the barrier phase lets all in-flight
	// probes drain in parallel. Without this, the probe's own timer could
	// race the barrier release and the dedup window would close early.
	probeRelease := make(chan struct{})
	h := New(asm, clock.Real(), WithVerboseDisabled(), WithDeadline(testtime.D2s))
	require.NoError(t, h.RegisterChecker("slow", func(ctx context.Context) error {
		callCount.Add(1)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-probeRelease:
			return nil
		}
	}))

	const concurrency = 16
	// readyWG signals that every goroutine has reached the barrier.
	// doneWG waits for the HTTP handler return once the burst is released.
	var readyWG, doneWG sync.WaitGroup
	readyWG.Add(concurrency)
	doneWG.Add(concurrency)
	release := make(chan struct{})

	bodies := make([][]byte, concurrency)
	for i := range concurrency {
		go func() {
			defer doneWG.Done()
			readyWG.Done()
			<-release // all goroutines cross this gate simultaneously
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			h.ReadyzHandler().ServeHTTP(rec, req)
			bodies[i] = rec.Body.Bytes()
		}()
	}
	readyWG.Wait() // every goroutine parked at the barrier
	close(release) // fire them all at once — maximizes the dedup window

	// Wait for the leader to enter the probe (callCount == 1) using a
	// happens-before signal rather than a fixed-time sleep. Once the leader
	// is parked on probeRelease, every subsequent caller into singleflight
	// joins the in-flight slot deterministically. Bound the spin so a stuck
	// scheduler still fails the test instead of hanging.
	deadline := time.Now().Add(testtime.D2s)
	for callCount.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("leader probe never started (callCount=%d)", callCount.Load())
		}
		runtime.Gosched()
	}
	close(probeRelease)
	doneWG.Wait()

	calls := callCount.Load()
	assert.Greaterf(t, calls, int32(0),
		"probe must have been invoked at least once; got %d", calls)
	assert.Lessf(t, calls, int32(concurrency),
		"singleflight must collapse %d concurrent probes to strictly fewer executions; got %d",
		concurrency, calls)

	// Every response must parse and carry a status field — exact value not
	// asserted because singleflight guarantees all sharers see the same
	// result per execution. PR-A35 wraps success responses in the
	// {"data": {...}} envelope, so dig one layer in.
	for i, body := range bodies {
		var parsed map[string]any
		require.NoErrorf(t, json.Unmarshal(body, &parsed), "body[%d] = %s", i, string(body))
		data, ok := parsed["data"].(map[string]any)
		require.Truef(t, ok, "response %d missing data envelope: %s", i, string(body))
		_, ok = data["status"]
		assert.Truef(t, ok, "response %d missing status field under data: %s", i, string(body))
	}
}

// TestReadyz_Singleflight_SeparateKeysForVerboseVsAggregate guards against a
// regression where verbose and non-verbose bursts share the same
// singleflight key and return each other's response shape.
func TestReadyz_Singleflight_SeparateKeysForVerboseVsAggregate(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test-sf-keys", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm, clock.Real())
	h.SetVerboseToken(testVerboseToken)
	require.NoError(t, h.RegisterChecker("db", func(_ context.Context) error { return nil }))

	plainRec := httptest.NewRecorder()
	h.ReadyzHandler().ServeHTTP(plainRec,
		httptest.NewRequest(http.MethodGet, "/readyz", nil))
	require.Equal(t, http.StatusOK, plainRec.Code)
	var plain map[string]any
	require.NoError(t, json.Unmarshal(plainRec.Body.Bytes(), &plain))
	plainData, ok := plain["data"].(map[string]any)
	require.True(t, ok, "plain response must carry data envelope")
	_, plainHasCells := plainData["cells"]
	assert.False(t, plainHasCells, "plain response must not leak verbose cells under data")

	verboseRec := httptest.NewRecorder()
	h.ReadyzHandler().ServeHTTP(verboseRec, newVerboseRequest("/readyz?verbose=true"))
	require.Equal(t, http.StatusOK, verboseRec.Code)
	var verbose map[string]any
	require.NoError(t, json.Unmarshal(verboseRec.Body.Bytes(), &verbose))
	verboseData, ok := verbose["data"].(map[string]any)
	require.True(t, ok, "verbose response must carry data envelope")
	_, verboseHasCells := verboseData["cells"]
	assert.True(t, verboseHasCells, "verbose response must include cells under data")
}
