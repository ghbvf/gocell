// Package auth holds the kernel-level authentication plan model: the sealed
// AuthPlan / ListenerAuth interfaces, their five typed implementations (None,
// JWT, JWTFromAssembly, MTLS, ServiceToken), and the narrow dependency
// interfaces (IntentTokenVerifier, NonceStore, ServiceKeyring, AuthProvider, ...)
// that runtime/auth concrete types satisfy structurally.
//
// Previously these lived in kernel/cell. They were extracted in PR #615 to
// keep kernel/cell focused on the Cell/Registrar registration model, mirroring
// the package boundary that Kratos draws between its `registry/` (cell
// registration) and `middleware/auth/` (authentication) sub-packages.
//
// Dependency direction:
//   - kernel/auth depends on kernel/cell only for the Cell type referenced by
//     AssemblyRef.Cell(id) — a one-way edge, no cycle.
//   - runtime/auth provides the concrete verifiers / nonce stores / HMAC rings
//     and uses type aliases (auth.Claims, auth.TokenIntent) to share data
//     across the kernel/runtime boundary without conversion.
//
// ref: kratos middleware/auth/ — auth as an independent sub-package alongside
//
//	registry/, transport/, log/.
//
// ref: kubernetes/apiserver pkg/authentication/authenticator/interfaces.go —
//
//	sealed Token/Request/Password authenticator interfaces.
package auth
