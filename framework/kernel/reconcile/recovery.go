package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/framework/pkg/redaction"
)

// classify maps an error to its metric result label. It is the single source
// of classification truth for the Loop's process() switch — process() calls
// recoverReconcile then classify.
//
// Mapping:
//   - nil                    → resultSuccess
//   - ErrFencedWriteStale    → resultPermanent (stale-epoch write rejected by the
//     fencing CAS; this replica lost the fencing race — NOT a transient retry.
//     The entity will re-observe under the new leader's fresh trigger)
//   - IsPermanent(err)       → resultPermanent
//   - otherwise              → resultTransient
func classify(err error) resultLabel {
	if err == nil {
		return resultSuccess
	}
	// ErrFencedWriteStale is structurally permanent on this replica: the fencing
	// CAS rejected the write because a higher-epoch leader already wrote. Retrying
	// with the same epoch will always fail; only a fresh lease acquisition under
	// the new leader produces a valid epoch. Dead-letter here; a fresh Source
	// trigger re-observes the entity when the new leader is ready.
	if errors.Is(err, ErrFencedWriteStale) {
		return resultPermanent
	}
	if IsPermanent(err) {
		return resultPermanent
	}
	return resultTransient
}

// recoverReconcile invokes rec.Reconcile(ctx, req) with panic recovery so a
// single entity's panic cannot crash the worker goroutine. A recovered panic is
// returned as a transient (non-permanent) error that includes the entity ID and
// the recovered value. On no panic, the result of rec.Reconcile is returned
// verbatim.
//
// The returned panic error always satisfies classify(err) == resultTransient
// (it is never permanent, so the Loop retries rather than dead-lettering).
// This is an A-class recovery: the worker has no upstream recovery boundary,
// so the panic is absorbed and reported as a transient failure rather than
// re-panicked.
//
// O3: a recovered panic is a reconciler BUG and is logged at Error level here
// (correctness-affecting; per observability.md Error = "influences correctness").
// The metric result label stays "transient" — no 5th label is added.
func recoverReconcile(ctx context.Context, rec Reconciler, req Request, logger *slog.Logger, reconcilerID string) (res Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			panicValue := structuredPanicValue(r)
			err = fmt.Errorf("reconcile: recovered panic in Reconcile(entity=%q): %v", req.EntityID, panicValue)
			res = Result{}
			// Log at Error: a reconciler panic is a correctness bug, not ordinary
			// transient noise. The entity will be requeued (transient semantics), but
			// the operator must see this in the error log, not just the metric.
			logger.ErrorContext(ctx, "reconcile: reconciler panicked (BUG); entity requeued as transient",
				slog.String("reconciler", reconcilerID),
				slog.String("entity", req.EntityID),
				slog.Any("panic_value", panicValue),
				slog.Any("error", err))
		}
	}()
	return rec.Reconcile(ctx, req)
}

func structuredPanicValue(v any) (out any) {
	defer func() {
		if recover() != nil {
			out = safeRedactPanicFallback(v)
		}
	}()

	switch v.(type) {
	case nil, error, fmt.Stringer, string:
		return redaction.RedactAny(v)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return redaction.RedactAny(v)
	}
	var decoded any
	if err := json.Unmarshal(b, &decoded); err != nil {
		return redaction.RedactAny(v)
	}
	return redactDecodedPanicValue(decoded)
}

func safeRedactPanicFallback(v any) (out any) {
	defer func() {
		if recover() != nil {
			out = redaction.Mask
		}
	}()
	return redaction.RedactAny(v)
}

func redactDecodedPanicValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if redaction.IsSensitiveKey(k) {
				out[k] = redaction.Mask
				continue
			}
			out[k] = redactDecodedPanicValue(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = redactDecodedPanicValue(val)
		}
		return out
	case string:
		return redaction.RedactString(x)
	default:
		return x
	}
}
