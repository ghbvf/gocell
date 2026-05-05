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
	"log/slog"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// DetailsKeyReason is the slog.Attr key under which Wrap records the
// originating ctx-cancel reason ("canceled" | "deadline_exceeded"). Exported
// so consumers (HTTP boundary, tracing middleware, log4xx) reference one
// string instead of duplicating the literal at multiple call sites.
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
	if errors.Is(err, context.DeadlineExceeded) {
		return errcode.Wrap(
			errcode.KindDeadlineExceeded,
			errcode.ErrServerTimeout,
			"request timed out",
			err,
			errcode.WithCategory(errcode.CategoryInfra),
			errcode.WithInternal(fmt.Sprintf("%s ctx canceled %s", op, identifier)),
			errcode.WithDetails(slog.String(DetailsKeyReason, ReasonDeadlineExceeded)),
		)
	}
	return errcode.Wrap(
		errcode.KindClientClosed,
		errcode.ErrClientCanceled,
		"request canceled",
		err,
		errcode.WithCategory(errcode.CategoryInfra),
		errcode.WithInternal(fmt.Sprintf("%s ctx canceled %s", op, identifier)),
		errcode.WithDetails(slog.String(DetailsKeyReason, ReasonCanceled)),
	)
}

// WrapOrInfra is the canonical convenience for the IO-boundary pattern:
//
//	if cancelErr := ctxcancel.Wrap(err, op, id); cancelErr != nil {
//	    return cancelErr
//	}
//	return errcode.Wrap(errcode.KindInternal, fallbackCode, fallbackMsg, err, errcode.WithCategory(errcode.CategoryInfra))
//
// One call replaces both branches: a context cancellation surfaces as the
// canonical 499/504 split (Canceled→ErrClientCanceled, DeadlineExceeded→
// ErrServerTimeout) while every other error gets uniformly mapped to the
// caller-supplied infra-category errcode with err preserved as Cause.
//
// Use at repository / RPC client / message-bus call sites where the only
// branching needed is ctx-cancel vs generic infra failure. When the fallback
// path needs *additional* differentiation (a domain not-found branch, a
// PascalCase InternalMessage template, a non-Infra Category), keep the
// inline pattern — this helper deliberately omits those degrees of freedom
// to avoid a six-parameter signature.
func WrapOrInfra(err error, op, identifier string, fallbackCode errcode.Code, fallbackMsg string) error {
	if cancelErr := Wrap(err, op, identifier); cancelErr != nil {
		return cancelErr
	}
	// Struct literal is intentional: WrapOrInfra is a bridge helper that
	// receives caller-supplied dynamic messages (always string literals at
	// call sites). ERRCODE-KIND-LITERAL-01 exempts pkg/ctxcancel/ for this
	// reason; MESSAGE-CONST-LITERAL-01 does not apply to struct fields.
	return &errcode.Error{Kind: errcode.KindInternal, Code: fallbackCode, Message: fallbackMsg, Cause: err, Category: errcode.CategoryInfra}
}

// ReasonFromDetails extracts and canonicalizes the reason value from an
// *errcode.Error, returning the empty string when no recognized reason is
// present. Callers (HTTP boundary, log4xx, tracing middleware) MUST go
// through this helper instead of inspecting Details directly, so the
// low-cardinality enum contract holds even when a non-canonical producer
// (third-party code, future Wrap variants that haven't migrated yet,
// malicious upstream) attaches an arbitrary attribute.
//
// Fail-closed semantics: any value that is not exactly ReasonCanceled or
// ReasonDeadlineExceeded yields "" (treat as "unknown / instrumentation
// gap"). This prevents user-derived strings from polluting span attribute
// cardinality (Tempo/Jaeger backends are extremely sensitive to high
// cardinality on a single attribute) and from leaking arbitrary content
// into structured logs.
func ReasonFromDetails(e *errcode.Error) string {
	if e == nil {
		return ""
	}
	attr, ok := e.FindAttr(DetailsKeyReason)
	if !ok {
		return ""
	}
	if attr.Value.Kind() != slog.KindString {
		return ""
	}
	switch s := attr.Value.String(); s {
	case ReasonCanceled, ReasonDeadlineExceeded:
		return s
	default:
		return ""
	}
}
