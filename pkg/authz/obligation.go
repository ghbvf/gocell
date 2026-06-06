package authz

import (
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/tenant"
)

// FieldMask specifies the set of column names that the PEP must mask in the
// response. It is an open (non-sealed) obligation data struct — the names are
// inert strings and carry no runtime security themselves; enforcement is
// entirely on the PEP side.
//
// An empty FieldMask (IsZero() == true) is VALID and means "identity
// projection / no masking". This avoids MVP→PR-11 response-type churn: a
// Decision carrying FieldMask{} is identical to a Decision with no column
// masking obligation. PEPs SHOULD treat a zero FieldMask as a no-op.
type FieldMask struct {
	// Fields is the ordered list of column names to mask. Order is advisory
	// (PEPs apply masking by set membership). An empty slice is valid and
	// means "mask nothing" (identity projection).
	Fields []string
}

// IsZero reports whether the FieldMask has no masking columns (len(Fields)==0).
// A zero FieldMask is a valid obligation meaning "no column masking required".
func (fm FieldMask) IsZero() bool {
	return len(fm.Fields) == 0
}

// Validate returns an error if any entry in Fields is the empty string, or if
// Fields contains duplicate column names.
func (fm FieldMask) Validate() error {
	seen := make(map[string]struct{}, len(fm.Fields))
	for _, f := range fm.Fields {
		if f == "" {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"authz: FieldMask contains empty column name")
		}
		if _, dup := seen[f]; dup {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"authz: FieldMask contains duplicate column name",
				errcode.WithDetails(errcode.PublicString("column", f)))
		}
		seen[f] = struct{}{}
	}
	return nil
}

// Obligations is the bounded set of mandatory XACML obligations that the PEP
// must discharge when the Decision is Allow. Obligations are only meaningful on
// an Allow decision; a PEP MUST NOT enforce obligations from a Deny decision.
//
// Two obligation axes are modeled:
//
//   - RowScope: the row-visibility predicate the PEP must apply to list/get
//     queries. A zero RowScope is valid here — it means "this policy does not
//     itself constrain row scope" (the policy obligation is absent). The
//     *effective* row scope for a request is resolved by the PR-7 evaluator,
//     which combines all policy obligations with the principal→RowScope
//     derivation (PR-5). Only a non-zero RowScope carries an explicit
//     obligation; it must be a valid tenant.RowScope value.
//
//   - FieldMask: the set of columns to mask in the response. An empty FieldMask
//     is valid and means "identity projection / no masking".
//
// ref: XACML-3.0 §3.5 — obligations are MANDATORY; advice is OPTIONAL.
// GoCell only models mandatory obligations in this type.
type Obligations struct {
	// RowScope is the row-visibility obligation. A zero value is valid and
	// means "this policy does not impose a row-scope constraint"; the PR-7
	// evaluator resolves the effective scope from all applicable obligations
	// combined with the principal→RowScope derivation (PR-5). A non-zero
	// value must pass tenant.RowScope.Validate().
	RowScope tenant.RowScope
	// FieldMask specifies which response columns to mask. An empty FieldMask
	// is valid (no masking required).
	FieldMask FieldMask
}

// Validate returns an error if any obligation field is in an invalid state.
// A zero RowScope is valid (zero = "this policy does not impose a row-scope
// constraint"; the PR-7 evaluator resolves the effective scope).
// A non-zero RowScope must pass tenant.RowScope.Validate().
func (o Obligations) Validate() error {
	if o.RowScope != 0 {
		if err := o.RowScope.Validate(); err != nil {
			return err
		}
	}
	return o.FieldMask.Validate()
}
