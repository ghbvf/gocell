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

func TestWrap_ReturnsClientCanceled(t *testing.T) {
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

func TestWrap_PreservesDeadlineExceeded(t *testing.T) {
	got := Wrap(context.DeadlineExceeded, "ScanRow", "configID=cfg-1")
	require.NotNil(t, got)
	assert.Equal(t, errcode.ErrClientCanceled, got.Code)
	assert.ErrorIs(t, got, context.DeadlineExceeded)
}

// TestWrap_IsInfraError_Preserved guards the explicit decision in PR-A50+A51:
// ErrClientCanceled keeps IsInfraError == true so health.Checker timeout-bucket
// behaviour is unchanged. The HTTP layer routes 499 via codeToStatus mapping,
// not via IsInfraError. See plan §风险 #2.
func TestWrap_IsInfraError_Preserved(t *testing.T) {
	got := Wrap(context.Canceled, "Insert", "key=k")
	require.NotNil(t, got)
	assert.True(t, errcode.IsInfraError(got),
		"ctx cancel must remain IsInfraError=true (preserves health/timeout bucket)")
	assert.True(t, errcode.IsExpected4xx(got),
		"ctx cancel must also be IsExpected4xx=true (routes to slog.Warn at HTTP boundary)")
}

// TestWrap_ReasonInDetails locks the PR271-FU1 contract: the wrapped *errcode.Error
// must carry Details["reason"] distinguishing context.Canceled (real client
// disconnect) from context.DeadlineExceeded (server-side / inherited timeout)
// so dashboards can split 499 by source instead of seeing one opaque bucket.
//
// ref: Kratos transport/http/status — Canceled→499, DeadlineExceeded→504
//
//	(we keep both at 499 but expose reason for triage).
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
