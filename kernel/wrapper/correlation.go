package wrapper

import (
	"context"

	"github.com/ghbvf/gocell/kernel/ctxkeys"
	pctxkeys "github.com/ghbvf/gocell/pkg/ctxkeys"
)

// TraceIDFromContext returns the trace identifier carried on ctx, or "" if
// absent. It is a thin bridge over pkg/ctxkeys.TraceIDFrom so callers can
// depend on wrapper without pulling pkg/ctxkeys.
func TraceIDFromContext(ctx context.Context) string {
	if v, ok := pctxkeys.TraceIDFrom(ctx); ok {
		return v
	}
	return ""
}

// SpanIDFromContext returns the span identifier carried on ctx, or "" if
// absent.
func SpanIDFromContext(ctx context.Context) string {
	if v, ok := pctxkeys.SpanIDFrom(ctx); ok {
		return v
	}
	return ""
}

// ContractIDFromContext returns the contract identifier carried on ctx.
// It is populated by HTTPHandler / WrapConsumer at request / event entry.
func ContractIDFromContext(ctx context.Context) string {
	if v, ok := ctxkeys.ContractIDFrom(ctx); ok {
		return v
	}
	return ""
}
