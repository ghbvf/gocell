//go:build integration

package redis

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	idemhttp "github.com/ghbvf/gocell/runtime/http/idempotency"
	"github.com/ghbvf/gocell/runtime/http/idempotency/idempotencytest"
)

// http_idempotency_assembly_scope_test.go — behavioral proof of full-assembly
// HTTP idempotency scope (#1449): a request recorded by one pod is replayed by
// another pod that shares the same Redis backend.
//
// "Two pods" is modeled as two independent *HTTPIdempotencyStore instances over
// the same Redis. This is a faithful cross-pod model because the store holds NO
// in-memory replay state — all Claim/Record/Release state lives in Redis (that
// premise is frozen structurally by archtest
// HTTP-IDEMPOTENCY-STORE-STATELESS-FROZEN-01). Two store instances are therefore
// interchangeable with two pods; the shared connection pool is irrelevant to
// where the replay state lives. The "_runtime" namespace both stores use mirrors
// cmd/corebundle's httpIdempotencyStoreNamespace — the assembly-wide owner
// namespace established by ADR 202606051000-1449. Together with the node-agnostic key
// derivation (archtest HTTP-IDEMPOTENCY-KEY-NODE-AGNOSTIC-01, which guarantees
// both pods derive the same (ns,key) for the same logical request), these tests
// close the full-assembly claim.

const (
	// asmStoreNS is the assembly-wide owner namespace (mirrors
	// cmd/corebundle.httpIdempotencyStoreNamespace).
	asmStoreNS KeyNamespace = "_runtime"
	// asmReqNS is the request-time tenant namespace, identical across pods.
	asmReqNS = "tenant-assembly"
	// asmFP / asmFPAlt are valid hex-sha256-shaped fingerprint blobs.
	asmFP    = "1111aaaa2222bbbb3333cccc4444dddd5555eeee6666ffff1111aaaa2222bbbb"
	asmFPAlt = "9999aaaa8888bbbb7777cccc6666dddd5555eeee4444ffff3333aaaa2222bbbb"
)

// newTwoPodStores returns two independent HTTPIdempotencyStore instances backed
// by the same Redis, modeling two pods of one assembly (see file godoc), plus a
// cleanup func.
//
// startRedis launches a fresh testcontainers Redis per call, so each test
// function gets an isolated Redis instance — the hard-coded key prefixes
// (asm-replay / asm-busy / asm-fp) plus the per-test t.Name() suffix need not
// guard against cross-test residue; there is none.
func newTwoPodStores(t *testing.T) (podA, podB *HTTPIdempotencyStore, cleanup func()) {
	t.Helper()
	client, cleanup := startRedis(t)
	a, err := NewHTTPIdempotencyStore(client, asmStoreNS)
	require.NoError(t, err, "construct pod-A store")
	b, err := NewHTTPIdempotencyStore(client, asmStoreNS)
	require.NoError(t, err, "construct pod-B store")
	return a, b, cleanup
}

// TestIntegration_HTTPIdempotencyStore_AssemblyScope_CrossPodReplay proves the
// load-bearing full-assembly guarantee: a response recorded by pod A is replayed
// by pod B (different store instance, same Redis) with status + body intact.
func TestIntegration_HTTPIdempotencyStore_AssemblyScope_CrossPodReplay(t *testing.T) {
	podA, podB, cleanup := newTwoPodStores(t)
	defer cleanup()

	ctx := context.Background()
	key := "asm-replay\x00POST\x00/api/v1/orders\x00" + t.Name()

	// Pod A acquires and records.
	stateA, _, receiptA, err := podA.Claim(ctx, asmReqNS, key, asmFP, testtime.D5min)
	require.NoError(t, err, "pod-A Claim")
	require.Equal(t, idempotency.ClaimAcquired, stateA, "pod-A must acquire a fresh key")

	wantBody := []byte(`{"orderId":"asm-1"}`)
	resp := idempotencytest.BuildRecordedResponse(t, 201, wantBody)
	require.NoError(t, receiptA.Record(ctx, &resp, testtime.CtxLong), "pod-A Record")

	// Pod B (different store instance) replays the same (ns,key,fp).
	stateB, recB, _, err := podB.Claim(ctx, asmReqNS, key, asmFP, testtime.D5min)
	require.NoError(t, err, "pod-B Claim")
	require.Equal(t, idempotency.ClaimDone, stateB,
		"pod-B must replay (ClaimDone) a key pod-A recorded — assembly-wide dedup")
	require.NotNil(t, recB, "pod-B replay response must be non-nil")
	require.Equal(t, 201, recB.Status(), "replayed status must round-trip cross-pod")
	require.True(t, bytes.Equal(recB.Body(), wantBody), "replayed body must round-trip cross-pod")
	require.Equal(t, "application/json", recB.Header().Get("Content-Type"),
		"replayed header must round-trip cross-pod")
}

