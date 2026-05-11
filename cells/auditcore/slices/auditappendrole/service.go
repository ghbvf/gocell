// Package auditappendrole is the audit-append-role slice: it consumes role
// assignment events and appends them to the audit ledger. All behavior
// lives in cells/auditcore/internal/appender; this package contributes only
// its slice.yaml metadata (subscription contracts, verify list, consumer
// group) and the Spec that selects the actor-extraction strategy.
//
// Subscribed topics: event.role.assigned.v1, event.role.revoked.v1.
package auditappendrole

import "github.com/ghbvf/gocell/cells/auditcore/internal/appender"

// Service is the slice service type (see appender.doc.go for the
// type-alias rationale).
type Service = appender.Service

// Spec selects the actor-extraction strategy for role events: actorId is
// the admin/operator who performed the assignment; userId in role events
// identifies the target, not the actor, so userId fallback is forbidden
// (B2-C-05 fail-closed).
var Spec = appender.MustNewSpec("auditappendrole", appender.ActorRequireExplicit)
