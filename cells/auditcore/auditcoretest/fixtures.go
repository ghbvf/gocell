package auditcoretest

import (
	"encoding/json"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
)

// WARNING: entry.ID is set to sessionID so the audit ledger's content-fingerprint
// idempotency uses sessionID as the dedup key. Callers MUST pass distinct
// sessionID values per call within the same chain, otherwise the second insert
// will be rejected as ErrAuditLedgerAlreadyExists. Tests that need an explicit
// event_id independent of sessionID should construct outbox.Entry directly.

// CanonicalSessionCreatedEntry constructs a valid event.session.created.v1
// outbox.Entry whose payload satisfies the contract schema
// (contracts/event/session/created/v1/payload.schema.json).
//
// The payload contains sessionId and userId — the two required fields per the
// schema. entry.ID is used as the content-fingerprint idempotency key by
// ledger.MemStore, so callers that send multiple entries must supply distinct
// sessionID values.
//
// CreatedAt is zero so the ledger clock controls timestamps; this aligns with
// BuildAuditcoreChain's WithClock.
//
// ref: contracts/event/session/created/v1/payload.schema.json (required: sessionId, userId)
// ref: cells/accesscore/internal/dto/session_events.go SessionCreatedEvent (producer shape)
func CanonicalSessionCreatedEntry(sessionID, userID string) outbox.Entry {
	payload, err := json.Marshal(struct {
		SessionID string `json:"sessionId"`
		UserID    string `json:"userId"`
	}{
		SessionID: sessionID,
		UserID:    userID,
	})
	if err != nil {
		panic(panicregister.Approved("auditcoretest-marshal-canonical-entry",
			errcode.Assertion("marshal canonical session.created entry: %v", err)))
	}
	return outbox.Entry{
		ID:        sessionID,
		EventType: "event.session.created.v1",
		Payload:   payload,
		CreatedAt: time.Time{},
	}
}
