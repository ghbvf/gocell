package errcode

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// timeoutErr is a net.Error whose Timeout() reports true — the modern
// replacement for the deprecated net.Error.Temporary() (golang/go #45729).
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return false }

var _ net.Error = timeoutErr{}

// retryableErr implements the anonymous interface{ RetryableError() bool }
// adapter contract (pgconn.SafeToRetry / aws-sdk-go-v2 RetryableError idiom).
type retryableErr struct{ retry bool }

func (e retryableErr) Error() string        { return "adapter op" }
func (e retryableErr) RetryableError() bool { return e.retry }

func TestWrapInfra_SetsMarkerAndKind(t *testing.T) {
	cause := errors.New("conn reset")
	err := WrapInfra(Code("ERR_ADAPTER_PG_QUERY"), "pg query failed", cause)

	var ec *Error
	if !errors.As(err, &ec) {
		t.Fatalf("WrapInfra must produce *errcode.Error, got %T", err)
	}
	assert.Equal(t, KindUnavailable, ec.Kind, "WrapInfra → KindUnavailable (HTTP 503)")
	assert.Equal(t, CategoryInfra, ec.Category, "WrapInfra → CategoryInfra")
	assert.Equal(t, Code("ERR_ADAPTER_PG_QUERY"), ec.Code, "WrapInfra reuses caller operation code")
	assert.ErrorIs(t, err, cause, "WrapInfra preserves the cause chain")
	assert.True(t, IsTransient(err), "WrapInfra output is transient")
}

func TestIsTransient_MarkerKeyed(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"WrapInfra direct", WrapInfra(Code("ERR_ADAPTER_REDIS_GET"), "redis get failed", errors.New("x")), true},
		{"WrapInfra wrapped", fmt.Errorf("svc: %w", WrapInfra(Code("ERR_ADAPTER_S3_UPLOAD"), "s3 upload failed", errors.New("x"))), true},
		// Downstream Hard: the transient code constructed via New (no WrapInfra
		// marker) must NOT be transient — "looks transient but didn't pass
		// WrapInfra" is not recognized.
		{"code via New only", New(KindUnavailable, ErrKeyProviderTransient, "sealed"), false},
		{"plain permanent errcode", New(KindInternal, ErrKeyProviderEncryptFailed, "encrypt failed"), false},
		// Raw low-level recognizers (un-wrapped stdlib / SDK errors).
		{"context.DeadlineExceeded", context.DeadlineExceeded, true},
		{"wrapped DeadlineExceeded", fmt.Errorf("dial: %w", context.DeadlineExceeded), true},
		{"context.Canceled NOT transient", context.Canceled, false},
		{"net.Error Timeout()", timeoutErr{}, true},
		{"wrapped net timeout", fmt.Errorf("redis: %w", timeoutErr{}), true},
		{"RetryableError()=true", retryableErr{retry: true}, true},
		{"RetryableError()=false", retryableErr{retry: false}, false},
		{"plain error", errors.New("boom"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsTransient(tc.err))
		})
	}
}

// Disposition routing contract: transient → Requeue, permanent → Reject.
func TestIsTransient_DispositionContract(t *testing.T) {
	transient := WrapInfra(Code("ERR_ADAPTER_PG_QUERY"), "serialization failure", errors.New("40001"))
	permanent := New(KindInternal, Code("ERR_ADAPTER_PG_QUERY"), "schema drift")

	assert.True(t, IsTransient(transient), "serialization failure requeues")
	assert.False(t, IsTransient(permanent), "schema drift → Reject → DLX")
}

func TestWrapInfra_NilCauseTolerated(t *testing.T) {
	err := WrapInfra(Code("ERR_ADAPTER_REDIS_SET"), "redis set failed", nil)
	assert.True(t, IsTransient(err))
	var ec *Error
	assert.True(t, errors.As(err, &ec))
	assert.Nil(t, ec.Cause)
}

// TestIsTransient_TwoTierAuthority locks the two-tier semantic (F2/F3):
// a classified *errcode.Error is authoritative (outermost wins, re-wrap =
// re-classify); raw recognizers apply ONLY when the chain has no *Error.
func TestIsTransient_TwoTierAuthority(t *testing.T) {
	inner := WrapInfra(Code("ERR_ADAPTER_PG_QUERY"), "serialization failure",
		errors.New("40001"))

	// F2: an outer permanent errcode.Wrap re-classifies — the inner WrapInfra
	// marker must NOT leak through (re-classification wins).
	reclassified := Wrap(KindInternal, Code("ERR_CONFIG_DECRYPT_FAILED"),
		"config decrypt failed", inner)
	assert.False(t, IsTransient(reclassified),
		"outer permanent Wrap re-classifies; inner WrapInfra marker must not leak")

	// fmt.Errorf wrapping is transparent: WrapInfra stays the outermost (only)
	// *Error, so the marker is still authoritative → transient.
	assert.True(t, IsTransient(fmt.Errorf("svc: %w", inner)),
		"fmt.Errorf wrap is transparent; WrapInfra marker still authoritative")

	// F3: a classified-permanent *Error whose cause is a raw transient-looking
	// error (ctx.DeadlineExceeded) stays permanent — tier-2 raw recognizers
	// are NOT consulted once the error is classified.
	permWrapsDeadline := Wrap(KindInternal, Code("ERR_ADAPTER_PG_QUERY"),
		"schema drift", context.DeadlineExceeded)
	assert.False(t, IsTransient(permWrapsDeadline),
		"classified-permanent must not be overridden by an inner ctx.DeadlineExceeded")

	// Tier 2 still works for genuinely un-classified raw errors.
	assert.True(t, IsTransient(context.DeadlineExceeded),
		"bare ctx.DeadlineExceeded (no *Error) → tier-2 transient")
	assert.True(t, IsTransient(fmt.Errorf("dial: %w", context.DeadlineExceeded)),
		"fmt-wrapped bare ctx.DeadlineExceeded (no *Error) → tier-2 transient")
}

var _ = time.Second // keep time import stable for future deadline cases
