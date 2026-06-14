// Package reflect_string_form_red is the shared RED fixture for
// REFLECT-STRING-ARG-SCANNER-01 — the single-source reflect.Value
// {Field,Method}ByName string-arg scanner used by every reflect blind-spot
// reverse self-check (SESSION-REVOKED-FIELD-ACCESS-01,
// CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01, DOMAIN-AUTHZ-*, etc.).
//
// It exercises every extraction shape the type-aware scanner must catch — the
// const-foldable argument forms the incumbent `call.Args[0].(*ast.BasicLit)` +
// `strings.Trim(…, "\"")` scan was blind to (issue #948 / PR #542), and the
// method-EXPRESSION form whose argument offset the len(Args)!=1 scan missed —
// plus every boundary the scanner must NOT cross:
//
//	reflect.ValueOf(sess).FieldByName(`RevokedAt`)             // raw string    → detected
//	reflect.ValueOf(sess).FieldByName(revokedAtField)          // const ident   → detected
//	reflect.ValueOf(sess).FieldByName("Revoked" + "At")        // concat        → detected
//	reflect.Value.FieldByName(reflect.ValueOf(sess), "X")      // method expr   → detected (name at Args[1])
//	reflect.ValueOf(u).MethodByName(`CanAuthenticate`)         // raw string    → detected
//	reflect.ValueOf(u).MethodByName(canAuthMethod)             // const ident   → detected
//	reflect.ValueOf(u).MethodByName("CanAuth"+"enticate")      // concat        → detected
//	reflect.Value.MethodByName(reflect.ValueOf(u), "X")        // method expr   → detected (name at Args[1])
//	reflect.ValueOf(sess).FieldByName(runtimeName)             // runtime-value → NOT detected (arg boundary)
//	fakeReflect{}.FieldByName / .MethodByName("X")             // non-reflect   → NOT detected (receiver boundary)
//	reflect.TypeOf(x).FieldByName / .MethodByName("X")         // reflect.Type  → NOT detected (Type, not Value)
//
// LOCATION RATIONALE: imports corecells/accesscore/internal/domain, so Go's
// internal-import rule requires this fixture to live under corecells/accesscore/.
// The testdata/ directory excludes the package from `go build ./...` and the
// `./...` package pattern while archtest loads it via an explicit
// packages.Load pattern (same convention as value_capture_red).
package reflect_string_form_red

import (
	"reflect"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/framework/runtime/auth/session"
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
	return reflect.ValueOf(u).MethodByName("CanAuth" + "enticate") // string concatenation
}

// ── Method-expression forms — must be detected. Go spec §Method expressions:
// the receiver becomes the explicit first argument, so the field/method NAME
// shifts to Args[1] (and len(Args)==2). The incumbent len(Args)!=1 + Args[0]
// scan was blind to these (issue #948 follow-up). ──

func badFieldMethodExpr(sess session.Session) reflect.Value {
	return reflect.Value.FieldByName(reflect.ValueOf(sess), "RevokedAt")
}

func badMethodMethodExpr(u domain.User) reflect.Value {
	return reflect.Value.MethodByName(reflect.ValueOf(u), "CanAuthenticate")
}

// ── Boundary 1 (arg): runtime-value field name folds to no constant, so it is
// structurally invisible to any static scan — the irreducible reflect caveat. ──

func boundaryRuntimeArg(sess session.Session, runtimeName string) reflect.Value {
	return reflect.ValueOf(sess).FieldByName(runtimeName)
}

// ── Boundary 2 (receiver — non-reflect type): a non-reflect type that happens
// to expose FieldByName/MethodByName(string) methods, called with const banned
// names. The typed receiver gate (reflect.Value only) must exclude both; the
// incumbent BasicLit-only scan WOULD have falsely flagged them. ──

type fakeReflect struct{}

func (fakeReflect) FieldByName(name string) bool  { return name == "" }
func (fakeReflect) MethodByName(name string) bool { return name == "" }

func boundaryNonReflectFieldReceiver() bool {
	return fakeReflect{}.FieldByName("RevokedAt")
}

func boundaryNonReflectMethodReceiver() bool {
	return fakeReflect{}.MethodByName("CanAuthenticate")
}

// ── Boundary 3 (receiver — reflect.Type): reflect.Type.{Field,Method}ByName
// returns StructField/Method METADATA, not the field/method VALUE, so it is not
// a read-bypass vector. The typed receiver gate (reflect.Value, not reflect.Type)
// must exclude it. ──

func boundaryReflectTypeField(sess session.Session) reflect.StructField {
	f, _ := reflect.TypeOf(sess).FieldByName("RevokedAt")
	return f
}

func boundaryReflectTypeMethod(u domain.User) (reflect.Method, bool) {
	return reflect.TypeOf(u).MethodByName("CanAuthenticate")
}
