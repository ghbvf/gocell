package reconcile

import "time"

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

// permanentError is the sealed marker wrapping a non-retryable error. The seal
// has two layers, because in Go type identity alone is NOT proof of
// construction: reflect.New can instantiate this unexported type (its
// reflect.Type is reachable via the public constructor's return value) and
// inject it through a foreign As hook or Unwrap. So:
//
//  1. The type is unexported — no package-external struct literal.
//  2. The non-nil err field is a construction proof — reflect cannot set an
//     unexported field (reflect.Value.CanSet is false), and PermanentError(nil)
//     returns nil, so a genuine marker ALWAYS has a non-nil cause while any
//     reflect-forged value has a nil one.
//
// IsPermanent matches only a *permanentError WITH a non-nil err and never
// consults a foreign As/Is hook, so within Go's type system a permanent
// classification cannot be reached without passing through PermanentError.
//
// This intentionally does NOT reuse kernel/outbox.PermanentError: reconcile
// must not couple to the outbox domain (and outbox already keeps its marker
// exported for its own broker semantics).
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

// PermanentError wraps err to mark the entity non-retryable: retrying cannot
// change the outcome (revoked cert, malformed row, policy rejection). Once the
// Loop lands (PR-A3) it records a dead-letter metric and stops scheduling the
// entity until a fresh trigger re-observes it. PermanentError(nil) returns nil
// (no spurious wrapper) — this nil-guard is load-bearing for IsPermanent's
// construction proof: a genuine permanentError always has a non-nil cause.
func PermanentError(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err: err}
}

// IsPermanent reports whether err, or any error it structurally wraps, is a
// genuinely-constructed permanentError. It walks the unwrap tree itself —
// single (Unwrap() error) and multi (Unwrap() []error, e.g. errors.Join) — so
// it sees through fmt.Errorf %w chains.
//
// It deliberately does NOT use errors.As. errors.As honors a foreign error's
// As(any) bool hook, which a foreign error can use (directly, or via reflect to
// set the unexported target) to report permanent without passing through
// PermanentError; this walk never consults that hook. Matching the
// *permanentError type is necessary but not sufficient — reflect can forge the
// unexported type and inject it via a foreign Unwrap — so the err != nil check
// is the construction proof (see permanentError's godoc).
func IsPermanent(err error) bool {
	for {
		switch x := err.(type) { //nolint:errorlint // concrete-type walk by design; see IsPermanent godoc
		case nil:
			return false
		case *permanentError:
			return x.err != nil // construction proof: reflect-forged zero value has a nil cause
		case interface{ Unwrap() error }:
			err = x.Unwrap()
		case interface{ Unwrap() []error }:
			for _, e := range x.Unwrap() {
				if IsPermanent(e) {
					return true
				}
			}
			return false
		default:
			return false
		}
	}
}
