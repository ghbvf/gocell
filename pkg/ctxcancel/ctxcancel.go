// Package ctxcancel provides shared helpers for translating context
// cancellation surfaced from IO operations (DB scan, RPC call, message bus
// claim) into structured *errcode.Error values with the correct HTTP status
// for each ctx error variant:
//
//   - context.Canceled         → ErrClientCanceled  → HTTP 499 + slog.Warn
//     (real client-direction signal: keeps 5xx SLO clean)
//   - context.DeadlineExceeded → ErrServerTimeout   → HTTP 504 + slog.Error
//     (real server-direction timeout: feeds 5xx alerting + SDK retry)
//
// Splitting by ctx error variant aligns with NGINX (499 vs 504), Kratos
// transport/http/status (Canceled→499, DeadlineExceeded→504), and standard
// load-balancer / SDK retry expectations: 499 is benign client noise, 504
// is a real timeout that should be alerted on.
//
// Both wrapped errors carry CategoryInfra so existing IsInfraError
// predicates (health bucket, retry classifiers) preserve their semantics.
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

// Wrap returns a structured *errcode.Error for context cancellation errors,
// classifying by ctx error variant; nil when err is not a ctx cancellation.
//
//   - context.Canceled         → Code=ErrClientCanceled, public Message
//     "request canceled"        (HTTP 499, log4xx, span Unset)
//   - context.DeadlineExceeded → Code=ErrServerTimeout,  public Message
//     "request timed out"       (HTTP 504, log5xx, span Error)
//
// Parameters:
//   - op: PascalCase operation label (e.g. "Insert", "ScanRow"); recorded in
//     InternalMessage for operator triage.
//   - identifier: caller-redacted resource locator (e.g. "key=foo",
//     "configID=…"); recorded in InternalMessage only.
//
// The wrapped err is preserved as Cause so errors.Is(returned, context.Canceled)
// or errors.Is(returned, context.DeadlineExceeded) still works for callers
// that need to detect cancellation up the stack.
//
// Details["reason"] mirrors the variant ("canceled" / "deadline_exceeded")
// for observability fan-out: tracing span attribute (client.cancel.reason
// for 499 only) and log4xx cancel_reason field. The HTTP status carries the
// primary signal; the reason field provides supplementary low-cardinality
// dimension for dashboards that bucket by both.
//
// Privacy contract:
//   - Public Message and Details are consumed by pkg/httputil.writeErrcodeError
//     and appear in 4xx HTTP response bodies (5xx responses sanitize Message
//     to "internal server error" and strip Details per the standard pipeline).
//     The reason enum is intentionally low-cardinality and free of user
//     identifiers — safe to expose.
//   - InternalMessage carries the operator-grade op/identifier (key names,
//     config IDs) and is routed to slog only; pkg/httputil never writes
//     InternalMessage to the response body, so identifier may freely contain
//     such detail.
//
// Maintainers extending Details MUST keep new fields enum-typed and
// caller-redacted, otherwise rotate them through InternalMessage instead.
func Wrap(err error, op, identifier string) *errcode.Error {
	if !Detect(err) {
		return nil
	}
	code, message := codeAndMessageFor(err)
	return &errcode.Error{
		Code:            code,
		Message:         message,
		InternalMessage: fmt.Sprintf("%s ctx canceled %s", op, identifier),
		Cause:           err,
		Category:        errcode.CategoryInfra,
		Details:         map[string]any{DetailsKeyReason: reasonOf(err)},
	}
}

// codeAndMessageFor selects the (errcode, public message) pair based on the
// originating ctx error: context.DeadlineExceeded → ErrServerTimeout/504,
// anything else (default = context.Canceled branch) → ErrClientCanceled/499.
// Kept separate from reasonOf so the 499/504 split and the reason enum can
// evolve independently (e.g. if a third variant is ever added).
func codeAndMessageFor(err error) (errcode.Code, string) {
	if errors.Is(err, context.DeadlineExceeded) {
		return errcode.ErrServerTimeout, "request timed out"
	}
	return errcode.ErrClientCanceled, "request canceled"
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
