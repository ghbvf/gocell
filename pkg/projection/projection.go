package projection

import (
	"encoding/json"
	"strings"

	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/redaction"
)

// ResourceProjection is the sealed, column-masked view of a resource that a PEP
// (policy enforcement point) may serialize to the wire. Its only field is
// unexported, so a populated literal `projection.ResourceProjection{data: ...}`
// outside this package does not compile — the sole way to obtain a populated
// value is NewProjection / NewProjectionList, which apply the FieldMask
// obligation. A handler therefore cannot fabricate an un-masked ("full") view;
// see package doc and RESOURCE-PROJECTION-SEALED-01 for the funnel proof.
//
// The zero value marshals to JSON null (it carries no forged data) — the only
// literal expressible outside the package is harmless.
type ResourceProjection struct {
	data map[string]any
}

// NewProjection builds the sealed, masked view of a single resource. It first
// validates the obligation (authz.FieldMask.Validate — canonical keys, no
// duplicates), then refuses obligations this PEP cannot discharge
// (requireEnforceable — fail-closed), then applies the mask onto a private copy
// of data.
//
// The mask is expected to come from Decision.Obligations().FieldMask. An empty
// FieldMask is valid and yields the identity projection (every value visible,
// response field set stable) — passing an empty mask where a populated one was
// intended therefore returns the FULL view silently; supplying the right mask is
// the caller's responsibility (locked at the handler callsite in PR-12).
//
// Error kinds let the handler map status codes: a validation failure carries
// KindInvalid (→ 400, client-correctable bad mask); an un-dischargeable
// obligation carries KindInternal (→ 500, a PEP misconfiguration, not user
// error). A nil data map projects to an empty JSON object ("{}"), distinct from
// the zero-value ResourceProjection which marshals to null.
func NewProjection(mask authz.FieldMask, data map[string]any) (ResourceProjection, error) {
	if err := mask.Validate(); err != nil {
		return ResourceProjection{}, err
	}
	if err := requireEnforceable(mask); err != nil {
		return ResourceProjection{}, err
	}
	return ResourceProjection{data: applyMask(mask, data)}, nil
}

// NewProjectionList builds the sealed, masked view of every row under one mask.
// The mask is validated once (an invalid or un-enforceable obligation fails the
// whole call before any row is projected — fail-closed); each row is then masked
// onto its own private copy. A nil/empty rows slice yields an empty result.
func NewProjectionList(mask authz.FieldMask, rows []map[string]any) ([]ResourceProjection, error) {
	if err := mask.Validate(); err != nil {
		return nil, err
	}
	if err := requireEnforceable(mask); err != nil {
		return nil, err
	}
	out := make([]ResourceProjection, len(rows))
	for i, row := range rows {
		out[i] = ResourceProjection{data: applyMask(mask, row)}
	}
	return out, nil
}

// requireEnforceable rejects obligations this PEP cannot discharge. PR-11 only
// implements top-level (flat) column masking; a dotted field name denotes a
// nested path the masker does not traverse. Silently leaving such a column
// un-masked would be a fail-OPEN leak, so — per XACML's "a PEP that cannot
// discharge a mandatory obligation MUST NOT permit the access" — we fail closed
// instead of serving an un-masked view. The dedicated
// ErrAuthObligationUnenforceable code (KindInternal → 500) lets operators alert
// on un-dischargeable obligations distinctly from arbitrary 500s; the offending
// column is recorded as a server-only internal detail (never on the wire).
// Nested-path masking is deferred until a real nested obligation exists (PR-9/PR-12).
func requireEnforceable(mask authz.FieldMask) error {
	for _, f := range mask.Fields {
		if strings.ContainsRune(f, '.') {
			return errcode.New(errcode.KindInternal, errcode.ErrAuthObligationUnenforceable,
				"projection: PEP cannot discharge nested field-mask obligation",
				errcode.WithInternal(errcode.InternalAttr("column", f)))
		}
	}
	return nil
}

// applyMask returns a shallow copy of data in which every masked column present
// in data has its value replaced by redaction.Mask. The copy is defensive: the
// caller's map (and any nested object it holds) is never mutated. A shallow copy
// suffices because a masked key's entire value is replaced — no sub-field of a
// masked object can leak — while an un-masked key is meant to stay visible, so
// sharing its reference is intentional. A masked column absent from data is a
// no-op (the key is not materialized).
func applyMask(mask authz.FieldMask, data map[string]any) map[string]any {
	out := make(map[string]any, len(data))
	for k, v := range data {
		out[k] = v
	}
	for _, f := range mask.Fields {
		if _, ok := out[f]; ok {
			out[f] = redaction.Mask
		}
	}
	return out
}

// MarshalJSON renders the masked view. The zero value (data == nil) marshals to
// JSON null so an externally-expressible empty literal carries no data. Note the
// redaction.Mask sentinel is HTML-escaped by encoding/json like any string value.
func (p ResourceProjection) MarshalJSON() ([]byte, error) {
	if p.data == nil {
		return []byte("null"), nil
	}
	return json.Marshal(p.data)
}
