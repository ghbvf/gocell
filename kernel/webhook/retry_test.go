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

// TestDefaultSvixSchedule_GoldenSteps locks the canonical Svix retry schedule:
// 7 retries after the immediate first attempt (8 deliveries total).
// ref: svix/svix-webhooks docs.svix.com/retries
func TestDefaultSvixSchedule_GoldenSteps(t *testing.T) {
	t.Parallel()
	s := DefaultSvixSchedule()

	// Reference the production consts (same package): the test locks the count
	// and ordering of DefaultSvixSchedule against the declared svixRetryDelayN
	// values (whose literals are reviewed in retry.go), satisfying
	// TEST-TIME-LITERAL-01 without re-stating duration literals here.
	want := []time.Duration{
		svixRetryDelay1, svixRetryDelay2, svixRetryDelay3, svixRetryDelay4,
		svixRetryDelay5, svixRetryDelay6, svixRetryDelay7,
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
		{"4xx_400", http.StatusBadRequest, nil, outbox.DispositionReject},
		{"4xx_401", http.StatusUnauthorized, nil, outbox.DispositionReject},
		{"4xx_404", http.StatusNotFound, nil, outbox.DispositionReject},
		{"4xx_410_gone", http.StatusGone, nil, outbox.DispositionReject},
		{"3xx_302", http.StatusFound, nil, outbox.DispositionReject},
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
