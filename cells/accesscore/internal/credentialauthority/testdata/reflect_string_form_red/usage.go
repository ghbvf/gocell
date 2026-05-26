// Package reflect_string_form_red is the shared RED fixture for
// REFLECT-STRING-ARG-SCANNER-01 — the single-source reflect.Value
// {Field,Method}ByName string-arg scanner used by every reflect blind-spot
// reverse self-check (SESSION-REVOKED-FIELD-ACCESS-01,
// CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01, DOMAIN-AUTHZ-*, etc.).
//
// It exercises the four extraction shapes that the incumbent
// `call.Args[0].(*ast.BasicLit)` + `strings.Trim(…, "\"")` scan was blind to
// (issue #948 / PR #542), plus the two boundaries the type-aware scanner must
// NOT cross:
//
//	reflect.ValueOf(sess).FieldByName(`RevokedAt`)        // raw string   → detected
//	reflect.ValueOf(sess).FieldByName(revokedAtField)     // const ident  → detected
//	reflect.ValueOf(sess).FieldByName("Revoked" + "At")   // concat       → detected
//	reflect.ValueOf(u).MethodByName(`CanAuthenticate`)    // raw string   → detected
//	reflect.ValueOf(u).MethodByName(canAuthMethod)        // const ident  → detected
//	reflect.ValueOf(u).MethodByName("CanAuth"+"enticate") // concat       → detected
//	reflect.ValueOf(sess).FieldByName(runtimeName)        // runtime-value→ NOT detected (arg boundary)
//	fakeReflect{}.FieldByName("RevokedAt")                // non-reflect  → NOT detected (receiver boundary)
//
// LOCATION RATIONALE: imports cells/accesscore/internal/domain, so Go's
// internal-import rule requires this fixture to live under cells/accesscore/.
// The testdata/ directory excludes the package from `go build ./...` and the
// `./...` package pattern while archtest loads it via an explicit
// packages.Load pattern (same convention as value_capture_red).
package reflect_string_form_red

import (
	"reflect"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// const-ident forms of the protected names.
const (
	revokedAtField = "RevokedAt"
	canAuthMethod  = "CanAuthenticate"
)

// ── FieldByName("RevokedAt") — three const-expressible forms (must be detected) ──

func badFieldRaw(sess session.Session) reflect.Value {
	return reflect.ValueOf(sess).FieldByName(`RevokedAt`) // raw string literal
}

func badFieldConst(sess session.Session) reflect.Value {
	return reflect.ValueOf(sess).FieldByName(revokedAtField) // const ident
}

func badFieldConcat(sess session.Session) reflect.Value {
	return reflect.ValueOf(sess).FieldByName("Revoked" + "At") // string concatenation
}

// ── MethodByName("CanAuthenticate") — three const-expressible forms (must be detected) ──

func badMethodRaw(u domain.User) reflect.Value {
	return reflect.ValueOf(u).MethodByName(`CanAuthenticate`)
}

func badMethodConst(u domain.User) reflect.Value {
	return reflect.ValueOf(u).MethodByName(canAuthMethod)
}

func badMethodConcat(u domain.User) reflect.Value {
	return reflect.ValueOf(u).MethodByName("CanAuth" + "enticate")
}

// ── Boundary 1 (arg): runtime-value field name folds to no constant, so it is
// structurally invisible to any static scan — the irreducible reflect caveat. ──

func boundaryRuntimeArg(sess session.Session, runtimeName string) reflect.Value {
	return reflect.ValueOf(sess).FieldByName(runtimeName)
}

// ── Boundary 2 (receiver): a non-reflect type that happens to expose a
// FieldByName(string) method, called with a const banned name. The typed
// receiver gate (reflect.Value only) must exclude it; the incumbent
// BasicLit-only scan WOULD have falsely flagged it. ──

type fakeReflect struct{}

func (fakeReflect) FieldByName(name string) bool { return name == "" }

func boundaryNonReflectReceiver() bool {
	return fakeReflect{}.FieldByName("RevokedAt")
}
