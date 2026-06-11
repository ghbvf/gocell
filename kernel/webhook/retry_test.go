package webhook

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// goldenSvixDelayN is an INDEPENDENT golden copy of the Svix retry schedule,
// declared separately from retry.go's svixRetryDelayN. Referencing the
// production consts (the prior form) made the golden vacuous: editing a
// production delay would change both sides and the test would still pass. With
// an independent copy, any drift of a production delay constant breaks
// TestDefaultSvixSchedule_GoldenSteps. TEST-TIME-LITERAL-01: durations live in a
// package-level const initializer.
const (
	goldenSvixDelay1 = 5 * time.Second
	goldenSvixDelay2 = 5 * time.Minute
	goldenSvixDelay3 = 30 * time.Minute
	goldenSvixDelay4 = 2 * time.Hour
	goldenSvixDelay5 = 5 * time.Hour
	goldenSvixDelay6 = 10 * time.Hour
	goldenSvixDelay7 = 10 * time.Hour
)

// TestDefaultSvixSchedule_GoldenSteps locks the canonical Svix retry schedule:
// 7 retries after the immediate first attempt (8 deliveries total).
// ref: svix/svix-webhooks docs.svix.com/retries
func TestDefaultSvixSchedule_GoldenSteps(t *testing.T) {
	t.Parallel()
	s := DefaultSvixSchedule()

	// Lock DefaultSvixSchedule against the INDEPENDENT golden copy (goldenSvixDelayN,
	// declared at the top of this file) — not the production svixRetryDelayN — so
	// editing a production delay constant fails this golden instead of passing it.
	want := []time.Duration{
		goldenSvixDelay1, goldenSvixDelay2, goldenSvixDelay3, goldenSvixDelay4,
		goldenSvixDelay5, goldenSvixDelay6, goldenSvixDelay7,
	}
	assert.Equal(t, len(want), s.MaxRetries(), "MaxRetries")
	assert.Equal(t, len(want)+1, s.Attempts(), "Attempts (1 immediate + retries)")

	for i, w := range want {
		d, ok := s.DelayFor(i + 1)
		require.Truef(t, ok, "DelayFor(%d) ok", i+1)
		assert.Equalf(t, w, d, "DelayFor(%d)", i+1)
	}
}

func TestRetrySchedule_DelayFor_OutOfRange(t *testing.T) {
	t.Parallel()
	s := DefaultSvixSchedule()
	cases := []int{-1, 0, s.MaxRetries() + 1, 100}
	for _, n := range cases {
		_, ok := s.DelayFor(n)
		assert.Falsef(t, ok, "DelayFor(%d) should be exhausted", n)
	}
}

// TestRetrySchedule_Delays verifies the bulk-read accessor consumed by the
// #1458 broker-delay wiring returns the full tier list in retry order and a
// defensive copy (mutating the result must not affect the schedule).
func TestRetrySchedule_Delays(t *testing.T) {
	t.Parallel()
	s := DefaultSvixSchedule()
	want := []time.Duration{
		goldenSvixDelay1, goldenSvixDelay2, goldenSvixDelay3, goldenSvixDelay4,
		goldenSvixDelay5, goldenSvixDelay6, goldenSvixDelay7,
	}
	got := s.Delays()
	assert.Equal(t, want, got, "Delays() must return the canonical tiers in retry order")

	// Mutating the returned slice must not corrupt the schedule's internal state.
	got[0] = time.Nanosecond
	again := s.Delays()
	assert.Equal(t, want, again, "Delays() must return a defensive copy")
}

func TestClassify(t *testing.T) {
	t.Parallel()
	ssrf := errcode.New(errcode.KindPermissionDenied, errcode.ErrWebhookSSRFBlocked, "blocked")
	// Wrapped to mimic http.Client wrapping a dial error in *url.Error.
	wrappedSSRF := errcode.Wrap(errcode.KindUnavailable, errcode.ErrWebhookDeliveryFailed,
		"transport", ssrf)
	_ = wrappedSSRF // see dedicated case below

	tests := []struct {
		name       string
		statusCode int
		err        error
		want       outbox.Disposition
	}{
		{"2xx_200", http.StatusOK, nil, outbox.DispositionAck},
		{"2xx_202", http.StatusAccepted, nil, outbox.DispositionAck},
		{"2xx_299", 299, nil, outbox.DispositionAck},
		{"5xx_500", http.StatusInternalServerError, nil, outbox.DispositionRequeue},
		{"5xx_503", http.StatusServiceUnavailable, nil, outbox.DispositionRequeue},
		{"408_timeout", http.StatusRequestTimeout, nil, outbox.DispositionRequeue},
		{"429_throttle", http.StatusTooManyRequests, nil, outbox.DispositionRequeue},
		// standard-webhooks / Svix aligned: every non-2xx is a retryable delivery
		// failure (the receiver's 4xx/3xx may be transient — deploy gap, secret
		// rotation, throttle — and the sender cannot tell permanent from transient).
		{"4xx_400_requeue", http.StatusBadRequest, nil, outbox.DispositionRequeue},
		{"4xx_401_requeue", http.StatusUnauthorized, nil, outbox.DispositionRequeue},
		{"4xx_404_requeue", http.StatusNotFound, nil, outbox.DispositionRequeue},
		{"4xx_410_gone_requeue", http.StatusGone, nil, outbox.DispositionRequeue},
		{"3xx_302_requeue", http.StatusFound, nil, outbox.DispositionRequeue},
		{"transport_ssrf_reject", 0, ssrf, outbox.DispositionReject},
		{"transport_ssrf_wrapped_reject", 0, errors.Join(errors.New("Get x:"), ssrf), outbox.DispositionReject},
		{"transport_generic_requeue", 0, errors.New("connection refused"), outbox.DispositionRequeue},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, Classify(tc.statusCode, tc.err))
		})
	}
}
