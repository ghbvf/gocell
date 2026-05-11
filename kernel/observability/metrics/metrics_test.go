package metrics_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

func TestValidateLabels(t *testing.T) {
	tests := []struct {
		name         string
		expected     []string
		got          metrics.Labels
		wantErr      bool
		wantSentinel error // non-nil expected sentinel to assert errors.Is against
	}{
		{
			name:     "exact match",
			expected: []string{"cell_id", "outcome"},
			got:      metrics.Labels{"cell_id": "accesscore", "outcome": "success"},
			wantErr:  false,
		},
		{
			name:     "missing key",
			expected: []string{"cell_id", "outcome"},
			got:      metrics.Labels{"cell_id": "accesscore"},
			wantErr:  true,
		},
		{
			name:     "extra key",
			expected: []string{"cell_id"},
			got:      metrics.Labels{"cell_id": "accesscore", "stray": "x"},
			wantErr:  true,
		},
		{
			name:     "wrong key",
			expected: []string{"cell_id"},
			got:      metrics.Labels{"cellid": "accesscore"},
			wantErr:  true,
		},
		{
			name:     "both empty",
			expected: nil,
			got:      nil,
			wantErr:  false,
		},
		{
			name:     "expected empty, got populated",
			expected: nil,
			got:      metrics.Labels{"cell_id": "x"},
			wantErr:  true,
		},
		{
			name:         "value with pipe separator rejected",
			expected:     []string{"pool"},
			got:          metrics.Labels{"pool": "pg|main"},
			wantErr:      true,
			wantSentinel: metrics.ErrLabelValueIllegal,
		},
		{
			name:         "value with equals separator rejected",
			expected:     []string{"pool"},
			got:          metrics.Labels{"pool": "foo=bar"},
			wantErr:      true,
			wantSentinel: metrics.ErrLabelValueIllegal,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := metrics.ValidateLabels(tc.expected, tc.got)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateLabels wantErr=%v got=%v", tc.wantErr, err)
			}
			if err == nil {
				return
			}
			sentinel := tc.wantSentinel
			if sentinel == nil {
				sentinel = metrics.ErrLabelMismatch
			}
			if !errors.Is(err, sentinel) {
				t.Fatalf("error must wrap %v, got %v", sentinel, err)
			}
		})
	}
}

func TestNopProvider_CounterAndHistogram(t *testing.T) {
	p := metrics.NopProvider{}

	cv, err := p.CounterVec(metrics.CounterOpts{
		Name:       "nop_counter_total",
		Help:       "nop counter",
		LabelNames: []string{"phase"},
	})
	if err != nil {
		t.Fatalf("CounterVec: %v", err)
	}
	c := cv.With(metrics.Labels{"phase": "start"})
	c.Inc()
	c.Add(2.5) // must not panic

	hv, err := p.HistogramVec(metrics.HistogramOpts{
		Name:       "nop_hist_seconds",
		Help:       "nop histogram",
		LabelNames: []string{"phase"},
		Buckets:    []float64{0.1, 1, 10},
	})
	if err != nil {
		t.Fatalf("HistogramVec: %v", err)
	}
	h := hv.With(metrics.Labels{"phase": "stop"})
	h.Observe(0.5)
}

func TestNopProvider_PanicsOnLabelMismatch(t *testing.T) {
	p := metrics.NopProvider{}
	cv, err := p.CounterVec(metrics.CounterOpts{
		Name:       "nop_counter",
		LabelNames: []string{"a", "b"},
	})
	if err != nil {
		t.Fatalf("CounterVec: %v", err)
	}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on label mismatch, got nothing")
		}
		// A/B class panic wraps errcode.Assertion → *errcode.Error with ErrInternal.
		// The message embeds the original ValidateLabels description.
		var ec *errcode.Error
		if !errors.As(r.(error), &ec) {
			t.Fatalf("panic must be *errcode.Error, got %T: %v", r, r)
		}
		if !strings.Contains(ec.Message, "metrics: invalid labels") {
			t.Fatalf("panic message must contain 'metrics: invalid labels', got %q", ec.Message)
		}
	}()
	cv.With(metrics.Labels{"a": "x"}) // missing "b"
}

func TestMustValidateLabels(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic")
		}
	}()
	metrics.MustValidateLabels([]string{"x"}, metrics.Labels{"y": "1"})
}

func TestNopProvider_AcceptsEmptyLabels(t *testing.T) {
	p := metrics.NopProvider{}
	cv, err := p.CounterVec(metrics.CounterOpts{Name: "no_labels", Help: "h"})
	if err != nil {
		t.Fatalf("CounterVec: %v", err)
	}
	cv.With(nil).Inc()               // nil labels OK
	cv.With(metrics.Labels{}).Add(1) // empty map OK
}

// TestNopProvider_Unregister_IdempotentReturnsNil verifies that
// NopProvider.Unregister returns nil for any Collector (including nil-like
// values) and is safe to call multiple times (idempotent contract).
func TestNopProvider_Unregister_IdempotentReturnsNil(t *testing.T) {
	p := metrics.NopProvider{}

	cv, err := p.CounterVec(metrics.CounterOpts{Name: "unreg_counter", Help: "h", LabelNames: []string{"x"}})
	if err != nil {
		t.Fatalf("CounterVec: %v", err)
	}
	hv, err := p.HistogramVec(metrics.HistogramOpts{Name: "unreg_hist", Help: "h", LabelNames: []string{"x"}})
	if err != nil {
		t.Fatalf("HistogramVec: %v", err)
	}

	for _, c := range []metrics.Collector{cv, hv} {
		if err := p.Unregister(c); err != nil {
			t.Fatalf("first Unregister: want nil, got %v", err)
		}
		// Idempotent: second call must also return nil.
		if err := p.Unregister(c); err != nil {
			t.Fatalf("second Unregister (idempotent): want nil, got %v", err)
		}
	}
}

// TestNopCounterVec_Registered_ReturnsTrue verifies that nopCounterVec.Registered
// is the compile-time membership marker that always returns true.
func TestNopCounterVec_Registered_ReturnsTrue(t *testing.T) {
	p := metrics.NopProvider{}
	cv, err := p.CounterVec(metrics.CounterOpts{Name: "reg_counter", Help: "h"})
	if err != nil {
		t.Fatalf("CounterVec: %v", err)
	}
	if !cv.Registered() {
		t.Fatal("nopCounterVec.Registered() must return true")
	}
}

// TestNopHistogramVec_Registered_ReturnsTrue verifies that nopHistogramVec.Registered
// is the compile-time membership marker that always returns true.
func TestNopHistogramVec_Registered_ReturnsTrue(t *testing.T) {
	p := metrics.NopProvider{}
	hv, err := p.HistogramVec(metrics.HistogramOpts{Name: "reg_hist", Help: "h"})
	if err != nil {
		t.Fatalf("HistogramVec: %v", err)
	}
	if !hv.Registered() {
		t.Fatal("nopHistogramVec.Registered() must return true")
	}
}
