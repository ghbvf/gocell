// Package ledger provides a typed Protocol primitive and Store interface for
// append-only, HMAC-linked audit chains.
//
// # Protocol Paradigm
//
// The package follows the typed-Go-heavy paradigm introduced in
// runtime/auth/session (S1+S2): protocol decisions are captured in a strongly-
// typed *Protocol value assembled at composition root. Cells consume an
// injected *Protocol; they never construct one. The AUDIT-LEDGER-PROTOCOL-
// COMPOSITION-ROOT-01 archtest in tools/archtest/ enforces this boundary.
//
// # Sealed Interface Markers
//
// Two sealed interfaces prevent external packages from declaring new
// protocol shapes without modifying this package:
//
//   - RestartRecoveryMode — implemented only by RestartRecoveryStrictTailVerify.
//   - IdempotencyMode — implemented only by IdempotencyContentFingerprint.
//
// The marker methods (restartRecoveryModeOK, idempotencyModeOK) are unexported;
// external types that attempt to implement these interfaces fail to compile.
//
// # Hash Chain Algorithm
//
// Each entry's Hash is computed as:
//
//	HMAC-SHA256(key, prevHash|eventID|eventType|actorID|UnixNano|payload)
//
// encoded as lowercase hex. The algorithm is byte-for-byte identical to
// cells/auditcore/internal/domain/hashchain.go to preserve chain continuity
// when the PG-backed store (S8+) replaces the legacy in-cell chain.
//
// # Restart Recovery
//
// RestartRecoveryStrictTailVerify requires the store to verify the tail of
// the existing chain before accepting new entries after a restart. For
// MemStore this is a no-op (ephemeral state). For the PG store (S8+) it
// translates to a tail-integrity SELECT + verify before the first Append.
//
// ref: google/trillian log/sequencer.go — IntegrateBatch verifies tree
// integrity before accepting new leaves.
//
// # Idempotency
//
// IdempotencyContentFingerprint uses a SHA-256 digest of the entry fields
// (eventID + eventType + actorID + UnixNano(timestamp) + payload) as the
// idempotency key. Duplicate appends return ErrAuditLedgerAlreadyExists.
//
// ref: google/trillian types/logroot.go — LeafIdentityHash content-addressed
// deduplication.
//
// # Strict Payload Validation
//
// All Append calls validate that the payload is valid JSON (or nil). This
// strict mode is always on — there is no toggle Option. Producers must
// ensure their payloads are well-formed before calling Append.
//
// # ADR Reference
//
// docs/architecture/202605101800-adr-audit-ledger-protocol.md
package ledger
