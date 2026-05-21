//go:build integration

package integration

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/auditcore/auditcoretest"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// TestJAuditlogintrailHashChain implements journeys/J-auditlogintrail.yaml
// passCriteria "审计记录写入 hash chain" — checkRef
// journey.J-auditlogintrail.hash-chain. Same lifecycle / Docker-free /
// governance VERIFY-06 contract as TestJAuditlogintrailEventConsume in this
// package; see that test's docstring for the full layer-compromise rationale.
//
// The criterion asserts that after auditcore consumes a session lifecycle
// event, an entry is durably written to the tamper-evident hash chain and
// the chain verifies clean. We drive the same auditappendsession
// subscription seam, then probe ledger.Store directly:
//
//   - Tail.SeqNo == 1 — exactly one entry was appended for the one consumed
//     event. Anything else points to either duplicate appends (idempotency
//     leak) or a wholly-missed consumer path.
//   - Verify(1, 1) returns valid=true with firstInvalidSeq=-1 — the HMAC
//     chain link computed by ledger.Protocol.ComputeHash round-trips
//     through Store.Verify, which is the on-startup integrity check
//     auditcore runs via RestartRecoveryStrictTailVerify. Asserting it
//     immediately after Append exercises the same code path that prevents
//     a tampered chain from coming back online.
//
// We do not chain a second Append here: the criterion is about a single
// entry's hash chain integrity, and the auditappendsession service's
// content-fingerprint idempotency (errcode.ErrAuditLedgerAlreadyExists ->
// Ack treated as idempotent success) would mask a second send anyway. The
// multi-entry chain-length property is covered by ledger/storetest's
// conformance suite.
func TestJAuditlogintrailHashChain(t *testing.T) {
	t.Parallel()
	handler, store, ctx := auditcoretest.BuildAuditcoreChain(t)
	entry := auditcoretest.CanonicalSessionCreatedEntry("sess-j-auditlogintrail", "usr-j-auditlogintrail")

	result := handler(ctx, entry)
	require.Equalf(t, outbox.DispositionAck, result.Disposition,
		"auditcore must Ack before hash-chain assertions are meaningful; "+
			"got disposition=%v error=%v",
		result.Disposition, result.Err)

	tail, err := store.Tail(ctx)
	require.NoError(t, err, "store.Tail after successful Append")
	require.Equal(t, int64(1), tail.SeqNo,
		"exactly one entry must be appended for one consumed session.created event")
	require.Equal(t, int64(1), tail.EntryCount,
		"EntryCount must agree with SeqNo for a single-append chain")
	require.NotEmpty(t, tail.PrevHash,
		"tail.PrevHash (HMAC of the appended entry) must be non-empty; "+
			"empty hash would mean ledger.Protocol.ComputeHash silently degenerated")

	// fromSeq=1: this MemStore is freshly built per test, so the first (and
	// only) appended entry has SeqNo=1 (ledger sequences are 1-based, not
	// 0-based — see runtime/audit/ledger storetest conformance).
	valid, firstInvalidSeq, err := store.Verify(ctx, 1, tail.SeqNo)
	require.NoError(t, err, "store.Verify after single Append")
	require.True(t, valid,
		"hash chain must verify clean immediately after Append; "+
			"firstInvalidSeq=%d (-1 means clean)", firstInvalidSeq)
	require.Equal(t, int64(-1), firstInvalidSeq,
		"firstInvalidSeq must be -1 when the chain is intact")
}
