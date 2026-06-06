//go:build integration_cluster

package redis

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/http/idempotency/idempotencytest"
)

// http_idempotency_cluster_real_test.go — full-assembly HTTP idempotency on a
// real Redis Cluster (#1449). Sibling of cluster_real_test.go's
// TestClusterIntegration_IdempotencyClaimer_NoCrossSlot, but for the HTTP store.
//
// The HTTP store's Claim/Record Lua scripts read THREE KEYS per EVAL
// (resp/lease/fp); the hashtag colocation that
// http_idempotency_cluster_slot_test.go proves statically must also hold against
// a live multi-node cluster — that is the "Redis Cluster 必须" multi-pod shape
// for full-assembly. Run out-of-band (see cluster_real_test.go and
// docs/ops/redis-cluster-deployment.md); skips when
// GOCELL_TEST_REDIS_CLUSTER_ADDRS is unset.

// clusterHTTPFP is a valid hex-sha256-shaped fingerprint blob.
const clusterHTTPFP = "1111aaaa2222bbbb3333cccc4444dddd5555eeee6666ffff1111aaaa2222bbbb"

// TestClusterIntegration_HTTPIdempotencyStore_NoCrossSlot is the load-bearing
// cluster test for full-assembly: across business keys whose unhashed bytes
// would scatter slots, Claim → Record → re-Claim must run without CROSSSLOT and
// replay correctly. The "_runtime" namespace mirrors cmd/corebundle's
// httpIdempotencyStoreNamespace (assembly-wide, ADR 202606051000-1449).
func TestClusterIntegration_HTTPIdempotencyStore_NoCrossSlot(t *testing.T) {
	client, cleanup := startCluster(t)
	defer cleanup()

	ctx := context.Background()
	store, err := NewHTTPIdempotencyStore(client, KeyNamespace("_runtime"))
	require.NoError(t, err)

	uniq := time.Now().UnixNano()
	reqNS := "tenant-cluster"
	// Distinct business keys that, without the hashtag, would scatter across
	// many slots (the resp/lease/fp suffixes alone change the hashed bytes).
	keys := []string{
		fmt.Sprintf("user-1\x00POST\x00/api/v1/orders\x00idem-%d-1", uniq),
		fmt.Sprintf("user-2\x00PUT\x00/api/v1/payments/42\x00idem-%d-2", uniq),
		fmt.Sprintf("svc\x00DELETE\x00/api/v1/sessions/9\x00idem-%d-3", uniq),
	}
	for _, k := range keys {
		t.Run(k, func(t *testing.T) {
			state, _, receipt, err := store.Claim(ctx, httpKey(reqNS, k), clusterHTTPFP, testtime.D5min)
			require.NoError(t, err, "Claim must not return CROSSSLOT")
			require.Equal(t, idempotency.ClaimAcquired, state)

			resp := idempotencytest.BuildRecordedResponse(t, 201, []byte(`{"ok":true}`))
			require.NoError(t, receipt.Record(ctx, &resp, testtime.CtxLong),
				"Record must not return CROSSSLOT")

			// Re-Claim returns ClaimDone now that the resp key is set.
			state2, rec2, _, err := store.Claim(ctx, httpKey(reqNS, k), clusterHTTPFP, testtime.D5min)
			require.NoError(t, err, "re-Claim must not return CROSSSLOT")
			require.Equal(t, idempotency.ClaimDone, state2)
			require.NotNil(t, rec2, "replay response must be non-nil")
			require.Equal(t, 201, rec2.Status())
		})
	}
}
