package webhook

import (
	"errors"
	"slices"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// RetrySchedule is the fixed sequence of wait intervals between outbound
// delivery retries. The first delivery attempt is immediate; delays[i] is the
// wait before retry i+1. The schedule is value-immutable: DefaultSvixSchedule
// returns a fresh copy and there is no setter.
//
// DefaultSvixSchedule is the canonical default. The per-attempt wall-clock
// delays ARE honored at runtime (gh #1458): the webhook-dispatch bootstrap
// drain copies Delays() onto outbox.Subscription.BrokerDelaySchedule, and the
// Subscriber applies broker-native delayed re-delivery (rabbitmq TTL+DLX
// delay-tier queues / in-memory clock-timed schedule), durable across restarts.
// DelayFor is the single-tier lookup; Delays is the bulk seam the wiring consumes.
//
// See docs/architecture/202606012052-1160-adr-webhook-retry-default.md §D5.
type RetrySchedule struct {
	delays []time.Duration
}

// svixRetryDelayN are the 7 Svix default retry intervals (PROD-DURATION-CONST-01:
// production duration literals must be named package-level consts). Steps 6 and
// 7 are both 10h by Svix's table — the tail interval is capped, not growing.
const (
	svixRetryDelay1 = 5 * time.Second
	svixRetryDelay2 = 5 * time.Minute
	svixRetryDelay3 = 30 * time.Minute
	svixRetryDelay4 = 2 * time.Hour
	svixRetryDelay5 = 5 * time.Hour
	svixRetryDelay6 = 10 * time.Hour
	svixRetryDelay7 = 10 * time.Hour
)

// DefaultSvixSchedule returns the Svix default retry schedule: 7 retries after
// the immediate first attempt (8 deliveries total), spaced
// 5s / 5min / 30min / 2h / 5h / 10h / 10h.
//
// ref: svix/svix-webhooks docs.svix.com/retries (official retry table)
// ref: standard-webhooks/standard-webhooks spec/standard-webhooks.md (retry guidance)
func DefaultSvixSchedule() RetrySchedule {
	return RetrySchedule{delays: []time.Duration{
		svixRetryDelay1, svixRetryDelay2, svixRetryDelay3, svixRetryDelay4,
		svixRetryDelay5, svixRetryDelay6, svixRetryDelay7,
	}}
}

// MaxRetries is the number of retries after the first (immediate) attempt.
func (s RetrySchedule) MaxRetries() int { return len(s.delays) }

// Attempts is the total number of delivery attempts: the immediate first
// delivery plus MaxRetries retries.
func (s RetrySchedule) Attempts() int { return len(s.delays) + 1 }

// DelayFor returns the wait before retry n (1-based: n==1 is the first retry).
// ok is false when n is out of range (n < 1 or n > MaxRetries), i.e. the retry
// budget is exhausted.
func (s RetrySchedule) DelayFor(n int) (delay time.Duration, ok bool) {
	if n < 1 || n > len(s.delays) {
		return 0, false
	}
	return s.delays[n-1], true
}

// Delays returns a copy of the retry-delay tiers, in retry order (delays[i] is
// the wait before retry i+1). It is the bulk-read seam the broker-delay wiring
// (#1458) consumes to build per-tier delay queues / schedule-driven redelivery;
// DelayFor remains the single-tier lookup. The slice is cloned so callers cannot
// mutate the schedule's internal state — call it once at wiring time (e.g. in
// the dispatch consumer builder), not on the per-delivery hot path.
func (s RetrySchedule) Delays() []time.Duration { return slices.Clone(s.delays) }

// Classify maps an outbound delivery outcome to an outbox.Disposition per the
// webhook retry-default ADR (standard-webhooks / Svix aligned). Exactly one of
// the two inputs is meaningful per call: pass the transport error (statusCode
// ignored) when client.Do failed, or statusCode with a nil error when a
// response was received. When transportErr is non-nil the statusCode is
// ignored; pass 0 by convention.
//
// Mapping (standard-webhooks / Svix: the receiver MUST return 2xx to acknowledge;
// every other outcome is a delivery failure the sender retries):
//   - transportErr != nil:
//   - SSRF-blocked (ErrWebhookSSRFBlocked — bad target URL, blocked dial,
//     or denied redirect) → Reject (permanent: a misconfigured/hostile
//     target never succeeds on retry).
//   - otherwise (DNS resolution failure, timeout, connection refused, …) →
//     Requeue (transient).
//   - 2xx → Ack.
//   - every non-2xx status (3xx / 4xx / 5xx) → Requeue. A receiver's non-2xx
//     usually reflects a transient condition on its side (deploy gap → 404,
//     secret rotation → 401, throttle → 429, server fault → 5xx); the sender
//     cannot distinguish a "permanent" 4xx from a transient one, so it retries
//     until the budget is exhausted (then ConsumerBase dead-letters). 3xx
//     normally never reaches this branch because redirects are denied at the
//     transport layer and surface as an SSRF transport error (→ Reject above).
func Classify(statusCode int, transportErr error) outbox.Disposition {
	if transportErr != nil {
		var ee *errcode.Error
		if errors.As(transportErr, &ee) && ee.Code == errcode.ErrWebhookSSRFBlocked {
			return outbox.DispositionReject
		}
		return outbox.DispositionRequeue
	}
	if statusCode >= 200 && statusCode < 300 {
		return outbox.DispositionAck
	}
	return outbox.DispositionRequeue
}
