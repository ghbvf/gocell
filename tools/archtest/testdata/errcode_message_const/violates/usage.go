// Package violates is a fixture for MESSAGE-CONST-LITERAL-01 negative
// case: errcode.New and errcode.Wrap are called with non-const messages,
// which the scanner must report as violations. Parsed by archtest in
// pure-AST mode; not intended to compile.
package violates

import (
	"fmt"

	"github.com/ghbvf/gocell/pkg/ctxcancel"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
)

// CallWithSprintfMessage is the canonical violation pattern: runtime data
// is interpolated into the message argument instead of WithDetails or
// WithInternal.
func CallWithSprintfMessage(field string) error {
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		fmt.Sprintf("violates: %s is required", field))
}

// CallWrapWithSprintfMessage violates MESSAGE-CONST-LITERAL-01 on errcode.Wrap:
// fmt.Sprintf result passed as the message argument.
func CallWrapWithSprintfMessage(cause error, tenant string) error {
	return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
		fmt.Sprintf("query failed for tenant %s", tenant), cause)
}

// CallNewWithConcatenation violates MESSAGE-CONST-LITERAL-01 via string +
// concatenation in the message argument.
func CallNewWithConcatenation(resource string) error {
	return errcode.New(errcode.KindNotFound, errcode.ErrCellNotFound,
		"resource "+resource+" not found")
}

// CallWritePublicWithSprintfMessage violates MESSAGE-CONST-LITERAL-01 by
// passing fmt.Sprintf output as the message argument of httputil.WritePublic.
// Verifies the rule's helper-coverage extension (PR #391 P2).
func CallWritePublicWithSprintfMessage(detail string) {
	httputil.WritePublic(nil, nil, errcode.KindInvalid,
		errcode.ErrValidationFailed,
		fmt.Sprintf("violates: %s", detail))
}

// CallWrapOrInfraWithSprintfMessage violates MESSAGE-CONST-LITERAL-01 by
// passing fmt.Sprintf output as the fallbackMsg argument of
// ctxcancel.WrapOrInfra. Verifies the rule's helper-coverage extension.
func CallWrapOrInfraWithSprintfMessage(err error, op, id, tenant string) error {
	return ctxcancel.WrapOrInfra(err, op, id, errcode.ErrInternal,
		fmt.Sprintf("infra: tenant %s", tenant))
}
