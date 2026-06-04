// Package audit centralizes bootstrap-period audit-chain helpers used by
// auditcore's auditappendbootstrap slice handler.
//
// The bootstrap auth-fail event flow is:
//
//  1. runtime/auth.NewBootstrapMiddleware calls the observer closure on 401/429.
//  2. The observer (constructed in cellmodules/accesscore) calls
//     cells/accesscore/slices/setup.Service.RecordBootstrapAuthFail, which emits
//     event.auth.bootstrap-failed.v1 via outbox (inside a transaction).
//  3. auditcore's auditappendbootstrap slice consumes the event and calls
//     AppendBootstrapAuthFail → BootstrapLedgerStore.Append.
//
// The emit path is covered by EMIT-DECL-COVER-01 + the event contract.
// The MODULE-PROVIDE-NO-VALUE-HANDOFF-01 archtest ensures the composition root
// cannot pass a BootstrapLedgerStore across cell boundaries via ModuleExports.
// The bootstrap namespace is physically isolated from the relay chain via
// BootstrapNamespace() (ADR 202605270230).
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

// Bootstrap auth-fail reason values are the authoritative source for valid
// reason strings. These constants mirror the literal vocabulary used by
// runtime/auth/bootstrap.go and are re-declared in
// cells/accesscore/internal/dto (which cannot import runtime/audit) and in
// cells/accesscore/slices/setup (reason whitelist for RecordBootstrapAuthFail).
//
// The event contract (event.auth.bootstrap-failed.v1 payload.schema.json)
// only constrains reason to type string — it does NOT express the closed
// {missing_header, wrong_credentials, rate_limited} set (no JSON-schema enum).
// The closed set is maintained at runtime by validBootstrapAuthFailReasons
// (consumer) + the setup reason whitelist (producer), and the cross-package
// equivalence of these declared sets is enforced statically by the
// BOOTSTRAP-REASON-SET-EQUIVALENCE archtest. Schema = type guard; archtest +
// whitelists = closed-set guard.
const (
	ReasonMissingHeader    = "missing_header"
	ReasonWrongCredentials = "wrong_credentials"
	ReasonRateLimited      = "rate_limited"

	// bootstrapAuthFailEventType is the ledger EventType label. Point-dotted
	// shape matches existing ledger.Entry godoc style ("user.login", "config.updated").
	bootstrapAuthFailEventType = "bootstrap.auth.fail"

	// bootstrapAuthFailActorID identifies the platform itself as the actor —
	// no real user is involved at bootstrap auth time.
	bootstrapAuthFailActorID = "system:bootstrap"
)

// validBootstrapAuthFailReasons is the whitelist that gates payload entry.
// Centralizing the set as a map keeps O(1) membership while making the three
// public constants the single source of truth.
var validBootstrapAuthFailReasons = map[string]struct{}{
	ReasonMissingHeader:    {},
	ReasonWrongCredentials: {},
	ReasonRateLimited:      {},
}

// bootstrapAuthFailPayload is the JSON envelope persisted in ledger.Entry.Payload.
// Field names are camelCase per go-standards JSON convention; future fields
// must be optional/additive (v1 schema-evolution rule for ledger payloads).
//
// ClientIPHash is the keyed, non-reversible hash of the client IP (#1488): the
// ledger stores the hash, never the plaintext PII, so it cannot leak through the
// replayable outbox/broker/DLX path or the auditquery egress.
type bootstrapAuthFailPayload struct {
	Reason       string `json:"reason"`
	ClientIPHash string `json:"clientIpHash"`
}

