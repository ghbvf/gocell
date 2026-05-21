// Package accesscoretest provides public test-infrastructure helpers for the
// accesscore cell.
//
// # Purpose
//
// Go's internal-package barrier prevents tests/integration/ and other
// external packages from importing cells/accesscore/internal/ports (the real
// UserRepository / RoleRepository / ConfigGetter interfaces). This package
// lives inside the cells/accesscore subtree, so it can import internal packages
// while only exporting *Service values and testutil-local Fake types to
// callers.
//
// # Relationship to cell.go composition root
//
// cells/accesscore/cell.go is the production composition root: it wires real
// PG adapters, real JWT issuers, and real TxManagers. This package is the
// test-composition root: it wires in-memory stores and stubs so tests remain
// docker-free and fast.
//
// # Why AccessFixture aggregates via mem.Bundle
//
// PR #595 review caught a test helper in cmd/corebundle that wired
// UserRepository + RoleRepository + SetupLock from different mem stores.
// Because the effective-admin invariant (S4.0) requires atomic cross-repo
// operations, silently using two independent stores breaks the invariant
// without any compile-time or runtime signal.
//
// cells/accesscore/mem.NewBundle is a sealed funnel that vends all four
// primitives (UserRepository, RoleRepository, SetupLock, TxRunner) from the
// same underlying mem.Store. AccessFixture wraps a single Bundle, making it
// impossible to accidentally mis-pair repos from different stores.
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
//	    // Construct the service and its dependencies.
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
// Tests may import this package freely.
//
// # Deferred: spy methods
//
// AccessFixture does not expose CallsOf / Snapshot spy methods. The current
// design converges on AccessFixture as an aggregate; spy methods will be added
// when a journey criterion actually needs them.
//
// ref: PR #595 (store-pairing precedent)
// ref: docs/plans/202605191943-044-journey-backlog-realignment.md §2
package accesscoretest
