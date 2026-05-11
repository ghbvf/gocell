// Package auditappendconfig is the audit-append-config slice: it consumes
// config-change events and appends them to the audit ledger. All behavior
// lives in cells/auditcore/internal/appender; this package contributes only
// its slice.yaml metadata (subscription contracts, verify list, consumer
// group) and the Spec that selects the actor-extraction strategy.
//
// Subscribed topics: event.config.entry-upserted.v1,
// event.config.entry-deleted.v1, event.config.version-published.v1,
// event.config.rollback.v1.
package auditappendconfig

import "github.com/ghbvf/gocell/cells/auditcore/internal/appender"

// Service is the slice service type (see appender.doc.go for the
// type-alias rationale).
type Service = appender.Service

// Spec selects the actor-extraction strategy for config-change events:
// prefer payload.actorId, fall back to payload.userId.
var Spec = appender.MustNewSpec("auditappendconfig", appender.ActorAcceptUserFallback)
