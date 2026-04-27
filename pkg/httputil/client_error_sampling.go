package httputil

import (
	"context"
	"sync/atomic"
)

type clientErrorLogSamplingKey struct{}

type clientErrorLogSampling struct {
	every uint64
}

var clientErrorLogSampleCounter atomic.Uint64

// WithClientErrorLogSampling marks client-error logs written through this
// context for deterministic one-in-N sampling. Values <= 1 preserve the
// default behavior and log every 4xx.
func WithClientErrorLogSampling(ctx context.Context, every int) context.Context {
	if every <= 1 {
		return context.WithValue(ctx, clientErrorLogSamplingKey{}, clientErrorLogSampling{every: 1})
	}
	return context.WithValue(ctx, clientErrorLogSamplingKey{}, clientErrorLogSampling{every: uint64(every)})
}

func shouldLogClientError(ctx context.Context) bool {
	cfg, ok := ctx.Value(clientErrorLogSamplingKey{}).(clientErrorLogSampling)
	if !ok || cfg.every <= 1 {
		return true
	}
	return clientErrorLogSampleCounter.Add(1)%cfg.every == 0
}
