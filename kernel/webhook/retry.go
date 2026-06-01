package webhook

import (
	"errors"
	"net/http"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// RetrySchedule is the fixed sequence of wait intervals between outbound
// delivery retries. The first delivery attempt is immediate; delays[i] is the
// wait before retry i+1. The schedule is value-immutable: DefaultSvixSchedule
// returns a fresh copy and there is no setter.
//
// PR-5 ships DefaultSvixSchedule as the canonical default and the seam for the
// broker-delay follow-up (gh #1458). PR-5 does NOT wire per-attempt delays or a
// per-dispatcher RetryCount at runtime: the dispatch consumer rides the shared
// ConsumerBase (default exponential backoff, capped 30 s). Honoring the full
// Svix timeline (up to ~40 h) needs broker-delay support; DelayFor is the seam
// the follow-up will consume. The schedule is retained because that follow-up is
// a real future need, not a dead abstraction.
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

// Classify maps an outbound delivery outcome to an outbox.Disposition per the
// webhook retry-default ADR (standard-webhooks / Svix aligned). Exactly one of
// the two inputs is meaningful per call: pass the transport error (statusCode
// ignored) when client.Do failed, or statusCode with a nil error when a
// response was received. When transportErr is non-nil the statusCode is
// ignored; pass 0 by convention.
//
// Mapping:
//   - transportErr != nil:
//   - SSRF-blocked (ErrWebhookSSRFBlocked — bad target URL, blocked dial,
//     or denied redirect) → Reject (permanent: a misconfigured/hostile
//     target never succeeds on retry).
//   - otherwise (timeout, connection refused, …) → Requeue (transient).
//   - 2xx → Ack.
//   - 408 Request Timeout / 429 Too Many Requests → Requeue (throttle/timeout
//     are transient; the spec asks senders to back off and retry).
//   - 5xx → Requeue (transient server fault).
//   - 3xx and every other 4xx (400/401/403/404/410 …) → Reject (the request
//     itself is faulty; retrying will not help). 3xx normally never reaches
//     this branch because redirects are denied at the transport layer and
//     surface as an SSRF transport error.
func Classify(statusCode int, transportErr error) outbox.Disposition {
	if transportErr != nil {
		var ee *errcode.Error
		if errors.As(transportErr, &ee) && ee.Code == errcode.ErrWebhookSSRFBlocked {
			return outbox.DispositionReject
		}
		return outbox.DispositionRequeue
	}
	switch {
	case statusCode >= 200 && statusCode < 300:
		return outbox.DispositionAck
	case statusCode == http.StatusRequestTimeout || statusCode == http.StatusTooManyRequests:
		return outbox.DispositionRequeue
	case statusCode >= 500:
		return outbox.DispositionRequeue
	default:
		return outbox.DispositionReject
	}
}
