package wrapper_test

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/kernel/ctxkeys"
	pctxkeys "github.com/ghbvf/gocell/pkg/ctxkeys"

	"github.com/ghbvf/gocell/kernel/wrapper"
)

func TestTraceIDFromContext_ReadsFromPkgCtxkeys(t *testing.T) {
	t.Parallel()
	ctx := pctxkeys.WithTraceID(context.Background(), "trace-123")
	if got := wrapper.TraceIDFromContext(ctx); got != "trace-123" {
		t.Errorf("want trace-123, got %q", got)
	}
}

func TestSpanIDFromContext_ReadsFromPkgCtxkeys(t *testing.T) {
	t.Parallel()
	ctx := pctxkeys.WithSpanID(context.Background(), "span-abc")
	if got := wrapper.SpanIDFromContext(ctx); got != "span-abc" {
		t.Errorf("want span-abc, got %q", got)
	}
}

func TestContractIDFromContext_ReadsFromKernelCtxkeys(t *testing.T) {
	t.Parallel()
	ctx := ctxkeys.WithContractID(context.Background(), "http.auth.login.v1")
	if got := wrapper.ContractIDFromContext(ctx); got != "http.auth.login.v1" {
		t.Errorf("want http.auth.login.v1, got %q", got)
	}
}

func TestContractIDFromContext_EmptyWhenAbsent(t *testing.T) {
	t.Parallel()
	if got := wrapper.ContractIDFromContext(context.Background()); got != "" {
		t.Errorf("want empty string when absent, got %q", got)
	}
}

func TestTraceIDFromContext_EmptyWhenAbsent(t *testing.T) {
	t.Parallel()
	if got := wrapper.TraceIDFromContext(context.Background()); got != "" {
		t.Errorf("want empty string when absent, got %q", got)
	}
}

func TestSpanIDFromContext_EmptyWhenAbsent(t *testing.T) {
	t.Parallel()
	if got := wrapper.SpanIDFromContext(context.Background()); got != "" {
		t.Errorf("want empty string when absent, got %q", got)
	}
}
