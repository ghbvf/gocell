package reconcile

import (
	"errors"
	"time"
)

// Result is the scheduling hint a Reconciler returns to the Loop. Unlike
// controller-runtime's reconcile.Result { Requeue bool; RequeueAfter Duration },
// GoCell carries only RequeueAfter: "requeue immediately / at the default tick"
// is expressed as RequeueAfter == 0, so the Requeue bool is redundant and
// dropped. There is no Priority dimension.
//
// INVARIANT: RECONCILE-RESULT-FIELDS-FROZEN-01 — the field set is frozen to
// exactly { RequeueAfter time.Duration }. Re-introducing Requeue bool or a
// Priority int (controller-runtime / K8s-scheduler residue) trips an exact-set
// reflect assertion in CI.
type Result struct {
	// RequeueAfter, when > 0, asks the Loop to re-observe this entity after the
	// given duration. When 0, the entity is re-observed at the Loop's default
	// tick interval. It is ignored when Reconcile also returns a non-nil error
	// (the error path drives backoff instead).
	RequeueAfter time.Duration
}

// normalizedRequeueAfter clamps a negative RequeueAfter to 0. A negative
// duration is a programmer error; treating it as 0 (== default tick) keeps the
// Loop from scheduling in the past or panicking. Used by the Loop when reading a
// Reconciler's Result.
func (r Result) normalizedRequeueAfter() time.Duration {
	if r.RequeueAfter < 0 {
		return 0
	}
	return r.RequeueAfter
}

// permanentError is the sealed marker wrapping a non-retryable error. It is
// unexported on purpose: PermanentError is the only constructor and IsPermanent
// is the only classifier, so "a permanent error that did not pass through
// PermanentError" is unrepresentable outside this package (typed-marker funnel,
// not a string/sentinel compare). This intentionally does NOT reuse
// kernel/outbox.PermanentError: reconcile must not couple to the outbox domain
// (and outbox already keeps its marker exported for its own broker semantics).
type permanentError struct {
	err error
}

func (e *permanentError) Error() string {
	return "permanent: " + e.err.Error()
}

// Unwrap lets errors.Is / errors.As reach the wrapped cause, so a consumer that
// wrapped a sentinel keeps it recoverable through the permanent marker.
func (e *permanentError) Unwrap() error {
	return e.err
}

// PermanentError wraps err to tell the Loop the entity must NOT be retried:
// retrying cannot change the outcome (revoked cert, malformed row, policy
// rejection). The Loop records a dead-letter metric and stops scheduling the
// entity until a fresh trigger re-observes it. PermanentError(nil) returns nil
// (no spurious wrapper).
func PermanentError(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err: err}
}

// IsPermanent reports whether err, or any error it wraps, was marked by
// PermanentError. It sees through fmt.Errorf %w chains.
func IsPermanent(err error) bool {
	var pe *permanentError
	return errors.As(err, &pe)
}
