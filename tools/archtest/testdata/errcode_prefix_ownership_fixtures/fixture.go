// Package errcode_prefix_ownership_fixtures is a fixture for
// ERRCODE-PREFIX-OWNERSHIP-01. It intentionally contains violations across
// every code-bearing mint helper in codeGatedCallees:
//
//  1. An unregistered string literal code passed to errcode.New.
//  2. A non-const runtime-assembled Code value passed to errcode.New.
//  3. Unregistered string literal codes passed to errcode.WrapInfra (code at
//     arg 0), httputil.WritePublic (arg 3), and ctxcancel.WrapOrInfra (arg 3) —
//     these prove the code gate covers every helper, not just New/Wrap.
//
// This fixture is scanned in AST-only mode (no packages.Load), so the local
// package names ("errcode" / "httputil" / "ctxcancel") are sufficient for the
// scanner's AST-only fallback path.
//
// NOT intended to compile.
package errcode_prefix_ownership_fixtures

import (
	"context"
	"net/http"

	"github.com/ghbvf/gocell/framework/pkg/ctxcancel"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/httputil"
)

// ViolatesUnregisteredPrefix calls errcode.New with a string literal that
// has no registered prefix owner — triggers the unregistered-prefix diagnostic.
func ViolatesUnregisteredPrefix() error {
	return errcode.New(errcode.KindInvalid, "ERR_UNREGISTEREDBOGUS_NOPE", "unregistered prefix")
}

// ViolatesNonConstMint calls errcode.New with a runtime-assembled Code value —
// triggers the non-const hard-fail diagnostic.
func ViolatesNonConstMint(suffix string) error {
	bogus := "ERR_" + suffix
	return errcode.New(errcode.KindInvalid, errcode.Code(bogus), "non-const mint")
}

// ViolatesWrapInfraUnregistered mints via errcode.WrapInfra (code at arg 0)
// with an unregistered prefix — proves WrapInfra is covered by the code gate.
func ViolatesWrapInfraUnregistered(cause error) error {
	return errcode.WrapInfra("ERR_FAKEINFRABOGUS_NOPE", "infra", cause)
}

// ViolatesWritePublicUnregistered mints via httputil.WritePublic (code at arg 3)
// with an unregistered prefix.
func ViolatesWritePublicUnregistered(ctx context.Context, w http.ResponseWriter) {
	httputil.WritePublic(ctx, w, errcode.KindInvalid, "ERR_FAKEPUBLICBOGUS_NOPE", "public")
}

// ViolatesWrapOrInfraUnregistered mints via ctxcancel.WrapOrInfra (code at arg 3)
// with an unregistered prefix.
func ViolatesWrapOrInfraUnregistered(cause error) error {
	return ctxcancel.WrapOrInfra(cause, "op", "id", "ERR_FAKEWRAPORBOGUS_NOPE", "fallback")
}
