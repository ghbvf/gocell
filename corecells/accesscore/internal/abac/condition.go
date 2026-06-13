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

// ParseAttributeSource converts a wire/log spelling back to its AttributeSource
// value (inverse of String). Recognized codes: "subject", "resource",
// "environment". Any other input — including the empty string — returns an
// error (fail-closed).
func ParseAttributeSource(s string) (AttributeSource, error) {
	switch s {
	case "subject":
		return SourceSubject, nil
	case "resource":
		return SourceResource, nil
	case "environment":
		return SourceEnvironment, nil
	default:
		return 0, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "abac: unknown AttributeSource wire code")
	}
}

// Condition is a single predicate in a Rule. It evaluates to true when the
// attribute identified by (Source, Key) satisfies Operator relative to its
// right-hand side.
//
// The right-hand side is operator-discriminated and the two shapes are mutually
// exclusive (enforced by Validate, so an ill-formed mix is unexpressible):
//   - Static operators (OpEquals/OpNotEquals/OpIn/OpNotIn) compare against the
//     literal Values set; RHSSource/RHSKey MUST be zero.
//   - The cross-attribute operator (OpEqualsAttr) compares against ANOTHER
//     resolved attribute (RHSSource, RHSKey); Values MUST be empty.
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
	// Operator is the comparison applied between Key's resolved value and the
	// operator-discriminated right-hand side.
	Operator Operator
	// Values is the static right-hand side. Required (non-empty, no empty-string
	// entries) for static operators; MUST be empty for OpEqualsAttr.
	Values []string
	// RHSSource is the attribute namespace of the cross-attribute right-hand side.
	// Set only for OpEqualsAttr; MUST be zero for static operators.
	RHSSource AttributeSource
	// RHSKey is the attribute name of the cross-attribute right-hand side.
	// Set only for OpEqualsAttr; MUST be empty for static operators.
	RHSKey string
}

// HasRHS reports whether the Condition carries a cross-attribute right-hand-side
// reference, i.e. either RHSSource or RHSKey is set. It is the single source of
// the "does this condition have an RHS?" decision shared by every wire/persistence
// emit site (PG codec encode + the four HTTP response converters), so the predicate
// can never drift between them (#1977). A well-formed condition has both fields set
// (cross-attribute) or neither (static); a half-formed condition (one field set) is
// still reported as HasRHS so it round-trips losslessly to Validate, which rejects it.
func (c Condition) HasRHS() bool {
	return c.RHSSource != 0 || c.RHSKey != ""
}

// ParseRHS losslessly converts a wire/persisted right-hand-side pair into the
// domain (RHSSource, RHSKey) fields. It is the single inbound funnel shared by the
// HTTP request converter and the PG codec decoder: rhsSource is parsed via
// ParseAttributeSource only when non-empty (an unknown code is a fail-closed error),
// but rhsKey is ALWAYS preserved — even when rhsSource is empty. A half-formed RHS
// (rhsKey without rhsSource) therefore reaches Condition.Validate and is rejected
// there, instead of being silently dropped at the conversion boundary and downgraded
// to a static condition (#1977). Conversion stays a lossless translator; validateRHS
// remains the single source of structural truth.
func ParseRHS(rhsSource, rhsKey string) (AttributeSource, string, error) {
	if rhsSource == "" {
		return 0, rhsKey, nil
	}
	src, err := ParseAttributeSource(rhsSource)
	if err != nil {
		return 0, "", err
	}
	return src, rhsKey, nil
}

// RHSWire losslessly emits the right-hand-side pair for the wire/persisted
// representation, the single outbound counterpart to ParseRHS used by the HTTP
// response converters. A condition carrying an RHS (HasRHS) emits both fields so it
// round-trips faithfully; a static condition (neither field set) emits the empty
// pair, which downstream omitempty drops — keeping persisted static rows
// byte-identical and static responses field-free (#1977).
func (c Condition) RHSWire() (rhsSource, rhsKey string) {
	if c.HasRHS() {
		return c.RHSSource.String(), c.RHSKey
	}
	return "", ""
}

// Validate returns an error if the Condition is structurally invalid. Source,
// Key and Operator are always required; the right-hand side is checked by
// validateRHS according to the operator's shape.
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
	return c.validateRHS()
}

// validateRHS enforces the operator-discriminated right-hand side: OpEqualsAttr
// carries a (RHSSource, RHSKey) attribute reference and no static Values; every
// static operator carries non-empty Values and no RHS reference. Mixing the two
// shapes is a structural error — the sole funnel that makes an ill-formed
// cross-attribute condition unexpressible (#1977).
func (c Condition) validateRHS() error {
	if c.Operator == OpEqualsAttr {
		if err := c.RHSSource.Validate(); err != nil {
			return err
		}
		if err := authz.ValidAttributeKey(c.RHSKey); err != nil {
			return err
		}
		if len(c.Values) != 0 {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "abac: cross-attribute condition must not carry static Values")
		}
		return nil
	}
	if c.RHSSource != 0 || c.RHSKey != "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"abac: static condition must not carry a cross-attribute RHS reference")
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
