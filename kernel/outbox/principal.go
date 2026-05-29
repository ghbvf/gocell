package outbox

import (
	"context"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
)

// PrincipalMetadata carries cross-async principal identity that the gocell
// observability bridge owns. Producers MUST NOT populate these fields
// directly — NewEntry fills them from the construction context (the single
// injection trust boundary). Consumer middleware (RestoreToContext) reads
// them back into handler context. It mirrors ObservabilityMetadata field-for
// -field (sealed-construction sibling, issue #1229).
//
// OAuth/OIDC semantics:
//
//   - ActorID = impersonator — the principal actually triggering the
//     action (token "act.sub" claim). In non-impersonation flows equals
//     SubjectID.
//   - SubjectID = subject-of-record — the user the action is performed
//     on behalf of (token "sub" claim).
//   - TenantID = tenant boundary identifier (multi-tenant deployments).
//   - SessionID = session identifier (server-side session binding).
//
// The typed field prevents producers from forging principal IDs via
// entry.Metadata["actor_id"] = "evil" — the two namespaces are physically
// separate, and Entry.Validate rejects ReservedMetadataKeys to keep
// producers honest at write time. Mirror of ObservabilityMetadata's
// rationale for trace identity.
//
// ref: OpenID Connect Core 1.0 §5.1 ("sub"), RFC 8693 §4.1 ("act") —
// typed carrier of principal identity, kept distinct from observability
// (trace) and business (metadata) namespaces.
//
// All four fields use idutil.SafeID — UnmarshalJSON fail-closes on
// unsafe characters at wire boundary (CWE-117 defense).
//
// The field set, JSON tags, and method set are frozen by archtest
// PRINCIPAL-SEALED-FIELD-FROZEN-01.
type PrincipalMetadata struct {
	ActorID   idutil.SafeID `json:"actorId,omitempty"`
	SubjectID idutil.SafeID `json:"subjectId,omitempty"`
	TenantID  idutil.SafeID `json:"tenantId,omitempty"`
	SessionID idutil.SafeID `json:"sessionId,omitempty"`
}

// IsZero reports whether all fields are empty.
func (p PrincipalMetadata) IsZero() bool {
	return p.ActorID == "" && p.SubjectID == "" &&
		p.TenantID == "" && p.SessionID == ""
}

// Validate enforces per-field size + charset bounds. Each non-empty
// field must satisfy idutil.IsSafeID and len ≤ idutil.MaxMetadataIDLen
// via SafeID.Validate. Zero-value PrincipalMetadata returns nil (matches
// ObservabilityMetadata optional model).
//
// Producers MUST call Validate (via Entry.Validate, which is called by
// every Writer.Write impl) so size violations surface at write time
// rather than as silent broker rejections or downstream OOMs.
func (p PrincipalMetadata) Validate() error {
	if err := validatePrincipalSafeID("actorId", p.ActorID); err != nil {
		return err
	}
	if err := validatePrincipalSafeID("subjectId", p.SubjectID); err != nil {
		return err
	}
	if err := validatePrincipalSafeID("tenantId", p.TenantID); err != nil {
		return err
	}
	if err := validatePrincipalSafeID("sessionId", p.SessionID); err != nil {
		return err
	}
	return nil
}

// validatePrincipalSafeID wraps SafeID.Validate failures with the
// field-name tag that principal errcode consumers (logs, metrics) rely on.
// The field name (a const literal) is the only runtime datum surfaced; the
// principal value is never interpolated into the message (MESSAGE-CONST-
// LITERAL-01 / INV-ERRCODE-NO-PII). Returns nil for the zero value.
func validatePrincipalSafeID(name string, id idutil.SafeID) error {
	if err := id.Validate(); err != nil {
		return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed,
			"outbox: principal field invalid", err,
			errcode.WithDetails(errcode.PublicString("field", name)))
	}
	return nil
}

// ContextPrincipal reads reserved principal values from ctx and returns
// a populated PrincipalMetadata. Missing keys stay empty. Mirror of
// ContextObservability — ctx values were already validated upstream
// (runtime/auth middleware writes ctxkeys at the request trust boundary);
// cast to SafeID without re-checking; producer-side Validate (invoked by
// Entry.Validate at NewEntry) catches any drift before reaching wire.
func ContextPrincipal(ctx context.Context) PrincipalMetadata {
	var p PrincipalMetadata
	if id, ok := ctxkeys.ActorIDFrom(ctx); ok && id != "" {
		p.ActorID = idutil.SafeID(id)
	}
	if id, ok := ctxkeys.SubjectIDFrom(ctx); ok && id != "" {
		p.SubjectID = idutil.SafeID(id)
	}
	if id, ok := ctxkeys.TenantIDFrom(ctx); ok && id != "" {
		p.TenantID = idutil.SafeID(id)
	}
	if id, ok := ctxkeys.SessionIDFrom(ctx); ok && id != "" {
		p.SessionID = idutil.SafeID(id)
	}
	return p
}

// RestoreToContext returns a new context populated with the non-empty
// fields of p. Existing non-empty ctx values WIN (idempotent restore —
// the consumer ctx may already carry principal identity from an outbound
// header on a synchronous spawn path; we do not stomp it). Values that
// fail safety validation (overlong, unsafe chars) are silently dropped.
//
// Producer/consumer asymmetry — by design (mirror of
// ObservabilityMetadata.RestoreToContext):
//
//   - NewEntry OVERWRITES e.principal with the construction context's
//     identity (the producer is the source of truth for what the entry
//     carries).
//   - RestoreToContext does NOT overwrite existing ctx values (the
//     consumer ctx may legitimately carry its own principal propagated
//     by an outer middleware; the entry's identity is a fallback).
func (p PrincipalMetadata) RestoreToContext(ctx context.Context) context.Context {
	ctx = withContextMetadata(ctx, string(p.ActorID), ctxkeys.ActorIDFrom, ctxkeys.WithActorID)
	ctx = withContextMetadata(ctx, string(p.SubjectID), ctxkeys.SubjectIDFrom, ctxkeys.WithSubjectID)
	ctx = withContextMetadata(ctx, string(p.TenantID), ctxkeys.TenantIDFrom, ctxkeys.WithTenantID)
	ctx = withContextMetadata(ctx, string(p.SessionID), ctxkeys.SessionIDFrom, ctxkeys.WithSessionID)
	return ctx
}

// injectPrincipalFromContext populates e.principal from ctx. NewEntry calls
// this at construction — the single injection trust boundary. There is no
// exported producer-facing inject API (issue #1229: principal injection is
// the framework's responsibility, not the producer's). Idempotent; overwrites
// any prior value.
//
// Symmetric with the consumer-side restoration baked into
// SubscriberWithMiddleware.SubscribeEntry: NewEntry injects from the
// construction ctx, consumers automatically restore from entry.principal at
// dispatch time. The two endpoints are coupled by construction — neither can
// be silently disabled. Mirror of injectObservabilityFromContext.
func (e *Entry) injectPrincipalFromContext(ctx context.Context) {
	e.principal = ContextPrincipal(ctx)
}
