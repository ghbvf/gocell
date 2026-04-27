package httputil

import (
	"context"
	"sync"
	"sync/atomic"
)

type clientErrorLogSamplingKey struct{}

type clientErrorLogSampling struct {
	every   uint64
	counter *atomic.Uint64
}

type listErrorLogSampler struct {
	every    uint64
	counters sync.Map
}

var defaultListErrorLogSampler = newListErrorLogSampler(100)

// WithListErrorLogSampling marks list-route client-error logs written through
// this context for deterministic route-keyed one-in-100 sampling.
func WithListErrorLogSampling(ctx context.Context, routeKey string) context.Context {
	return defaultListErrorLogSampler.withContext(ctx, routeKey)
}

func newListErrorLogSampler(every int) *listErrorLogSampler {
	if every < 1 {
		every = 1
	}
	return &listErrorLogSampler{every: uint64(every)}
}

func withClientErrorLogSampling(ctx context.Context, routeKey string, every int) context.Context {
	return newListErrorLogSampler(every).withContext(ctx, routeKey)
}

func (s *listErrorLogSampler) withContext(ctx context.Context, routeKey string) context.Context {
	if s.every <= 1 {
		return context.WithValue(ctx, clientErrorLogSamplingKey{}, clientErrorLogSampling{every: 1})
	}
	return context.WithValue(ctx, clientErrorLogSamplingKey{}, clientErrorLogSampling{
		every:   s.every,
		counter: s.counter(routeKey),
	})
}

func (s *listErrorLogSampler) counter(routeKey string) *atomic.Uint64 {
	actual, _ := s.counters.LoadOrStore(routeKey, &atomic.Uint64{})
	return actual.(*atomic.Uint64)
}

func shouldLogClientError(ctx context.Context) bool {
	cfg, ok := ctx.Value(clientErrorLogSamplingKey{}).(clientErrorLogSampling)
	if !ok || cfg.every <= 1 {
		return true
	}
	return cfg.counter.Add(1)%cfg.every == 0
}
