// Package abac is the ABAC (Attribute-Based Access Control) policy authoring
// and persistence model for accesscore. It contains the domain types for
// Policy, Rule, Condition, Operator, and AttributeSource — pure value objects
// with typed validation, no clock dependency, and no wire types.
//
// This package may import: pkg/authz, pkg/tenant, pkg/errcode, stdlib.
// It must NOT import: kernel/, runtime/, adapters/, corecells/ (other than itself).
//
// Design references:
//   - AWS Cedar (model/policy separation, default-deny/forbid-wins)
//   - XACML 3.0 §5 — Policy / Rule / Condition type hierarchy
//   - Casbin model/policy domain (reject reflective ...interface{})
package abac