// AppendBootstrapAuthFail constructs a bootstrap.auth.fail ledger entry and
// persists it via the sealed *BootstrapLedgerStore. clientIPHash is the keyed,
// non-reversible hash of the client IP (#1488); it is empty when no IP was
// available (e.g. health probes, or the request did not flow through middleware
// that sets ctxkeys.RealIP).
//
// eventID is the stable source-event identity (the consuming slice passes
// outbox.Entry.ID()). It becomes the ledger EventID, which is the SOLE
// idempotency key (ledger.IdempotencyContentFingerprint hashes EventID only,
// see runtime/audit/ledger/mem_store.go). Using the stable entry ID — rather
// than a freshly minted uuid.NewString() — is what makes at-least-once
// redelivery idempotent: a redelivered event carries the same outbox UUID, so
// the second Append collapses to ErrAuditLedgerAlreadyExists instead of
// appending a duplicate ledger row. This mirrors the relay-chain appender
// (cells/auditcore/internal/appender) which also keys on entry.ID().
// Callers must treat ErrAuditLedgerAlreadyExists (KindConflict) as an
// idempotent SUCCESS and Ack — see auditappendbootstrap.Service.HandleEvent.
//
// Consistency level: L1 LocalTx — store.Append runs in a single PG transaction
// with the advisory-lock-fenced chain tail read; no outbox emit follows, so
// L2 atomicity coverage does not apply. The bootstrap chain is physically
// distinct from the auditcore relay chain via BootstrapNamespace(): the
// *BootstrapLedgerStore typed handle makes accidental injection of the
// auditcore-namespace store a compile error rather than a runtime fork
// (issue #1121 / ADR 202605270230 — Vault per-device-salt + Trillian per-tree
// partition patterns).
//
// Payload JSON shape (camelCase, additive evolution):
//
//	{"reason": "<one of: missing_header | wrong_credentials | rate_limited>",
//	 "clientIpHash": "<keyed HMAC hex hash of the client IP, or empty>"}
//
// Errors:
//   - ErrValidationFailed when store / clock is nil, eventID is empty, or
//     reason is not in {missing_header, wrong_credentials, rate_limited}.
//   - The wrapped Append error otherwise — most commonly
//     ErrAuditLedgerAlreadyExists (idempotent replay; callers Ack) or a
//     chain-write failure (transient; callers Requeue). The wrap preserves the
//     inner *errcode.Error code so callers can classify via errors.As.
func AppendBootstrapAuthFail(
	ctx context.Context, store *BootstrapLedgerStore, clk clock.Clock,
	eventID, reason, clientIPHash string,
) error {
	if store == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit: AppendBootstrapAuthFail requires non-nil *BootstrapLedgerStore",
			errcode.WithInternal(errcode.InternalAttr("_", "nil store")))
	}
	if validation.IsNilInterface(clk) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit: AppendBootstrapAuthFail requires non-nil clock",
			errcode.WithInternal(errcode.InternalAttr("_", "nil clock")))
	}
	if eventID == "" {
		// eventID is the sole idempotency key; an empty key would collide all
		// empty-id appends into one fingerprint. Fail-fast rather than silently
		// corrupt the chain's at-least-once dedup guarantee.
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit: AppendBootstrapAuthFail requires non-empty eventID",
			errcode.WithInternal(errcode.InternalAttr("_", "empty eventID")))
	}
	if _, ok := validBootstrapAuthFailReasons[reason]; !ok {
		allowed := make([]string, 0, len(validBootstrapAuthFailReasons))
		for k := range validBootstrapAuthFailReasons {
			allowed = append(allowed, k)
		}
		sort.Strings(allowed)
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit: bootstrap auth-fail reason not in whitelist",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("reason=%q allowed=%v", reason, allowed))))
	}
	payload, err := json.Marshal(bootstrapAuthFailPayload{Reason: reason, ClientIPHash: clientIPHash})
	if err != nil {
		// json.Marshal of two strings can only fail under engine-level
		// corruption; wrap for diagnostic surface and propagate.
		return fmt.Errorf("audit: marshal bootstrap auth-fail payload: %w", err)
	}
	entry := &ledger.Entry{
		EventID:   eventID,
		EventType: bootstrapAuthFailEventType,
		ActorID:   bootstrapAuthFailActorID,
		Timestamp: clk.Now().UTC(),
		Payload:   payload,
	}
	if err := store.Append(ctx, entry); err != nil {
		return fmt.Errorf("audit: append bootstrap auth-fail entry: %w", err)
	}
	return nil
}
