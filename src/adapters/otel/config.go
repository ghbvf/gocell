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
	// Default: 1.0 (sample everything).
	SampleRate float64
}

// defaults fills zero-valued fields with sensible defaults.
func (c *TracerConfig) defaults() {
	if c.SampleRate <= 0 {
		c.SampleRate = 1.0
	}
	if c.SampleRate > 1.0 {
		c.SampleRate = 1.0
	}
}
