// Package httputil provides shared HTTP response helpers for GoCell handlers,
// including JSON success/error writers and the standard response envelope.
//
// # Error response helpers
//
// Three functions write structured error responses; choose based on where the
// status code originates:
//
//   - WriteError(ctx, w, err) — general-purpose fallback. Accepts any error;
//     derives the HTTP status from errcode.Kind via the Kind → Status mapping.
//     Used by generated handlers for un-declared framework 5xx paths (panic
//     recover, infrastructure faults) and for pre-service decode errors.
//
//   - WriteErrorWithStatus(ctx, w, status, ecErr) — typed-envelope path. The
//     caller (a generated visit{Method}Response method) pins the exact HTTP
//     status drawn from the struct identity (e.g. Get404ErrorResponse → 404).
//     Applies the same 4xx/5xx redaction policy as WriteError; status is never
//     re-derived from ecErr.Kind. Use this only from generated code — business
//     logic must return a typed response struct, not call this directly.
//
//   - WritePublic(ctx, w, kind, code, message) — framework-internal path for
//     cases where the kind, code, and message are already known at the call
//     site (e.g. middleware writing a fixed auth-failure body). The message
//     argument must be a compile-time const literal (MESSAGE-CONST-LITERAL-01).
//
// For codegen-driven cell adapters the correct pattern is to return a typed
// response struct (Xxx{Status}ErrorResponse{Body: *errcode.Error}) from the
// Service method, not to call any WriteError variant directly. See
// tools/codegen/contractgen/doc.go and
// docs/architecture/202605061500-adr-typed-response-envelope.md.
package httputil
