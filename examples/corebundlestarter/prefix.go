package main

import "github.com/ghbvf/gocell/pkg/errcode"

// init registers the corebundlestarter error-code prefix with the global
// errcode prefix registry (#1091 reference pattern).
//
// Every external cell or module should register its ERR_ prefix from an
// init() so that prefix collisions with the gocell platform (or with sibling
// modules in the same binary) are detected fail-fast at process startup rather
// than silently producing ambiguous ownership at runtime.
//
// External cell workflow (3 steps):
//  1. RegisterPrefix in init() — done here.
//  2. Declare error codes: const ErrStarterXxx errcode.Code = "ERR_STARTER_XXX"
//     in your errors.go (or equivalent) for every code your cell mints.
//  3. Use the codes: errcode.New(errcode.KindInvalid, ErrStarterXxx, "description", ...)
//     (errcode.New signature is New(kind Kind, code Code, message string, ...)).
//
// The registration call is the teaching artifact here; corebundlestarter does
// not currently declare any ERR_STARTER_* constants — see #1091.
func init() {
	errcode.RegisterPrefix("ERR_STARTER_", "github.com/ghbvf/gocell/examples/corebundlestarter")
}
