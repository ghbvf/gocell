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

// DetailsKeyReason is the *errcode.Error.Details map key under which Wrap
// records the originating ctx-cancel reason ("canceled" |
// "deadline_exceeded"). Exported so consumers (HTTP boundary, tracing
// middleware, log4xx) reference one string instead of duplicating the
// literal at multiple call sites.
const DetailsKeyReason = "reason"

// Reason values surfaced via Details[DetailsKeyReason]. Low-cardinality
// enum safe to expose in HTTP response bodies, slog records, and tracing
// span attributes.
const (
	ReasonCanceled         = "canceled"
	ReasonDeadlineExceeded = "deadline_exceeded"
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
//     "configID=…"); recorded in InternalMessage only.
//
// Public Message is the constant "request canceled". The wrapped err is
// preserved as Cause so errors.Is(returned, context.Canceled) still works
// for callers that need to detect cancellation up the stack.
//
// Details["reason"] disambiguates the originating ctx error so the HTTP
// boundary can route a distinct tracing attribute, span the log4xx
// cancel_reason field, and dashboards can split "client disconnect"
// (canceled) from "server-side / inherited timeout" (deadline_exceeded)
// without a second log query. Both still map to 499 — the split is in
// observability, not in the public response status.
//
// Privacy contract:
//   - Public Message (the constant "request canceled") and Details
//     (currently `{"reason": "canceled" | "deadline_exceeded"}`) are
//     consumed by pkg/httputil.writeErrcodeError and DO appear in the
//     4xx HTTP response body. The reason enum is intentionally
//     low-cardinality and free of user identifiers — safe to expose.
//   - InternalMessage carries the operator-grade op/identifier (key
//     names, config IDs) and is routed to log4xx slog.Warn only;
//     pkg/httputil never writes InternalMessage to the response body,
//     so identifier may freely contain such detail.
//
// Maintainers extending Details MUST keep new fields enum-typed and
// caller-redacted, otherwise rotate them through InternalMessage instead.
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
		Details:         map[string]any{DetailsKeyReason: reasonOf(err)},
	}
}

// reasonOf maps a context cancellation error to a stable low-cardinality
// label safe to attach to a tracing span attribute. Defaults to
// ReasonCanceled for the context.Canceled branch (covers both raw and
// wrapped sentinels).
func reasonOf(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return ReasonDeadlineExceeded
	}
	return ReasonCanceled
}
