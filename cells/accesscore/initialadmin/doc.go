//go:build unix

// Package initialadmin implements first-run admin bootstrap for accesscore:
// when no user carries the admin role at service start, the injected
// GOCELL_BOOTSTRAP_ADMIN_USERNAME/PASSWORD env credentials are used to create
// an admin user with password_reset_required=true.
//
// # Consistency level
//
// L1 LocalTx — bootstrap writes the admin user and role binding via the
// accesscore repositories. No credential file is written; no outbox emission
// or cross-cell event is involved. See cells/accesscore/cell.yaml for the
// cell-level declaration.
//
// # Usage
//
// Wire via accesscore.WithInitialAdminBootstrap with required BootstrapCredentials:
//
//	accessCore := accesscore.NewAccessCore(
//	    accesscore.WithInitialAdminBootstrap(initialadmin.BootstrapCredentials{
//	        Username: []byte("admin"),
//	        Password: []byte("secret"),
//	    }),
//	)
//	// bootstrap.New auto-wires the returned Hook at phase3b.
//
// Omit [accesscore.WithInitialAdminBootstrap] entirely (e.g., interactive mode)
// and no Hook is registered — Init does not call reg.Lifecycle. The persistent
// startup credential model (ADR §D9) means the env credentials remain mandatory
// regardless of whether the lifecycle hook is wired — they protect the
// setup/admin endpoint via runtime/auth.NewBootstrapMiddleware.
//
// # Build tag
//
// This package requires unix or windows (build tag unix || windows).
// Unsupported-platform stubs live in *_unsupported.go variants.
package initialadmin
