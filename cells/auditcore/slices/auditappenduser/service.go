// Package auditappenduser is the audit-append-user slice: it consumes user
// lifecycle events and appends them to the audit ledger. All behavior lives
// in cells/auditcore/internal/appender; this package contributes only its
// slice.yaml metadata (subscription contracts, verify list, consumer group)
// and the Spec that selects the actor-extraction strategy.
//
// Subscribed topics: event.user.created.v1, event.user.locked.v1,
// event.user.unlocked.v1, event.user.updated.v1, event.user.deleted.v1.
package auditappenduser

import "github.com/ghbvf/gocell/cells/auditcore/internal/appender"

// Service is the slice service type. The cell_gen.go field declared as
// *auditappenduser.Service resolves through this alias to *appender.Service;
// because Go forbids methods on aliases to non-local types, this package
// cannot fork HandleEvent or any other appender.Service behavior.
type Service = appender.Service

// Spec is the per-slice configuration that names this slice for log/error
// prefixes. Actor identity now comes exclusively from entry.Principal.ActorID
// (injected by producers via InjectPrincipalFromContext). cell.go reads Spec
// during initSlices.
var Spec = appender.MustNewSpec("auditappenduser")
