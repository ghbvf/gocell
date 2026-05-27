// Package auditappendsession is the audit-append-session slice: it consumes
// session lifecycle events and appends them to the audit ledger. All
// behavior lives in cells/auditcore/internal/appender; this package
// contributes only its slice.yaml metadata (subscription contracts, verify
// list, consumer group) and the Spec that selects the actor-extraction
// strategy.
//
// Subscribed topics: event.session.created.v1, event.session.revoked.v1.
// Note: event.session.auth-failed.v1 is not yet connected pending contract definition.
package auditappendsession

import "github.com/ghbvf/gocell/cells/auditcore/internal/appender"

// Service is the slice service type (see appender.doc.go for the
// type-alias rationale).
type Service = appender.Service

// Spec is the per-slice configuration that names this slice for log/error
// prefixes. Actor identity now comes exclusively from entry.Principal.ActorID
// (injected by producers via InjectPrincipalFromContext).
var Spec = appender.MustNewSpec("auditappendsession")