// TestIntegration_HTTPIdempotencyStore_AssemblyScope_CrossPodBusy proves the
// in-flight lease taken by pod A blocks pod B (ClaimBusy) — concurrent dedup
// works across pods, not just within one.
func TestIntegration_HTTPIdempotencyStore_AssemblyScope_CrossPodBusy(t *testing.T) {
	podA, podB, cleanup := newTwoPodStores(t)
	defer cleanup()

	ctx := context.Background()
	key := "asm-busy\x00PUT\x00/api/v1/payments\x00" + t.Name()

	stateA, _, _, err := podA.Claim(ctx, asmReqNS, key, asmFP, testtime.D5min)
	require.NoError(t, err, "pod-A Claim")
	require.Equal(t, idempotency.ClaimAcquired, stateA, "pod-A must acquire")

	// Pod B sees the in-flight lease held by pod A.
	stateB, _, _, err := podB.Claim(ctx, asmReqNS, key, asmFP, testtime.D5min)
	require.NoError(t, err, "pod-B Claim")
	require.Equal(t, idempotency.ClaimBusy, stateB,
		"pod-B must observe pod-A's in-flight lease (ClaimBusy) — cross-pod concurrency lock")
}

// TestIntegration_HTTPIdempotencyStore_AssemblyScope_CrossPodFingerprintMismatch
// proves the same-key/different-body guard holds across pods: a body fingerprint
// recorded by pod A is enforced when pod B presents a different one.
func TestIntegration_HTTPIdempotencyStore_AssemblyScope_CrossPodFingerprintMismatch(t *testing.T) {
	podA, podB, cleanup := newTwoPodStores(t)
	defer cleanup()

	ctx := context.Background()
	key := "asm-fp\x00POST\x00/api/v1/orders\x00" + t.Name()

	stateA, _, receiptA, err := podA.Claim(ctx, asmReqNS, key, asmFP, testtime.D5min)
	require.NoError(t, err, "pod-A Claim")
	require.Equal(t, idempotency.ClaimAcquired, stateA, "pod-A must acquire")
	resp := idempotencytest.BuildRecordedResponse(t, 200, []byte(`{"ok":true}`))
	require.NoError(t, receiptA.Record(ctx, &resp, testtime.CtxLong), "pod-A Record")

	// Pod B replays the same key with a different body fingerprint → mismatch.
	_, _, _, err = podB.Claim(ctx, asmReqNS, key, asmFPAlt, testtime.D5min)
	require.Error(t, err, "pod-B Claim with mismatched fingerprint must error")
	require.True(t, errors.Is(err, idemhttp.ErrFingerprintMismatch),
		"cross-pod fingerprint mismatch must surface ErrFingerprintMismatch; got %v", err)
	var fpErr *idemhttp.FingerprintMismatchError
	require.True(t, errors.As(err, &fpErr), "must be *FingerprintMismatchError; got %T", err)
	require.Equal(t, asmFP, fpErr.Stored, "stored fingerprint (recorded by pod-A) must round-trip cross-pod")
}
