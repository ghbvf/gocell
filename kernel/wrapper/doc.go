// Package wrapper binds contracts to runtime observability primitives.
//
// A Cell registers an HTTP handler via runtime/auth.Mount or an outbox
// consumer via runtime/eventrouter with a ContractSpec — the spec carries
// the contract id, transport, method/path (or topic). wrapper.HTTPHandler
// and wrapper.WrapConsumer emit one trace span per invocation annotated
// with stable attributes (gocell.contract.id, gocell.contract.kind,
// http.method/route/status_code or messaging.destination/system). Callers
// compose the wrapper into their middleware chain; the Tracer is injected
// via WithTracer, defaulting to NoopTracer.
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
