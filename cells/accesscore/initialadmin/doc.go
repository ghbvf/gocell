//go:build unix

// Package initialadmin implements scheme H first-run admin bootstrap for
// accesscore: when no user carries the admin role at service start, a random
// password is generated, written to a credential file (mode 0600), assigned
// to a new admin user with password_reset_required=true, and a TTL-bounded
// cleaner worker removes the file after the configured TTL.
//
// # Consistency level
//
// L1 LocalTx — bootstrap writes (a) the admin user and role binding via the
// accesscore repositories and (b) the credential file side-by-side on the
// host filesystem. Both succeed or neither takes effect for the caller: the
// credential-file write happens inside ensureAdmin after the user insert and
// is followed by a best-effort rollback if the password hash assignment
// fails. No outbox emission or cross-cell event is involved; see
// cells/accesscore/cell.yaml for cell-level declaration.
//
// # Usage
//
// The package exposes [Lifecycle] as the composition-root entry point. Wire
// it into accesscore via [accesscore.WithInitialAdminBootstrap]; accesscore.Init
// registers the returned Hook via reg.Lifecycle, so composition code is a single
// line:
//
//	accessCore := accesscore.NewAccessCore(
//	    accesscore.WithUserRepository(userRepo),
//	    accesscore.WithRoleRepository(roleRepo),
//	    accesscore.WithInitialAdminBootstrap(
//	        initialadmin.WithCredentialPath(path),
//	        initialadmin.WithTTL(24 * time.Hour),
//	    ),
//	)
//	// bootstrap.New auto-wires the returned Hook at phase3b.
//
// Omit [accesscore.WithInitialAdminBootstrap] entirely (e.g., demo mode) and
// no Hook is registered — Init does not call reg.Lifecycle.
//
// # Construction order
//
// [Lifecycle] uses two-phase construction because repositories are not yet
// available when NewAccessCore runs:
//
//  1. [NewLifecycle] — collects user config via [LifecycleOption]s.
//  2. [Lifecycle.Bind] — wired by accesscore.Init once UserRepo/RoleRepo exist.
//  3. [Lifecycle.Hook] — registered by accesscore.Init via reg.Lifecycle;
//     Bootstrap phase3b drains it from the RegistrySnapshot; Hook.OnStart reads
//     Bind state at invocation time so ordering is enforced without framework
//     coupling.
//
// # Blocking semantics
//
// [Lifecycle] runs the cleaner in a background goroutine so that
// bootstrap.Hook.StartTimeout (30s default) is not starved: cleaner.Start
// blocks on ctx.Done() waiting for TTL. OnStop cancels an internal runCtx
// to drain the goroutine before calling cleaner.Stop for explicit timer
// cancellation.
//
// # Scheme H reference
//
// See docs/architecture/202604181900-adr-auth-setup-first-run.md.
//
// # Build tag
//
// This package is Unix-only (file-permission 0600 is the core security
// invariant; Windows stubs live in *_unsupported.go variants).
package initialadmin
