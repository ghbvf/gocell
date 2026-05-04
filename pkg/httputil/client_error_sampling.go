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

type clientErrorLogSampler struct {
	every    uint64
	counters sync.Map
}

const fallbackClientErrorRouteKey = "__missing_client_error_sampling_context__"

var defaultClientErrorLogSampler = newClientErrorLogSampler(100)

// WithClientErrorLogSampling installs deterministic route-keyed sampling for
// client-error logs produced through WriteError.
func WithClientErrorLogSampling(ctx context.Context, routeKey string) context.Context {
	return defaultClientErrorLogSampler.withContext(ctx, routeKey)
}

func withClientErrorLogSamplingEvery(ctx context.Context, routeKey string, every int) context.Context {
	return newClientErrorLogSampler(every).withContext(ctx, routeKey)
}

func newClientErrorLogSampler(every int) *clientErrorLogSampler {
	if every < 1 {
		every = 1
	}
	return &clientErrorLogSampler{every: uint64(every)}
}

func (s *clientErrorLogSampler) withContext(ctx context.Context, routeKey string) context.Context {
	if routeKey == "" {
		routeKey = fallbackClientErrorRouteKey
	}
	if s.every <= 1 {
		return context.WithValue(ctx, clientErrorLogSamplingKey{}, clientErrorLogSampling{every: 1})
	}
	return context.WithValue(ctx, clientErrorLogSamplingKey{}, clientErrorLogSampling{
		every:   s.every,
		counter: s.counter(routeKey),
	})
}

func (s *clientErrorLogSampler) counter(routeKey string) *atomic.Uint64 {
	actual, _ := s.counters.LoadOrStore(routeKey, &atomic.Uint64{})
	return actual.(*atomic.Uint64)
}

func shouldLogClientError(ctx context.Context) bool {
	cfg, ok := ctx.Value(clientErrorLogSamplingKey{}).(clientErrorLogSampling)
	if !ok {
		cfg = clientErrorLogSampling{
			every:   defaultClientErrorLogSampler.every,
			counter: defaultClientErrorLogSampler.counter(fallbackClientErrorRouteKey),
		}
	}
	if cfg.every <= 1 {
		return true
	}
	return cfg.counter.Add(1)%cfg.every == 0
}
