// Package wrapper binds contracts to runtime observability primitives.
//
// A Cell registers an HTTP handler via runtime/auth.Mount or an outbox
// consumer via runtime/eventrouter with a ContractSpec — the spec carries
// the contract id, transport, method/path (or topic). wrapper.HTTPHandler
// contributes HTTP contract attributes to the runtime HTTP middleware's
// single request span; wrapper.WrapConsumer owns the consumer span and
// annotates it with gocell.contract.* plus messaging.destination/system.
// The consumer Tracer is injected by bootstrap, defaulting to NoopTracer.
//
// Layering: package wrapper depends only on stdlib + kernel/ctxkeys +
// kernel/outbox + pkg/ctxkeys. OpenTelemetry lives in runtime/observability
// where an adapter implements wrapper.Tracer.
//
// ref: go-kratos/kratos middleware/tracing/tracing.go — decorator + Options
// ref: open-telemetry/opentelemetry-go-contrib otelhttp/config.go — Filter
// and SpanNameFormatter extensibility
// ref: riandyrn/otelchi middleware.go — chi RouteContext span-name fallback
package wrapper
