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
//	HMAC-SHA256(key, json.Marshal(auditHashInput{...}))
//
// where auditHashInput is a typed struct with 11 fields (prev_hash, event_id,
// event_type, actor_id, subject_id, tenant_id, session_id, correlation_id,
// occurred_at_unix_nano, timestamp_unix_nano, payload) and json.Marshal
// serializes fields in source-declaration order (deterministic). The result
// is hex-encoded as lowercase hex.
//
// Using canonical JSON encoding eliminates the field-boundary collision risk
// that existed in the previous pipe-separated fmt.Sprintf format: JSON's
// quote/escape handling makes any field value safe regardless of its bytes
// (F3+F6, PR #1218 W1.2). Payload is a []byte field, encoded by the JSON
// encoder as a base64 string.
//
// ref: google/trillian storage/leafdata.go — typed canonical input struct for
// log-leaf HMAC; RFC 8785 JCS (struct-order determinism sufficient here).
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
// IdempotencyContentFingerprint uses the entry's EventID as the sole
// idempotency key. EventID (the outbox.Entry UUID) is stable across
// at-least-once redeliveries while Timestamp/Payload may vary per attempt —
// including them would defeat dedup. Duplicate appends return
// ErrAuditLedgerAlreadyExists.
//
// The DB-level UNIQUE INDEX on (namespace, event_id) (migration 021) is the
// second-line guard against concurrent bypass of this application-level check.
//
// ref: ADR 202605101800-adr-audit-ledger-protocol §D3 F-CR-2
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
