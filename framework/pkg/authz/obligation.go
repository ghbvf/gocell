package authz

import (
	"regexp"
	"unicode"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

// attributeKeyPattern is the canonical pattern for an attribute or column
// identifier: starts with a letter, followed by letters, digits, underscores,
// or dots. This rejects leading/trailing/internal whitespace, control chars,
// and identifiers that start with a digit (e.g. "1abc") which would be
// ambiguous in filter expressions.
var attributeKeyPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.]*$`)

// ValidAttributeKey validates a canonical attribute/column identifier.
// It must be non-empty, contain no leading/trailing or internal whitespace,
// no control chars, and match ^[A-Za-z][A-Za-z0-9_.]*$.
// Returns KindInvalid/ErrValidationFailed otherwise.
//
// This function is shared by FieldMask.Validate and Condition.Validate to
// prevent silent masking/evaluation no-ops caused by keys like " ssn" or
// "ssn\n" that would never match at enforcement time.
func ValidAttributeKey(s string) error {
	if s == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"authz: attribute key must not be empty")
	}
	// Reject any control characters or whitespace, even internal ones. The
	// regex below would accept embedded unicode letters but not spaces; this
	// explicit check also catches control chars not matched by \s.
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"authz: attribute key must not contain whitespace or control characters")
		}
	}
	if !attributeKeyPattern.MatchString(s) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"authz: attribute key must match ^[A-Za-z][A-Za-z0-9_.]*$")
	}
	return nil
}

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

// Masks reports whether column is in the mask's field set. A zero FieldMask
// masks nothing (always false). This is the obligation-query a PEP uses to
// decide whether a given column is governed by the mask — e.g. to reject a query
// predicate on a column the caller cannot see (a masked-column filter would leak
// a match/no-match oracle on a value the response redacts).
func (fm FieldMask) Masks(column string) bool {
	for _, f := range fm.Fields {
		if f == column {
			return true
		}
	}
	return false
}

// IdentityFieldMask returns the empty (identity-projection) FieldMask: it masks
// nothing, so a PEP serializes the full column set. It is the NAMED intent marker
// for a read whose column visibility is not (yet) constrained — every such
// callsite is greppable as `authz.IdentityFieldMask()` rather than an anonymous
// `authz.FieldMask{}` that reads ambiguously as "identity" vs "TODO: real mask".
// When the ABAC policy engine is wired (PR-10 #1348), the swap is a one-line
// replacement of this call with Decision.Obligations().FieldMask at each site.
func IdentityFieldMask() FieldMask {
	return FieldMask{}
}

// Validate returns an error if any entry in Fields is not a canonical
// attribute key (see ValidAttributeKey), or if Fields contains duplicate
// column names.
func (fm FieldMask) Validate() error {
	seen := make(map[string]struct{}, len(fm.Fields))
	for _, f := range fm.Fields {
		if err := ValidAttributeKey(f); err != nil {
			return err
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

// clone returns a deep copy of fm with a fresh Fields backing array.
// A nil Fields slice stays nil (zero FieldMask clones to zero FieldMask).
func (fm FieldMask) clone() FieldMask {
	if fm.Fields == nil {
		return FieldMask{}
	}
	cp := make([]string, len(fm.Fields))
	copy(cp, fm.Fields)
	return FieldMask{Fields: cp}
}

// Obligations is the bounded set of mandatory XACML obligations that the PEP
// must discharge when the Decision is Allow. Obligations are only meaningful on
// an Allow decision; a PEP MUST NOT enforce obligations from a Deny decision.
//
// Two obligation axes are modeled:
//
//   - RowScope: the row-visibility predicate the PEP must apply to list/get
//     queries. A zero RowScope is valid here — it means "this policy does not
//     itself constrain row scope" (the policy obligation is absent). The current
//     PR-7 evaluator combines RowScope obligations only among matching permit
//     rules, choosing the narrowest policy-imposed scope. It does NOT yet merge
//     those policy obligations with the principal→RowScope derivation (PR-5);
//     data PEPs that need an effective request scope must perform that merge at
//     the enforcement boundary. Only a non-zero RowScope carries an explicit
//     obligation; it must be a valid tenant.RowScope value.
//
//   - FieldMask: the set of columns to mask in the response. An empty FieldMask
//     is valid and means "identity projection / no masking".
//
// ref: XACML-3.0 §3.5 — obligations are MANDATORY; advice is OPTIONAL.
// GoCell only models mandatory obligations in this type.
type Obligations struct {
	// RowScope is the row-visibility obligation. A zero value is valid and
	// means "this policy does not impose a row-scope constraint"; the current
	// evaluator merges only matching policy-permit obligations and leaves any
	// principal-derived RowScope merge to the data PEP. A non-zero value must
	// pass tenant.RowScope.Validate().
	RowScope tenant.RowScope
	// FieldMask specifies which response columns to mask. An empty FieldMask
	// is valid (no masking required).
	FieldMask FieldMask
}

// IsZero reports whether the Obligations impose no PEP duty at all: no row-scope
// constraint (zero RowScope) and an empty FieldMask. A coarse route-gate PEP that
// cannot discharge obligations uses this to fail closed on any non-zero
// obligation rather than silently drop it (see runtime/auth.RequirePermission, F5).
func (o Obligations) IsZero() bool {
	return o.RowScope == 0 && o.FieldMask.IsZero()
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

// clone returns a deep copy of o with a fresh FieldMask.Fields backing array.
// RowScope is a scalar value type (uint8) and copies by value automatically.
func (o Obligations) clone() Obligations {
	return Obligations{
		RowScope:  o.RowScope,
		FieldMask: o.FieldMask.clone(),
	}
}
