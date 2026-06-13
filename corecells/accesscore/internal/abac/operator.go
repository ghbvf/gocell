package abac

import (
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Operator is the comparison operation in a Condition. iota+1 zero-invalid
// convention: the zero value is never a valid operator.
//
// The static-value set covers the four most common ABAC attribute predicates:
// equality, inequality, set-membership, and set-non-membership — each comparing
// a resolved attribute against the Condition's static Values
// (department=eng, classification!=secret, role in {admin,editor}).
//
// OpEqualsAttr is the one cross-attribute operator: it compares the resolved
// (Source, Key) attribute against ANOTHER resolved attribute (RHSSource, RHSKey)
// rather than static Values — e.g. subject.sub == resource.id (identity
// ownership) or subject.sub == resource.owner (delegated ownership, MDM #1977).
type Operator uint8

const (
	// OpEquals asserts that the named attribute equals one of the Values.
	// Wire spelling: "eq". Example: department = "eng".
	OpEquals Operator = iota + 1
	// OpNotEquals asserts that the named attribute does not equal any of the Values.
	// Wire spelling: "neq". Example: classification != "secret".
	OpNotEquals
	// OpIn asserts that the named attribute is a member of the Values set.
	// Wire spelling: "in". Example: role in {"admin", "editor"}.
	OpIn
	// OpNotIn asserts that the named attribute is not in the Values set.
	// Wire spelling: "not_in". Example: region not_in {"us-gov-east-1"}.
	OpNotIn
	// OpEqualsAttr asserts that the named attribute (Source, Key) equals the
	// attribute referenced by (RHSSource, RHSKey) — a cross-attribute comparison,
	// NOT a comparison against static Values. Wire spelling: "eq_attr". Example:
	// subject.sub == resource.id. A cross-attribute condition carries an RHS
	// attribute reference and MUST NOT carry static Values (see Condition.Validate).
	OpEqualsAttr
)

// Valid reports whether op is one of the defined operators.
func (op Operator) Valid() bool {
	return op >= OpEquals && op <= OpEqualsAttr
}

// String returns the wire/log spelling of the operator, or "invalid" for the
// zero value and any out-of-range value.
func (op Operator) String() string {
	switch op {
	case OpEquals:
		return "eq"
	case OpNotEquals:
		return "neq"
	case OpIn:
		return "in"
	case OpNotIn:
		return "not_in"
	case OpEqualsAttr:
		return "eq_attr"
	default:
		return "invalid"
	}
}

// Validate returns an error for the zero value or any out-of-range operator.
func (op Operator) Validate() error {
	if !op.Valid() {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "abac: invalid Operator value")
	}
	return nil
}

// ParseOperator converts a wire/log spelling back to its Operator value
// (inverse of String). Recognized codes: "eq", "neq", "in", "not_in",
// "eq_attr". Any other input — including the empty string — returns an error
// (fail-closed).
func ParseOperator(s string) (Operator, error) {
	switch s {
	case "eq":
		return OpEquals, nil
	case "neq":
		return OpNotEquals, nil
	case "in":
		return OpIn, nil
	case "not_in":
		return OpNotIn, nil
	case "eq_attr":
		return OpEqualsAttr, nil
	default:
		return 0, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "abac: unknown Operator wire code")
	}
}
