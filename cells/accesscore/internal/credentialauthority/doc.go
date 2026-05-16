// Package credentialauthority is the single read-side funnel for "is this
// credential authorized to issue or continue using a session?" decisions.
//
// It mirrors the write-side authzmutate.Mutator funnel: callers that mutate
// authz state must go through authzmutate; callers that read authz state to
// gate token issue/use must go through Assert. Together the two funnels form
// the read-side / write-side bidirectional closure of credential authority.
//
// # Funnel surface
//
//	Assert(user *domain.User, checks ...Check) error
//
// Assert always runs the baseline check (user.CanAuthenticate()) inline,
// then applies each variadic Check in order. The three failure modes
// (baseline / version-pin / session-revoked) collapse to a single
// (KindPermissionDenied, ErrAuthUserNotActive) error shape by design — the
// slice wire response stays uniform-401 防枚举. Specific reason is carried
// in errcode.WithInternal for slog only.
//
// Callers MUST NOT branch on err to discover which check failed. When
// different side effects are required per failure class (e.g. sessionrefresh
// cascades only on baseline-fail), the slice issues two separate Assert
// calls and routes the side effect by call site, not by error inspection.
//
// # Hard funnel enforcement (archtest CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01)
//
// Two-prong Hard archtest enforces the funnel closure:
//
//   - Downstream prong (caller allowlist): production calls to Assert may
//     only originate from sessionlogin/, sessionrefresh/, sessionvalidate/
//     (slice prefixes) or this package itself.
//
//   - Upstream prong (mandatory funnel): production code under the three
//     slice prefixes MUST NOT directly call user.CanAuthenticate(), read
//     user.PasswordVersion, or read session.{Session,ValidateView}.RevokedAt
//     outside Assert.
//
// Blind-spot self-checks cover method-value assignment, reflect.MethodByName,
// reflect.FieldByName, unsafe.Pointer writes, and slice-internal helper
// indirection.
//
// # Known caveats
//
// AST scanning catches only direct calls; theoretical bypasses include
// cross-package helper wrappers (e.g. pkg/authcheck.X(user) reading
// CanAuthenticate internally) and reading user fields via an interface
// abstraction over *domain.User. Neither bypass exists in this repo's
// slice structure (slices self-contain service.go + handler.go + helpers;
// slices directly hold *domain.User, not an interface). These are listed
// as documented limitations, not Hard-rating downgrades.
//
// See ADR docs/architecture/202605101400-adr-credential-session-protocol.md §A11.
package credentialauthority
