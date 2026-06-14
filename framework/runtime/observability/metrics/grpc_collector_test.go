package metrics

import (
	"context"
	"errors"
	"testing"

	kernelmetrics "github.com/ghbvf/gocell/framework/kernel/observability/metrics"
)

// counterErrProvider succeeds on everything except CounterVec.
type counterErrProvider struct{ kernelmetrics.NopProvider }

func (counterErrProvider) CounterVec(kernelmetrics.CounterOpts) (kernelmetrics.CounterVec, error) {
	return nil, errors.New("counter register boom")
}

// histErrProvider succeeds on CounterVec but fails on HistogramVec.
type histErrProvider struct{ kernelmetrics.NopProvider }

func (histErrProvider) HistogramVec(kernelmetrics.HistogramOpts) (kernelmetrics.HistogramVec, error) {
	return nil, errors.New("histogram register boom")
}

func TestInMemoryGRPCCollector(t *testing.T) {
	c := NewInMemoryGRPCCollector()
	c.RecordRPC(context.Background(), testLabel(""), "/pkg.Svc/Do", "OK", 0.01)
	c.RecordRPC(context.Background(), testLabel(""), "/pkg.Svc/Do", "OK", 0.02)
	c.RecordRPC(context.Background(), testLabel(""), "/pkg.Svc/Do", "Internal", 0.03)

	if got := c.Count("_runtime", "/pkg.Svc/Do", "OK"); got != 2 {
		t.Fatalf("OK count = %d, want 2", got)
	}
	if got := c.Count("_runtime", "/pkg.Svc/Do", "Internal"); got != 1 {
		t.Fatalf("Internal count = %d, want 1", got)
	}
	if got := c.Count("_runtime", "/pkg.Svc/Do", "NotFound"); got != 0 {
		t.Fatalf("absent count = %d, want 0", got)
	}
}

func TestNewGRPCProviderCollector(t *testing.T) {
	t.Run("nil provider rejected", func(t *testing.T) {
		if _, err := NewGRPCProviderCollector(nil, ProviderCollectorConfig{}); err == nil {
			t.Fatalf("expected error for nil provider")
		}
	})

	t.Run("registers and records without panic", func(t *testing.T) {
		p := kernelmetrics.NopProvider{}
		c, err := NewGRPCProviderCollector(p, ProviderCollectorConfig{})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		c.RecordRPC(context.Background(), testLabel(""), "/pkg.Svc/Do", "OK", 0.01)
	})

	t.Run("counter registration error surfaces", func(t *testing.T) {
		if _, err := NewGRPCProviderCollector(counterErrProvider{}, ProviderCollectorConfig{}); err == nil {
			t.Fatalf("expected counter registration error")
		}
	})

	t.Run("histogram registration error surfaces", func(t *testing.T) {
		if _, err := NewGRPCProviderCollector(histErrProvider{}, ProviderCollectorConfig{}); err == nil {
			t.Fatalf("expected histogram registration error")
		}
	})
}
