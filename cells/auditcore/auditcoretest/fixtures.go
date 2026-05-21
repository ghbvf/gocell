package auditcoretest

import (
	"encoding/json"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
)

// CanonicalSessionCreatedEntry constructs a valid event.session.created.v1
// outbox.Entry whose payload satisfies the contract schema
// (contracts/event/session/created/v1/payload.schema.json).
//
// The payload contains sessionId and userId — the two required fields per the
// schema. entry.ID is used as the content-fingerprint idempotency key by
// ledger.MemStore, so callers that send multiple entries must supply distinct
// sessionID values.
//
// ref: contracts/event/session/created/v1/payload.schema.json (required: sessionId, userId)
// ref: cells/accesscore/internal/dto/session_events.go SessionCreatedEvent (producer shape)
func CanonicalSessionCreatedEntry(sessionID, userID string) outbox.Entry {
	payload, _ := json.Marshal(struct {
		SessionID string `json:"sessionId"`
		UserID    string `json:"userId"`
	}{
		SessionID: sessionID,
		UserID:    userID,
	})
	return outbox.Entry{
		ID:        sessionID,
		EventType: "event.session.created.v1",
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	}
}
