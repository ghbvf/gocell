// Package dlxoutcome seals the dead-letter routing outcome so the alertable
// failure metric is structurally inseparable from a $dead drop.
//
// (*mqtt.Subscriber).routeDeadLetter is typed to return an [Outcome]. The only
// way to obtain one is [Dropped] (which records the alertable
// RecordDeadLetterFailure signal) or [Captured] (which records the RecordDeadLetter
// success signal). Because Go forces every exit path of a value-returning function
// to return that value, a new drop branch that forgets the metric cannot produce an
// Outcome to return — it does not compile. "Every exit records a metric" is thus a
// compile-time property, not an archtest one. (Hard upgrade of archtest
// MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01, gh #1440.)
//
// The residual hole — the empty literal dlxoutcome.Outcome{} and a zero
// `var o Outcome` are still constructible from package mqtt — is irreducible in Go
// (there is no "no zero value" type modifier) and is closed by the H2 shape-guard
// in that archtest.
//
// NOTE: the generic type parameter R is load-bearing. It lets the constructors
// record via the caller's collector WITHOUT importing adapters/mqtt — which would
// be an import cycle (mqtt imports this package). Do NOT replace R with a concrete
// mqtt.ConsumeFailureReason; it would not compile.
package dlxoutcome

import "context"

// Outcome is an opaque, unforgeable proof token that a dead-letter routing
// decision recorded its outcome metric. Its sole field is a blank field of an
// unexported type, so package mqtt cannot name it in a literal; the only producers
// are [Dropped] and [Captured] below, each of which records a metric. The token
// carries no payload — the drop-vs-capture distinction is the recorded metric
// (asserted by dlxoutcome_test.go and adapters/mqtt deadletter_test.go), not state
// on the token. Its only job is to be a mandatory return type that forces every
// exit of routeDeadLetter through a recording constructor.
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
