// Package runtime provides shared runtime infrastructure for the GoCell
// framework, including HTTP server, authentication middleware, background
// workers, and observability (logging, metrics, tracing).
//
// Sub-packages:
//
//   - runtime/auth  — authentication and authorization middleware
//   - runtime/http  — HTTP server, router, and handler helpers
//   - runtime/observability — slog configuration, metrics, and tracing
//   - runtime/worker — background worker lifecycle management
//
// The runtime layer depends on kernel/ and pkg/ but never on cells/ or
// adapters/. Adapters implement the interfaces defined here.
package runtime
