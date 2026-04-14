package otel

import "fmt"

// TracerConfig holds settings for the OTel tracer adapter.
type TracerConfig struct {
	// ServiceName identifies this service in traces (e.g. "access-core").
	ServiceName string

	// ExporterEndpoint is the OTLP gRPC collector address (e.g. "localhost:4317").
	ExporterEndpoint string

	// Insecure disables TLS for the gRPC connection to the collector.
	Insecure bool

	// SampleRate is the probability of sampling a trace, in the range (0, 1].
	// Zero value means "use default" (1.0 = sample everything).
	// To disable sampling entirely, set DisableSampling=true.
	// Values outside (0, 1] (when non-zero) cause NewTracer to return an error.
	SampleRate float64

	// DisableSampling forces SampleRate to 0, dropping all traces.
	// Takes precedence over SampleRate.
	DisableSampling bool
}

// validate checks SampleRate is within the allowed range.
// Called from NewTracer before defaults().
func (c *TracerConfig) validate() error {
	if c.DisableSampling {
		return nil
	}
	if c.SampleRate == 0 {
		return nil // zero value = use default
	}
	if c.SampleRate < 0 || c.SampleRate > 1.0 {
		return fmt.Errorf("otel: SampleRate must be in (0, 1], got %g", c.SampleRate)
	}
	return nil
}

// defaults fills zero-valued fields with sensible defaults.
// Must be called after validate().
func (c *TracerConfig) defaults() {
	if c.DisableSampling {
		c.SampleRate = 0
		return
	}
	if c.SampleRate == 0 {
		c.SampleRate = 1.0
	}
}
