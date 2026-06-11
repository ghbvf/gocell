package abac

import (
	"github.com/ghbvf/gocell/pkg/authz"
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
// These three sources are the complete attribute-condition universe. "Action"
// and "resource-type" scoping (which policy applies to which action + resource
// combination — analogous to Cedar's Rule.Target or XACML's Policy Target) is
// deliberately NOT modeled as a first-class target. PR-7 (#1345) resolved the
// applicability design as condition-based evaluate-all: the policy evaluator
// loads every policy of the tenant and evaluates each rule's conditions, with
// applicability expressed THROUGH conditions rather than a separate
// target-matching phase. A first-class action/resource-type Rule.Target is
// deferred under YAGNI until a real need appears. Design decision recorded in
// docs/plans/specs/1220-tenancy-abac-dataperm/research.md §1.1.
//
// ref: XACML 3.0 §5.7 — Subject, Resource, Action, Environment attribute categories.
type AttributeSource uint8

const (
	// SourceSubject resolves the attribute from the authenticated principal's
	// claims or profile. Example key: "department", "job_level", "clearance".
	SourceSubject AttributeSource = iota + 1
	// SourceResource resolves the attribute from the protected resource's
	// metadata. Example key: "classification", "owner_id", "sensitivity_level".
	// Attributes are fetched at evaluation time from the injected
	// ResourceAttributeProvider (ABAC PIP, PR-9 #1347), scoped to the request
	// tenant in the same transaction block as policy loading
	// (RESOURCE-ATTR-TENANT-SHARING-01). An absent key resolves found=false —
	// fail-closed (condition unsatisfied). PG-backed store is a follow-up issue.
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
	if err := authz.ValidAttributeKey(c.Key); err != nil {
		return err
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
