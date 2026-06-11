// Package projection is the PEP-side (policy enforcement point) column-masking
// framework for the GoCell data-permission model. It provides ResourceProjection,
// the sealed carrier for a column-masked resource view, and the NewProjection /
// NewProjectionList funnel that is the only way to build one.
//
// # PDP vs PEP split
//
// The authorization vocabulary in pkg/authz is the PDP (decision) output:
// authz.FieldMask is an open obligation that merely names the columns to mask —
// the names are inert strings carrying no runtime security. This package is the
// PEP that discharges that obligation. It imports authz.FieldMask rather than
// redefining it (the obligation lives with the Decision it travels in;
// see docs/plans/specs/1220-tenancy-abac-dataperm/research.md §1.1).
//
//	authz.FieldMask (PDP obligation)  →  NewProjection  →  ResourceProjection (PEP-enforced view)
//
// # Masking semantics
//
// A masked column's value is replaced by redaction.Mask ("<REDACTED>"); the
// column stays present. The visible field set is therefore invariant under
// masking, and an empty FieldMask is the identity projection. This avoids the
// MVP→masking response-shape churn the obligation model is designed to prevent:
// a Decision carrying FieldMask{} projects identically to no obligation at all.
// Masking is top-level only; an un-dischargeable (dotted/nested) obligation
// fails closed rather than leaking an un-masked column (see requireEnforceable).
//
// The masked sentinel is HTML-escaped by encoding/json on the wire (the bytes
// are "<REDACTED>", not the literal "<REDACTED>"); consumers matching
// the sentinel in a JSON body must decode first or compare the escaped form.
//
// # Immutability contract
//
// Construction never mutates the caller's input. A masked column's value is
// REPLACED (by the sentinel), so masked data is never aliased — there is no
// leak path through a shared reference. Un-masked values, however, share their
// reference with the caller's map (they are meant to be visible), so the caller
// MUST NOT mutate the input map after construction if a stable snapshot matters.
//
// # Seal (RESOURCE-PROJECTION-SEALED-01)
//
// ResourceProjection's single field is unexported, so an external populated
// literal does not compile — the same sealed-construction pattern as
// errcode.PublicDetail, outbox.Entry, and authz.Decision. A handler whose
// response type is a ResourceProjection cannot hand back a raw, un-masked map:
// the only ResourceProjection it can obtain comes from the constructors, which
// always apply the mask.
//
// The AI-robust rating is an honestly-split funnel:
//
//   - Upstream (type-system Hard): unexported field ⇒ no external populated
//     literal; reflect field-freeze guards a regression that re-exports it.
//   - Downstream (go/types Hard): the sole-reconstruction-surface check pins the
//     only exported producers to {NewProjection, NewProjectionList}, so no second
//     forge path can be added inside this package without tripping the archtest.
//
// SCOPE — what this package does NOT yet enforce. PR-11 ships the unforgeable
// carrier only. Forcing production read handlers to actually return a
// ResourceProjection built from the request's Decision.Obligations().FieldMask —
// the downstream callsite lock — lands in PR-12 (#1350), when real handlers
// adopt it. Until then this package is a safe building block, not active column
// protection: do not read RESOURCE-PROJECTION-SEALED-01 as "column leakage is
// already closed".
package projection
