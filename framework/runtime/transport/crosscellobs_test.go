package transport

import (
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/wrapper"
)

// TestNewCrossCellObs_RetainsBoth asserts the sealed cross-cell observability
// bundle carries the metrics and tracer it was minted with (#2251 P1.3). The
// bundle is the single-source carrier so a split-topology remote call gets the
// SAME tracer bootstrap wires, never a forgotten nil.
func TestNewCrossCellObs_RetainsBoth(t *testing.T) {
	t.Parallel()

	m, _ := newTestMetrics(t)
	rec := &recordingTracer{}

	obs := NewCrossCellObs(m, rec)

	if obs.Metrics() != m {
		t.Errorf("Metrics() = %p, want %p", obs.Metrics(), m)
	}
	if obs.Tracer() != rec {
		t.Errorf("Tracer() = %v, want the recording tracer", obs.Tracer())
	}
}

// TestNewCrossCellObs_NilTracerDegradesToNoop asserts a nil OR typed-nil tracer
// is normalized to wrapper.NoopTracer{} at construction (same contract as
// NewRemoteHTTP), so a minted bundle never carries a tracer that would panic on
// Start. The typed-nil case (a non-nil wrapper.Tracer interface wrapping a nil
// *recordingTracer) is the #2251 review F2 regression: a bare `tracer == nil`
// check misses it and ships the typed-nil into the remote transport, which then
// panics at DoContract's t.tracer.Start. validation.IsNilInterface catches both.
func TestNewCrossCellObs_NilTracerDegradesToNoop(t *testing.T) {
	t.Parallel()

	var typedNil *recordingTracer // typed-nil: non-nil wrapper.Tracer wrapping a nil pointer

	cases := []struct {
		name   string
		tracer wrapper.Tracer
	}{
		{"bare nil", nil},
		{"typed nil", typedNil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			obs := NewCrossCellObs(nil, tc.tracer)

			if obs.Metrics() != nil {
				t.Errorf("Metrics() = %p, want nil (no recording)", obs.Metrics())
			}
			if _, ok := obs.Tracer().(wrapper.NoopTracer); !ok {
				t.Errorf("Tracer() = %T, want wrapper.NoopTracer for a %s input", obs.Tracer(), tc.name)
			}
		})
	}
}

// TestCrossCellObs_ZeroValue documents that a zero-value bundle is a valid
// "no observability" input: nil metrics (no recording) and nil tracer (the
// NewRemoteHTTP / probe consumers degrade nil to NoopTracer). This keeps the
// resolve_test fixtures lightweight without forcing the constructor.
func TestCrossCellObs_ZeroValue(t *testing.T) {
	t.Parallel()

	var obs CrossCellObs
	if obs.Metrics() != nil {
		t.Errorf("zero-value Metrics() = %p, want nil", obs.Metrics())
	}
	if obs.Tracer() != nil {
		t.Errorf("zero-value Tracer() = %v, want nil", obs.Tracer())
	}
}

// TestEndpointDialTarget covers the shared endpoint → host:port parser the
// readiness probe TCP-dials (#2251 P2.7). It is the single source reused by
// rewriteToAbsolute so probe and request rewrite never drift.
func TestEndpointDialTarget(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		endpoint string
		want     string
		wantErr  bool
	}{
		{"bare host:port", "127.0.0.1:9090", "127.0.0.1:9090", false},
		{"http with port", "http://configcore:8080", "configcore:8080", false},
		{"https with port", "https://configcore:8443", "configcore:8443", false},
		{"http no port defaults 80", "http://configcore", "configcore:80", false},
		{"https no port defaults 443", "https://configcore", "configcore:443", false},
		{"empty", "", "", true},
		{"http with path", "http://configcore:8080/x", "", true},
		{"bare with path", "configcore:9090/x", "", true},
		{"bare with query", "configcore:9090?a=b", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := EndpointDialTarget(tc.endpoint)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("EndpointDialTarget(%q): expected error, got %q", tc.endpoint, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("EndpointDialTarget(%q): unexpected error %v", tc.endpoint, err)
			}
			if got != tc.want {
				t.Errorf("EndpointDialTarget(%q) = %q, want %q", tc.endpoint, got, tc.want)
			}
		})
	}
}
