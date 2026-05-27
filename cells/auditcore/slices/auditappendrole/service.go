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

// Spec is the per-slice configuration that names this slice for log/error
// prefixes. Actor identity now comes exclusively from entry.Principal.ActorID
// (injected by producers via InjectPrincipalFromContext).
var Spec = appender.MustNewSpec("auditappendrole")
