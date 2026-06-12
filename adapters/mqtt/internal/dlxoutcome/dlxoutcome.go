// Package dlxoutcome seals the dead-letter routing outcome so the alertable
// failure metric is bound to a $dead drop via a sealed-construction funnel.
//
// (*mqtt.Subscriber).routeDeadLetter is typed to return an [Outcome]. The type
// system contributes a FLOOR: every exit must return SOME Outcome value (a bare
// `return` is a compile error). The two sanctioned producers are [Dropped] (records
// the alertable RecordDeadLetterFailure signal) and [Captured] (records the
// RecordDeadLetter success signal). But the floor alone does NOT force the metric:
// a stateless proof token has a zero value that is always constructible
// (Outcome{}, var o, *new(Outcome), …), so a drop branch could forge one and still
// compile. That gap is closed by the archtest funnel, NOT the compiler: H2 forbids
// package mqtt forging an Outcome (composite-lit / zero-var / new), H4 forbids this
// package gaining a third, non-recording producer, H3a checks Dropped/Captured
// actually record. Floor + funnel ⇒ every Outcome routeDeadLetter returns came from
// a recording path.
//
// This is graded Medium (the closure is archtest), NOT Hard: genuine type-system
// Hard is unreachable for a stateless token — it would require proving a side effect
// happened, but Go can only prove a value was produced, and a zero value carries no
// such proof. See the MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01 archtest godoc §grading and
// ADR-048 §Amendment 2026-06-11 (gh #1440 / gh #1873 F1).
//
// The irreducible residual — a zero Outcome pulled from an open-ended set of
// extraction sites (array/map element, reflect.Zero, an IIFE) — is what keeps this
// Medium: Go has no "no zero value" type modifier and the forge surface is
// open-ended, so no archtest can bound it. The ENUMERABLE forge forms (empty
// literal, zero var, new — each alias-aware via types.Unalias) ARE closed, by H2.
//
// NOTE: the generic type parameter R is load-bearing. It lets the constructors
// record via the caller's collector WITHOUT importing adapters/mqtt — which would
// be an import cycle (mqtt imports this package). Do NOT replace R with a concrete
// mqtt.ConsumeFailureReason; it would not compile.
package dlxoutcome

import "context"

// Outcome is an opaque proof token that a dead-letter routing decision went
// through a recording constructor. Its sole field is a blank field of an unexported
// type, so package mqtt cannot construct a CONTENTFUL Outcome — but the EMPTY value
// (Outcome{}, *new(Outcome), …) is still constructible (Go has no "no zero value"
// type modifier), so the token is NOT unforgeable at the type level: forging from
// mqtt is blocked by the H2 archtest, and adding a non-recording producer here is
// blocked by H4. The two sanctioned producers are [Dropped] and [Captured] below,
// each of which records a metric. The token carries no payload — the drop-vs-capture
// distinction is the recorded metric (asserted by dlxoutcome_test.go and
// adapters/mqtt deadletter_test.go), not state on the token. Its job is to be a
// mandatory return type whose FLOOR forces every exit of routeDeadLetter to return
// some Outcome; the funnel (H2/H4/H3a) forces that Outcome to come from a recording
// constructor.
type Outcome struct{ _ recordedToken }

type recordedToken struct{}

// Dropped records the alertable dead-letter FAILURE signal (mqtt_dlx_failed_total)
// for a $dead drop and returns the proof token. Use it on every drop exit.
func Dropped[R any](ctx context.Context, rec interface {
	RecordDeadLetterFailure(context.Context, R)
}, reason R,
) Outcome {
	rec.RecordDeadLetterFailure(ctx, reason)
	return Outcome{}
}

// Captured records the dead-letter SUCCESS signal (mqtt_dlx_total) for a message
// actually captured in $dead and returns the proof token. Use it on the success exit.
func Captured[R any](ctx context.Context, rec interface {
	RecordDeadLetter(context.Context, R)
}, reason R,
) Outcome {
	rec.RecordDeadLetter(ctx, reason)
	return Outcome{}
}
