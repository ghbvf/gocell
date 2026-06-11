// Package credentialauthority is the read-side funnel for "is this
// user-bound credential authorized to issue or continue using a session?"
// decisions.
//
// Funnel 适用域 (ADR §A11 重写后): user-bound credential checks only —
// domain.(*User).CanAuthenticate() + domain.User.PasswordVersion.
// Session-state checks (session.{Session,ValidateView}.RevokedAt) are
// **not** in this funnel; they are owned by SESSION-REVOKED-FIELD-ACCESS-01,
// an independent Hard funnel that allowlists owner packages
// (sessionvalidate/, sessionrefresh/, runtime/auth/session/). Splitting the
// two concerns eliminates the apply(_ *User) underscore建模错位 (the old
// SessionNotRevoked Check declared a user receiver it never read) and lets
// slice code reject revoked sessions BEFORE any user lookup runs — closing
// the P1-A wire-status drift (revoked + inactive → 403 / revoked + userRepo
// outage → 503) into a single uniform 401 envelope.
//
// It mirrors the write-side authzmutate.Mutator funnel: callers that mutate
// authz state must go through authzmutate; callers that read user-bound
// authz state to gate token issue/use must go through Assert. Together the
// two funnels form the read-side / write-side bidirectional closure of
// credential authority. Session-state is the third independent prong.
//
// # Funnel surface
//
//	Assert(user *domain.User, checks ...Check) error
//
// Assert always runs the baseline check (user.CanAuthenticate()) inline,
// then applies each variadic Check in order. Both failure modes
// (baseline / version-pin) collapse to a single (KindPermissionDenied,
// ErrAuthUserNotActive) error shape by design — the slice wire response
// stays uniform-401 防枚举. Specific reason is carried in
// errcode.WithInternal for slog only.
//
// Callers MUST NOT branch on err to discover which check failed. When
// different side effects are required per failure class (e.g. sessionrefresh
// cascades only on baseline-fail), the slice issues two separate Assert
// calls and routes the side effect by call site, not by error inspection.
//
// # Hard funnel enforcement (archtest CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01)
//
// Four-prong Hard archtest enforces the funnel closure:
//
//   - Downstream prong (caller allowlist): production calls to Assert may
//     only originate from sessionlogin/, sessionrefresh/, sessionvalidate/
//     (slice prefixes) or this package itself.
//
//   - Upstream prong, direct (mandatory funnel): production code under the
//     three slice prefixes MUST NOT directly call user.CanAuthenticate()
//     or read user.PasswordVersion outside Assert.
//
//   - Upstream prong, sealed-by-name: concrete Check struct types declared
//     in this package MUST be unexported, so external callers cannot
//     zero-value construct a Check skipping the factory function (see
//     withPasswordVersionPin + SnapshotPasswordVersion).
//
//   - Upstream prong, value-capture: slice production files MUST NOT
//     capture credentialauthority.Assert or domain.(*User).CanAuthenticate
//     as a function value (AssignStmt / ValueSpec / CallExpr-arg),
//     defeating the direct-CallExpr funnel.
//
// Session-state (RevokedAt) is owned by an independent archtest
// SESSION-REVOKED-FIELD-ACCESS-01 (Hard upstream allowlist).
//
// Blind-spot self-checks cover method-value assignment (EachInSubtree on
// the entire file, covering chained-call shapes), reflect.MethodByName,
// reflect.FieldByName(PasswordVersion), unsafe.Pointer reads, and
// slice-internal helper indirection.
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
// See ADR docs/architecture/202605101400-adr-credential-session-protocol.md
// §A11 (重写) + §A12 (wire-uniformity 防枚举载体).
package credentialauthority
