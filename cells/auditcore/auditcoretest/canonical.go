package auditcoretest

import (
	"encoding/json"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
)

// sessionCreatedPayload mirrors the payload shape declared in
// contracts/event/session/created/v1/payload.schema.json (sessionId + userId
// required). It is a package-private type: callers interact with the
// constructed outbox.Entry, not with the raw payload struct.
//
// Per cell-patterns.md "跨 cell decode 重复属于预期成本", we duplicate the
// field names here rather than importing cells/accesscore/internal/dto.
// The canonical schema source is contracts/event/session/created/v1/payload.schema.json.
type sessionCreatedPayload struct {
	SessionID string `json:"sessionId"`
	UserID    string `json:"userId"`
}

// CanonicalSessionCreatedEntry constructs an outbox.Entry with an
// event.session.created.v1 payload that satisfies the auditappendsession
// actor-acceptance rules (userId present, payload is a valid JSON object).
//
// The entry ID is set to "evt-" + sessionID so that callers can drive multiple
// entries through one chain by varying sessionID — each entry carries a
// distinct ID, preventing idempotency rejection on the second call.
//
// CreatedAt is time.Now().UTC() at call time; the ledger MemStore assigns
// its own store-level Timestamp from the injected clock, so the entry
// CreatedAt is informational only for test assertions.
//
// ref: contracts/event/session/created/v1/payload.schema.json (sessionId + userId required).
// ref: cells/accesscore/internal/dto/session_events.go SessionCreatedEvent (same field names).
func CanonicalSessionCreatedEntry(sessionID, userID string) outbox.Entry {
	payload, err := json.Marshal(sessionCreatedPayload{
		SessionID: sessionID,
		UserID:    userID,
	})
	if err != nil {
		// json.Marshal on a plain struct with string fields never fails.
		// Unreachable in practice; kept to satisfy compiler.
		panic("auditcoretest: CanonicalSessionCreatedEntry: json.Marshal failed: " + err.Error())
	}
	return outbox.Entry{
		ID:        "evt-" + sessionID,
		EventType: "event.session.created.v1",
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	}
}
