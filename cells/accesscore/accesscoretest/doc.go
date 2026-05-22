// Package accesscoretest provides public test-infrastructure helpers for the
// accesscore cell.
//
// # Purpose
//
// Go's internal-package barrier prevents tests/integration/ and other
// external packages from importing cells/accesscore/internal/ports (the real
// UserRepository / RoleRepository / ConfigGetter interfaces) or
// cells/accesscore/internal/domain (the User / Role aggregates). This
// package lives inside the cells/accesscore subtree, so it can import those
// internal packages — but its public API exposes only value-type DTOs and
// builder helpers. External callers (tests/integration/*, journey suites)
// never need to import any cells/accesscore/internal/* package.
//
// # Public API surface (no internal leak)
//
//   - AccessFixture (sealed; constructor NewAccessFixture)
//   - SeededUser / SeededRole — value-type seed inputs
//   - SeededUserView / SeededRoleView — value-type read outputs
//   - UserStatus + UserStatusActive / Suspended / Locked
//   - AccessFixture.SeedUser / SeedRole / SeedAssignment
//   - AccessFixture.GetUser / GetRole / UserRoles / TxRunner
//   - NewCredentialInvalidator + WithInvalidatorFixture (fixture-only collapse)
//   - BuildIdentityManageService + With* options
//   - BuildConfigReceiveService + With* options
//   - FakeConfigGetter + four typed stub constructors:
//     PresentStub / SensitiveStub / NotFoundStub / ErrorStub
//
// Continued absence of internal-type leaks is gated by external_smoke_test.go,
// which uses a foreign _test package and would stop compiling the moment any
// public API forces callers to import cells/accesscore/internal/*.
//
// # Relationship to cell.go composition root
//
// cells/accesscore/cell.go is the production composition root: it wires real
// PG adapters, real JWT issuers, and real TxManagers. This package is the
// test-composition root: it wires in-memory stores and stubs so tests remain
// docker-free and fast.
//
// # Why AccessFixture aggregates via mem.Bundle + holds session/refresh
//
// PR #595 review caught a test helper in cmd/corebundle that wired
// UserRepository + RoleRepository + SetupLock from different mem stores.
// Because the effective-admin invariant (S4.0) requires atomic cross-repo
// operations, silently using two independent stores breaks the invariant
// without any compile-time or runtime signal.
//
// cells/accesscore/mem.NewBundle is the sealed funnel that vends all four
// store-paired primitives (UserRepository, RoleRepository, SetupLock,
// TxRunner) from the same underlying mem.Store. AccessFixture wraps a
// single Bundle, then adds singleton session.MemStore and refresh.Store
// instances of its own. Round-2 review of PR #845 added this extension:
// without the singletons, NewCredentialInvalidator's default fell back to
// brand-new session/refresh stores from internal/testutil, silently
// isolating it from any sessionlogin.Service a caller might later inject as
// TokenIssuer — the same mis-pairing failure PR #595 set out to prevent.
// NewCredentialInvalidator now only accepts a fixture (WithInvalidatorFixture),
// making the non-paired wiring path unexpressible.
//
// # Debug logger swap
//
// All builders default to slog.New(slog.DiscardHandler). To see verbose output
// during local debugging, swap in a text handler:
//
//	svc, fix, rec := accesscoretest.BuildIdentityManageService(t,
//	    accesscoretest.WithIdentityLogger(
//	        slog.New(slog.NewTextHandler(os.Stderr, nil)),
//	    ),
//	)
//
// # End-to-end example: BuildIdentityManageService
//
//	func ExampleBuildIdentityManageService() {
//	    svc, fix, _ := accesscoretest.BuildIdentityManageService(nil) // nil only in pkg example
//	    _ = svc
//	    _ = fix
//	}
//
// # Import scope
//
// This package is test-infrastructure: it must NOT be imported by production
// Go files. The CELLTEST-IMPORT-SCOPE-01 archtest enforces this automatically
// for all cells/{X}/{X}test packages.
//
// Tests may import this package freely. The complementary guarantee — that
// accesscoretest itself does not re-export internal types and therefore does
// not force its consumers to import cells/accesscore/internal/* — is gated
// by external_smoke_test.go (Hard funnel: Go internal barrier upstream,
// compile-time check downstream).
//
// # Deferred: spy methods
//
// AccessFixture does not expose CallsOf / Snapshot spy methods. The current
// design converges on AccessFixture as an aggregate; spy methods will be added
// when a journey criterion actually needs them.
//
// ref: PR #595 (store-pairing precedent)
// ref: PR #845 round-2 review (session/refresh pairing + internal-leak closure)
// ref: docs/plans/202605191943-044-journey-backlog-realignment.md §2
package accesscoretest
