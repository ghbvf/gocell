package otel

// TracerConfig holds settings for the OTel tracer adapter.
type TracerConfig struct {
	// ServiceName identifies this service in traces (e.g. "access-core").
	ServiceName string

	// ExporterEndpoint is the OTLP gRPC collector address (e.g. "localhost:4317").
	ExporterEndpoint string

	// Insecure disables TLS for the gRPC connection to the collector.
	Insecure bool

	// SampleRate is the probability of sampling a trace (0.0-1.0).
	// Use -1 (or leave at zero) for the default of 1.0 (sample everything).
	// Use 0.0 explicitly via DisableSampling to drop all traces.
	SampleRate float64

	// DisableSampling forces SampleRate to 0, dropping all traces.
	// This distinguishes "not configured" (zero value → default 1.0)
	// from "explicitly disabled" (DisableSampling=true → 0.0).
	DisableSampling bool
}

// defaults fills zero-valued fields with sensible defaults.
func (c *TracerConfig) defaults() {
	if c.DisableSampling {
		c.SampleRate = 0
		return
	}
	if c.SampleRate <= 0 {
		c.SampleRate = 1.0
	}
	if c.SampleRate > 1.0 {
		c.SampleRate = 1.0
	}
}
