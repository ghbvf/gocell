package redis

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// http_idempotency_cluster_slot_test.go — full-assembly Cluster-safety guard for
// the HTTP idempotency replay store (#1449).
//
// Sibling of idempotency_cluster_slot_test.go (which guards the consumer
// IdempotencyClaimer's 2 keys). The HTTP store's Claim Lua script reads THREE
// KEYS per EVAL — resp/lease/fp — derived as
//
//	<store-ns>:<req-ns>:{<key>}:resp | :lease | :fp
//
// via KeyNamespace.apply + KeyNamespace.applyHashtag (see http_idempotency.go
// Claim). Redis Cluster rejects a multi-KEY EVAL with CROSSSLOT when the keys
// map to different slots. The hashtag wrapping the business key colocates all
// three on one slot regardless of either namespace segment — this is the
// load-bearing invariant for full-assembly idempotency on a Redis Cluster
// backend (the multi-pod deployment shape, see
// docs/ops/redis-cluster-deployment.md). This pure-unit test fails the build the
// moment the derivation drops the hashtag braces or moves them, without needing
// a live cluster.
//
// These tests use the REAL key derivation (KeyNamespace.apply/applyHashtag), so
// if http_idempotency.go changes the key shape the test reflects it.

// httpIdempotencyStoreKeys derives the three Redis keys for one (store-ns,
// req-ns, business-key) exactly as HTTPIdempotencyStore.Claim does.
func httpIdempotencyStoreKeys(storeNS KeyNamespace, reqNS, key string) (resp, lease, fp string) {
	scoped := storeNS.apply(reqNS) // "<store-ns>:<req-ns>"
	resp = KeyNamespace(scoped).applyHashtag(key, "resp")
	lease = KeyNamespace(scoped).applyHashtag(key, "lease")
	fp = KeyNamespace(scoped).applyHashtag(key, "fp")
	return
}

// TestHTTPIdempotency_HashtagKeysShareSlot asserts the resp/lease/fp keys for a
// given request colocate on one Redis Cluster slot. Sample business keys mirror
// the real buildNamespaceKey output shape (subject\x00method\x00path\x00header),
// and the request-namespace varies (tenant id vs the "_notenant" sentinel). The
// "_runtime" store namespace mirrors cmd/corebundle's httpIdempotencyStoreNamespace
// (the assembly-wide owner namespace, ADR 202606051000-1449).
func TestHTTPIdempotency_HashtagKeysShareSlot(t *testing.T) {
	t.Parallel()

	const storeNS KeyNamespace = "_runtime" // assembly-wide owner namespace

	cases := []struct {
		reqNS string
		key   string
	}{
		{"_notenant", "user-1\x00POST\x00/api/v1/orders\x00idem-7c0fa5c4"},
		{"tenant-7c0fa5c4-8a2e-4cab-9f17", "user-2\x00PUT\x00/api/v1/payments/42\x00idem-abc"},
		{"_notenant", "x"}, // single-char edge case
		{"tenant-z", "svc\x00DELETE\x00/api/v1/sessions/9\x00k"},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s|%s", tc.reqNS, tc.key), func(t *testing.T) {
			resp, lease, fp := httpIdempotencyStoreKeys(storeNS, tc.reqNS, tc.key)
			respSlot := crc16Slot(resp)
			leaseSlot := crc16Slot(lease)
			fpSlot := crc16Slot(fp)
			assert.Equal(t, respSlot, leaseSlot,
				"hashtag-wrapped resp/lease keys must hash to the same slot (resp=%d lease=%d) for %q",
				respSlot, leaseSlot, tc.key)
			assert.Equal(t, respSlot, fpSlot,
				"hashtag-wrapped resp/fp keys must hash to the same slot (resp=%d fp=%d) for %q",
				respSlot, fpSlot, tc.key)
		})
	}
}

// TestHTTPIdempotency_WithoutHashtagDifferentSlots — counter-test confirming the
// hashtag is load-bearing: WITHOUT it, the role suffix (resp/lease/fp) changes
// the hashed bytes and scatters the keys across slots, which is exactly the
// CROSSSLOT failure the hashtag prevents. If this regression ever evaporates
// (someone hashes only a colliding prefix), update the sample so the guard keeps
// proving the regression direction.
func TestHTTPIdempotency_WithoutHashtagDifferentSlots(t *testing.T) {
	t.Parallel()

	// "<store-ns>:<req-ns>:<key>:<role>" — the same key shape but with the
	// braces removed, so CRC16 hashes the whole string including the role suffix.
	const prefix = "_runtime:_notenant:user-1\x00POST\x00/api/v1/orders\x00idem"
	respLegacy := prefix + ":resp"
	leaseLegacy := prefix + ":lease"
	assert.NotEqual(t, crc16Slot(respLegacy), crc16Slot(leaseLegacy),
		"un-hashtagged resp/lease keys must NOT colocate; if this assertion ever passes, the test "+
			"sample needs updating to keep guarding the regression direction")
}
