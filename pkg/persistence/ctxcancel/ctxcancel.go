// Package ctxcancel provides shared helpers for translating context
// cancellation surfaced from IO operations (DB scan, RPC call, message bus
// claim) into structured *errcode.Error values that map to HTTP 499 (nginx
// "Client Closed Request") rather than 500.
//
// Cell repositories that expose long-running IO should use Detect / Wrap to
// keep client-direction signals (user disconnect, request timeout) out of
// the 5xx error rate. The wrapped *errcode.Error carries
// errcode.ErrClientCanceled with CategoryInfra so existing IsInfraError
// predicates (health bucket, retry classifiers) preserve their semantics
// while the HTTP layer routes the response to 499 + slog.Warn.
package ctxcancel

import (
	"context"
	"errors"
	"fmt"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// Detect reports whether err is or wraps context.Canceled or
// context.DeadlineExceeded. Returns false for nil.
func Detect(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// Wrap returns a structured *errcode.Error with code ErrClientCanceled and
// CategoryInfra when err is a context cancellation; nil otherwise.
//
//   - op: PascalCase operation label (e.g. "Insert", "ScanRow"); recorded in
//     InternalMessage for operator triage.
//   - identifier: caller-redacted resource locator (e.g. "key=foo",
//     "configID=…"); recorded in InternalMessage only — never the public
//     Message — to prevent sensitive-name leakage to clients.
//
// Public Message is the constant "request canceled". The wrapped err is
// preserved as Cause so errors.Is(returned, context.Canceled) still works
// for callers that need to detect cancellation up the stack.
func Wrap(err error, op, identifier string) *errcode.Error {
	if !Detect(err) {
		return nil
	}
	return &errcode.Error{
		Code:            errcode.ErrClientCanceled,
		Message:         "request canceled",
		InternalMessage: fmt.Sprintf("%s ctx canceled %s", op, identifier),
		Cause:           err,
		Category:        errcode.CategoryInfra,
	}
}
