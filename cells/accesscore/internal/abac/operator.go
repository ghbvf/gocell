// Package abac is the ABAC (Attribute-Based Access Control) policy authoring
// and persistence model for accesscore. It contains the domain types for
// Policy, Rule, Condition, Operator, and AttributeSource — pure value objects
// with typed validation, no clock dependency, and no wire types.
//
// This package may import: pkg/authz, pkg/tenant, pkg/errcode, stdlib.
// It must NOT import: kernel/, runtime/, adapters/, cells/ (other than itself).
//
// Design references:
//   - AWS Cedar (model/policy separation, default-deny/forbid-wins)
//   - XACML 3.0 §5 — Policy / Rule / Condition type hierarchy
//   - Casbin model/policy domain (reject reflective ...interface{})
package abac

import (
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Operator is the comparison operation in a Condition. iota+1 zero-invalid
// convention: the zero value is never a valid operator.
//
// The set covers the four most common ABAC attribute predicates:
// equality, inequality, set-membership, and set-non-membership.
// This is a minimal extensible set aligned with the spec examples
// (department=eng, classification!=secret, role in {admin,editor}).
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
)

// Valid reports whether op is one of the four defined operators.
func (op Operator) Valid() bool {
	return op >= OpEquals && op <= OpNotIn
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
