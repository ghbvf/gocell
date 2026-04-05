// Package http provides the HTTP server, router, and handler helpers for the
// GoCell runtime.
//
// It builds on net/http and provides:
//
//   - A Router that maps contract-defined HTTP endpoints to handlers
//   - Standard health (/healthz) and readiness (/readyz) endpoints
//   - API versioning with /api/v1/ prefix (see api-versioning rules)
//   - Unified error response format: {"error":{"code":"...","message":"..."}}
//   - Request/response logging via slog middleware
//
// # Handler Convention
//
// Handlers accept (http.ResponseWriter, *http.Request) and use errcode for
// domain errors. The error middleware translates errcode.Error to the
// appropriate HTTP status code.
//
// # Graceful Shutdown
//
// The server supports graceful shutdown with configurable drain timeout.
package http
