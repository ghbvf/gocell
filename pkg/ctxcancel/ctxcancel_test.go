package ctxcancel

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetect(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil is not ctx cancel", err: nil, want: false},
		{name: "context.Canceled", err: context.Canceled, want: true},
		{name: "context.DeadlineExceeded", err: context.DeadlineExceeded, want: true},
		{name: "wrapped context.Canceled", err: fmt.Errorf("scan: %w", context.Canceled), want: true},
		{name: "wrapped DeadlineExceeded", err: fmt.Errorf("query: %w", context.DeadlineExceeded), want: true},
		{name: "joined chain with cancel", err: errors.Join(errors.New("outer"), context.Canceled), want: true},
		{name: "unrelated error", err: errors.New("connection refused"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Detect(tt.err))
		})
	}
}

func TestWrap_NilWhenNotCancel(t *testing.T) {
	assert.Nil(t, Wrap(nil, "Insert", "key=k"))
	assert.Nil(t, Wrap(errors.New("bad conn"), "Insert", "key=k"))
}

// TestWrap_CanceledReturnsClientCanceled locks the 499 (client-direction)
// branch: real client disconnect → ErrClientCanceled → log4xx + slog.Warn.
func TestWrap_CanceledReturnsClientCanceled(t *testing.T) {
	got := Wrap(context.Canceled, "Insert", "key=foo")
	require.NotNil(t, got)
	assert.Equal(t, errcode.ErrClientCanceled, got.Code)
	assert.Equal(t, "request canceled", got.Message)
	assert.Equal(t, errcode.CategoryInfra, got.Category)
	assert.Contains(t, got.InternalMessage, "Insert")
	assert.Contains(t, got.InternalMessage, "key=foo")
	assert.Contains(t, got.InternalMessage, "ctx canceled")
	assert.ErrorIs(t, got, context.Canceled,
		"Cause must be preserved so errors.Is(err, context.Canceled) works upstream")
}

// TestWrap_DeadlineReturnsServerTimeout locks the 504 (server-direction)
// branch: server-side / inherited timeout → ErrServerTimeout → log5xx +
// slog.Error + 5xx alerting. PR275 P2-3: split aligns with NGINX (499 vs
// 504) and Kratos transport/http/status (Canceled→499, DeadlineExceeded→504).
func TestWrap_DeadlineReturnsServerTimeout(t *testing.T) {
	got := Wrap(context.DeadlineExceeded, "ScanRow", "configID=cfg-1")
	require.NotNil(t, got)
	assert.Equal(t, errcode.ErrServerTimeout, got.Code,
		"context.DeadlineExceeded must surface as ErrServerTimeout (504), "+
			"not ErrClientCanceled (499) — server-direction timeouts feed 5xx alerts")
	assert.Equal(t, "request timed out", got.Message)
	assert.Equal(t, errcode.CategoryInfra, got.Category)
	assert.ErrorIs(t, got, context.DeadlineExceeded,
		"Cause must be preserved so errors.Is(err, context.DeadlineExceeded) works upstream")
}

// TestWrap_IsInfraError_Preserved guards the category invariant for both
// branches: ErrClientCanceled and ErrServerTimeout remain CategoryInfra so
// health.Checker timeout-bucket behaviour is unchanged. HTTP status mapping
// (499 vs 504) is driven by codeToStatus, not by IsInfraError.
func TestWrap_IsInfraError_Preserved(t *testing.T) {
	canceled := Wrap(context.Canceled, "Insert", "key=k")
	require.NotNil(t, canceled)
	assert.True(t, errcode.IsInfraError(canceled),
		"client cancel must remain IsInfraError=true (preserves health/timeout bucket)")
	assert.True(t, errcode.IsExpected4xx(canceled),
		"client cancel must be IsExpected4xx=true (routes to slog.Warn)")

	deadline := Wrap(context.DeadlineExceeded, "ScanRow", "configID=cfg-1")
	require.NotNil(t, deadline)
	assert.True(t, errcode.IsInfraError(deadline),
		"server timeout must remain IsInfraError=true (preserves health/timeout bucket)")
	assert.False(t, errcode.IsExpected4xx(deadline),
		"server timeout must NOT be IsExpected4xx — 504 is 5xx, routes to slog.Error")
}

// TestWrap_ReasonInDetails locks the PR271-FU1 contract: the wrapped *errcode.Error
// carries Details["reason"] mirroring the originating ctx error variant.
// After the PR275 P2-3 split the primary signal is the HTTP status (499 vs
// 504), but the reason field still provides a low-cardinality dimension for
// dashboards that bucket by both status and reason (e.g. ratio of canceled
// 499 to deadline-rooted 504, useful when investigating regressions).
//
// ref: Kratos transport/http/status — Canceled→499, DeadlineExceeded→504
// TestReasonFromDetails locks the fail-closed enum whitelist at the read
// site (PR275 P2-1): only ReasonCanceled and ReasonDeadlineExceeded round-
// trip; everything else (nil map, missing key, wrong type, arbitrary
// string, case mismatch) yields "" so callers fall back to their own
// "unknown" placeholder rather than propagating un-vetted values into
// span attributes / log fields.
func TestReasonFromDetails(t *testing.T) {
	tests := []struct {
		name string
		d    map[string]any
		want string
	}{
		{name: "nil map → empty", d: nil, want: ""},
		{name: "missing key → empty", d: map[string]any{}, want: ""},
		{name: "non-string value → empty", d: map[string]any{DetailsKeyReason: 42}, want: ""},
		{name: "empty-string value → empty", d: map[string]any{DetailsKeyReason: ""}, want: ""},
		{name: "ReasonCanceled accepted", d: map[string]any{DetailsKeyReason: ReasonCanceled}, want: ReasonCanceled},
		{name: "ReasonDeadlineExceeded accepted", d: map[string]any{DetailsKeyReason: ReasonDeadlineExceeded}, want: ReasonDeadlineExceeded},
		{name: "arbitrary string rejected", d: map[string]any{DetailsKeyReason: "future-enum-value"}, want: ""},
		{name: "user-derived string rejected", d: map[string]any{DetailsKeyReason: "key=admin"}, want: ""},
		{name: "case mismatch rejected", d: map[string]any{DetailsKeyReason: "Canceled"}, want: ""},
		{name: "extra unrelated keys ignored", d: map[string]any{"other": "x", DetailsKeyReason: ReasonCanceled}, want: ReasonCanceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ReasonFromDetails(tt.d))
		})
	}
}

func TestWrap_ReasonInDetails(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantReason string
	}{
		{name: "context.Canceled → reason=canceled", err: context.Canceled, wantReason: "canceled"},
		{name: "context.DeadlineExceeded → reason=deadline_exceeded", err: context.DeadlineExceeded, wantReason: "deadline_exceeded"},
		{name: "wrapped Canceled → reason=canceled", err: fmt.Errorf("scan: %w", context.Canceled), wantReason: "canceled"},
		{name: "wrapped DeadlineExceeded → reason=deadline_exceeded", err: fmt.Errorf("query: %w", context.DeadlineExceeded), wantReason: "deadline_exceeded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Wrap(tt.err, "Op", "id=x")
			require.NotNil(t, got)
			require.NotNil(t, got.Details, "Details must be set so tracing middleware can read reason")
			reason, ok := got.Details["reason"].(string)
			require.True(t, ok, "Details[\"reason\"] must be a string")
			assert.Equal(t, tt.wantReason, reason)
		})
	}
}
