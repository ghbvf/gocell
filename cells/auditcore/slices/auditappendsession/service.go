// Package auditappendsession is the audit-append-session slice: it consumes
// session lifecycle events and appends them to the audit ledger. All
// behavior lives in cells/auditcore/internal/appender; this package
// contributes only its slice.yaml metadata (subscription contracts, verify
// list, consumer group) and the Spec that selects the actor-extraction
// strategy.
//
// Subscribed topics: event.session.created.v1, event.session.revoked.v1.
// Note: event.session.auth-failed.v1 (PR392-FU) is not yet connected
// pending contract definition; backlog item PR392-FU tracks the wiring.
package auditappendsession

import "github.com/ghbvf/gocell/cells/auditcore/internal/appender"

// Service is the slice service type (see appender.doc.go for the
// type-alias rationale).
type Service = appender.Service

// Spec selects the actor-extraction strategy for session lifecycle events:
// prefer payload.actorId, fall back to payload.userId (the user whose
// session was created/revoked).
var Spec = appender.MustNewSpec("auditappendsession", appender.ActorAcceptUserFallback)
