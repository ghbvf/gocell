// Package errcode provides structured error codes for the GoCell framework.
// All errors exposed across package boundaries must use this package instead of
// bare errors.New. Error codes follow the ERR_{MODULE}_{REASON} convention.
//
// # Three-channel runtime data isolation
//
// Error separates runtime data into three channels with distinct visibility:
//
//   - Message: a compile-time const literal describing the failure shape.
//     Visible to clients in HTTP responses for both 4xx and 5xx.
//   - Details ([]slog.Attr via WithDetails): typed runtime fields visible
//     to clients on 4xx responses; stripped from 5xx by Error.MarshalJSON.
//   - InternalMessage (via WithInternal): server-side runtime context that
//     never appears in any HTTP response, only in slog records and traces.
//
// The const-literal restriction on New/Wrap message is enforced statically by
// archtest MESSAGE-CONST-LITERAL-01 outside this package; WithInternal is
// exempt and may carry fmt.Sprintf-formatted strings.
//
// # Assertion ctor for production panics
//
// Programmer-error / unreachable-path panics across kernel/, runtime/,
// adapters/ and cells/ use Assertion(format, args...) so the recovery
// middleware can surface a stable 500 + ErrInternal + CategoryInfra response.
// See .claude/rules/gocell/error-handling.md §5 for the A/B/C panic
// classification and the C-class re-throw exemption list.
package errcode
