package auditcoretest

import (
	"context"
	"encoding/json"

	"github.com/ghbvf/gocell/kernel/clock"
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

// NewSessionCreatedEntry constructs an outbox.Entry with an
// event.session.created.v1 payload that satisfies the auditappendsession
// actor-acceptance rules (userId present, payload is a valid JSON object).
//
// The entry ID format is "evt-{sessionID}" — each test scenario gets a unique
// ID by varying sessionID, preventing idempotency rejection when driving
// multiple entries through one chain. This diverges from PR #588's fixed
// literal "evt-j-auditlogintrail" so that multi-entry chain tests remain
// valid.
//
// createdAt/occurredAt are stamped by outbox.NewEntry from clock.Real() at call
// time; the ledger MemStore assigns its own store-level Timestamp from the
// injected clock, so the entry times are informational only for test assertions.
// The deterministic ID "evt-{sessionID}" is pinned via outbox.WithID so each
// scenario gets a unique idempotency key.
//
// ref: contracts/event/session/created/v1/payload.schema.json (sessionId + userId required).
// ref: cells/accesscore/internal/dto/session_events.go SessionCreatedEvent (same field names).
func NewSessionCreatedEntry(sessionID, userID string) outbox.Entry {
	payload, err := json.Marshal(sessionCreatedPayload{
		SessionID: sessionID,
		UserID:    userID,
	})
	if err != nil {
		// json.Marshal on a plain struct with string fields never fails.
		// Unreachable in practice; kept to satisfy compiler.
		panic("auditcoretest: NewSessionCreatedEntry: json.Marshal failed: " + err.Error())
	}
	e, err := outbox.NewEntry(clock.Real(), context.Background(),
		"event.session.created.v1", payload, outbox.WithID("evt-"+sessionID))
	if err != nil {
		panic("auditcoretest: NewSessionCreatedEntry: " + err.Error())
	}
	return e
}
