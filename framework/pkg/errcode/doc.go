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
//   - Details ([]PublicDetail via WithDetails(PublicString(k, v) | PublicInt(k, n) | …)):
//     typed runtime fields visible to clients on 4xx responses; stripped
//     from 5xx by Error.MarshalJSON.
//   - InternalDetails ([]InternalDetail via WithInternal(InternalAttr(k, v))):
//     server-side runtime context that never appears in any HTTP response,
//     only in slog records and traces.
//
// PublicDetail and InternalDetail are sealed newtypes (unexported fields)
// so outside-package struct-literal construction of either type is a Go
// compile error. PublicDetail's value field is additionally typed as the
// sealed publicValue marker interface so callers can only construct it via
// the typed PublicString / PublicInt / PublicBool / PublicDuration /
// PublicTime constructors — wire-unsafe types (channels, functions,
// NaN/Inf floats, maps, structs, pointers) are inexpressible at compile
// time. InternalAttr keeps an untyped any value because the entire
// channel is server-only.
//
// The const-literal restriction on New/Wrap message is enforced
// statically by archtest MESSAGE-CONST-LITERAL-01 outside this package;
// InternalAttr is exempt and may carry fmt.Sprintf-formatted strings.
//
// # Assertion ctor for production panics
//
// Programmer-error / unreachable-path panics across kernel/, runtime/,
// adapters/ and cells/ use Assertion(format, args...) so the recovery
// middleware can surface a stable 500 + ErrInternal + CategoryInfra response.
// See .claude/rules/gocell/error-handling.md §5 for the A/B/C panic
// classification and the C-class re-throw exemption list.
//
// # Prefix ownership registry
//
// Every production errcode.Code must belong to a registered prefix namespace.
// The registry maps ERR_ prefix strings to owning Go module paths and enforces
// that no two modules claim the same prefix (fail-fast panic on conflict).
//
// Two entry shapes are supported:
//   - Namespace entry ("ERR_AUTH_", trailing underscore): claims all codes
//     whose string starts with the prefix.
//   - Whole-code entry ("ERR_INTERNAL", no trailing underscore): claims
//     exactly that one code string, used for single-concept generic codes
//     where a namespace prefix would over-reach (e.g. ERR_NOT_FOUND must
//     not imply ownership of a hypothetical ERR_NOTICE_ namespace).
//
// GoCell platform prefixes (65 entries: 52 namespace + 13 whole-code) are
// self-registered in init() under owner "github.com/ghbvf/gocell". External
// cell modules register their own prefixes from their own init() by calling
// errcode.RegisterPrefix(prefix, owner). OwnerOfCode uses longest-prefix
// matching so finer-grained entries take precedence over broader namespaces.
//
// The closed-set invariant is enforced at CI time by archtest
// ERRCODE-PREFIX-OWNERSHIP-01: every errcode.New / errcode.Wrap callsite
// and every exported Code sentinel in production code must have a registered
// prefix entry. Adding a new prefix requires regenerating
// pkg/errcode/testdata/prefix_set.golden (ERRCODE_PREFIX_GOLDEN_UPDATE=1).
//
// See also:
//   - Issue #1091
//   - ADR: docs/architecture/202606031200-1091-adr-errcode-prefix-ownership-registry.md
//   - Rules: .claude/rules/gocell/error-handling.md §"错误码前缀所有权 (#1091)"
package errcode
