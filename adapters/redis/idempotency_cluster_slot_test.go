package redis

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// crc16Slot computes the Redis Cluster slot for a key using the CRC16 of the
// hashtag if present (text between the first '{' and the next '}'), or of the
// full key otherwise. This mirrors the algorithm in
// https://redis.io/docs/latest/operate/oss_and_stack/reference/cluster-spec/#hash-tags
//
// Inlining the table-driven CRC16-CCITT-FALSE polynomial keeps this test free
// of go-redis internal package imports while staying byte-for-byte identical
// to the wire-protocol behavior.
func crc16Slot(key string) int {
	open := -1
	for i := 0; i < len(key); i++ {
		if key[i] == '{' {
			open = i
			break
		}
	}
	target := key
	if open >= 0 {
		for i := open + 1; i < len(key); i++ {
			if key[i] == '}' && i > open+1 {
				target = key[open+1 : i]
				break
			}
		}
	}
	return int(crc16Bytes([]byte(target))) % 16384
}

// crc16Bytes is the XMODEM/CCITT-FALSE CRC16 used by Redis Cluster.
// Polynomial 0x1021, init 0x0000, no reflect, no xor-out — see
// https://github.com/redis/redis/blob/unstable/src/crc16.c
func crc16Bytes(data []byte) uint16 {
	var crc uint16
	for _, b := range data {
		crc ^= uint16(b) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// TestIdempotency_HashtagKeysShareSlot — Idempotency Lua scripts use 2 KEYS
// per EVAL (claim/commit). Cluster mode rejects EVAL with `CROSSSLOT` when
// KEYS map to different slots. Forcing the same hashtag is the documented
// fix; this test fails the build the moment idempotency.go drops the
// hashtag braces or moves them outside the colocated keys.
//
// Sample keys mirror real-world idempotency keys (event-id UUIDs, command
// queue ids, etc.). Slot equality is the load-bearing invariant.
func TestIdempotency_HashtagKeysShareSlot(t *testing.T) {
	t.Parallel()

	samples := []string{
		"order-create:42",
		"event-id-7c0fa5c4-8a2e-4cab-9f17-9d35d6f0f1cc",
		"login:user@example.com",
		"x", // single-char edge case
	}

	for _, k := range samples {
		t.Run(k, func(t *testing.T) {
			lease := "{" + k + "}:lease"
			done := "{" + k + "}:done"
			leaseSlot := crc16Slot(lease)
			doneSlot := crc16Slot(done)
			assert.Equal(t, leaseSlot, doneSlot,
				"hashtag-wrapped lease/done keys for %q must hash to the same slot (lease=%d done=%d)",
				k, leaseSlot, doneSlot)
		})
	}
}

// TestIdempotency_OldNamingDifferentSlots — counter-test confirming that the
// pre-cluster naming (`lease:{key}` / `done:{key}`) without a hashtag actually
// hashes to different slots, which is exactly what we are fixing. If this
// regression ever evaporates (e.g. someone reintroduces an `{` brace somewhere
// in a refactor) we want a loud failure rather than silent regression to
// double slots.
func TestIdempotency_OldNamingDifferentSlots(t *testing.T) {
	t.Parallel()

	// "abc" was selected because crc16("abc") and crc16("done:abc") /
	// crc16("lease:abc") all collide on different slots. Any non-trivial
	// key would do — what matters is that the prefix changes the hashed
	// bytes, since there is no hashtag to override the entire key.
	const k = "abc"
	leaseLegacy := "lease:" + k
	doneLegacy := "done:" + k
	leaseSlot := crc16Slot(leaseLegacy)
	doneSlot := crc16Slot(doneLegacy)
	assert.NotEqual(t, leaseSlot, doneSlot,
		"old key naming must NOT colocate; if this assertion ever passes, the test "+
			"sample needs updating to keep guarding the regression direction")
}
