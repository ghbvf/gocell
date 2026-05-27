// Package ctxkeys provides typed context keys for generic observability,
// networking, and OAuth/OIDC principal identity identifiers propagated through
// context.Context.
//
// Keys in this package fall into two groups:
//
//   - Observability + networking: Correlation/Trace/TraceParent/Span/Request/RealIP —
//     generic cross-service conventions used by middleware and OTel integrations.
//
//   - OAuth/OIDC principal identity: Actor/Subject/Tenant/Session —
//     OAuth/OIDC-aligned identity fields injected by auth middleware and carried
//     across the async boundary via the outbox wire envelope Principal field
//     (kernel/outbox.Entry.InjectPrincipalFromContext / RestoreToContext).
//     The Actor/Subject distinction follows RFC 8693 §4.1: SubjectID is the
//     resource owner; ActorID is the requesting party (equals SubjectID when
//     there is no impersonation).
//
// Cell-model architectural identifiers (cell, slice, journey, contract) live in
// github.com/ghbvf/gocell/kernel/ctxkeys because they encode GoCell
// architectural concepts rather than generic cross-service conventions.
//
// ref: PR #1218 / W0 wire envelope — Principal fields added to outbox envelope.
package ctxkeys
