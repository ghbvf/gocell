package abac

import (
	"github.com/ghbvf/gocell/pkg/errcode"
)

// AttributeSource identifies the namespace from which a Condition's attribute
// key is resolved at evaluation time. iota+1 zero-invalid convention.
//
// Three namespaces cover the standard ABAC attribute universe:
//   - Subject: attributes of the principal (e.g. department, role, job level)
//   - Resource: attributes of the protected object (e.g. classification, owner)
//   - Environment: contextual attributes (e.g. time_of_day, ip_region)
//
// ref: XACML 3.0 §5.7 — Subject, Resource, Action, Environment attribute categories.
// Action attributes are intentionally omitted (GoCell encodes actions as the
// HTTP method + contract ID, not as a separate attribute namespace).
type AttributeSource uint8

const (
	// SourceSubject resolves the attribute from the authenticated principal's
	// claims or profile. Example key: "department", "job_level", "clearance".
	SourceSubject AttributeSource = iota + 1
	// SourceResource resolves the attribute from the protected resource's
	// metadata. Example key: "classification", "owner_id", "sensitivity_level".
	SourceResource
	// SourceEnvironment resolves the attribute from the request context or
	// ambient environment. Example key: "time_of_day", "ip_region", "device_trust".
	SourceEnvironment
)

// Valid reports whether src is one of the three defined attribute sources.
func (src AttributeSource) Valid() bool {
	return src >= SourceSubject && src <= SourceEnvironment
}

// String returns the wire/log spelling of the attribute source, or "invalid"
// for the zero value and any out-of-range value.
func (src AttributeSource) String() string {
	switch src {
	case SourceSubject:
		return "subject"
	case SourceResource:
		return "resource"
	case SourceEnvironment:
		return "environment"
	default:
		return "invalid"
	}
}

// Validate returns an error for the zero value or any out-of-range source.
func (src AttributeSource) Validate() error {
	if !src.Valid() {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "abac: invalid AttributeSource value")
	}
	return nil
}

// Condition is a single predicate in a Rule. It evaluates to true when the
// attribute identified by (Source, Key) satisfies Operator relative to Values.
//
// Typed by design: no any/interface{} — Casbin's reflective ...interface{}
// evaluation is explicitly rejected; all value comparison is over []string,
// consistent with XACML AttributeValue typed strings.
//
// Multiple Conditions within a Rule are AND-combined.
type Condition struct {
	// Source is the attribute namespace from which Key is resolved.
	Source AttributeSource
	// Key is the attribute name within Source. Must be non-empty.
	Key string
	// Operator is the comparison applied between Key's resolved value and Values.
	Operator Operator
	// Values is the right-hand side of the comparison. Must be non-empty and
	// contain no empty-string entries.
	Values []string
}

// Validate returns an error if the Condition is structurally invalid.
// All fields are required; Values must be non-empty with no empty-string entries.
func (c Condition) Validate() error {
	if err := c.Source.Validate(); err != nil {
		return err
	}
	if c.Key == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "abac: condition Key must not be empty")
	}
	if err := c.Operator.Validate(); err != nil {
		return err
	}
	if len(c.Values) == 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "abac: condition Values must not be empty")
	}
	for _, v := range c.Values {
		if v == "" {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "abac: condition Values must not contain empty strings")
		}
	}
	return nil
}
