// This file deliberately uses a foreign _test package and imports only
// public packages. It exists as a compile-time gate: if any future change
// to cells/accesscore/accesscoretest re-exports a cells/accesscore/internal/*
// type in its public API, callers like this test would need to import the
// internal package to construct or read those values — but Go's internal
// barrier forbids that import, so this file would stop compiling.
//
// Hard funnel form:
//   - upstream: Go internal-package barrier (cells/accesscore/internal/* can
//     only be imported by code rooted at cells/accesscore/) — violation
//     unexpressible at the import path level.
//   - downstream: this file's continued compilation under "go test ./..." —
//     any drift back to internal types in the public API is caught at the
//     first `go build`.
//
// Intentionally narrow: this test does not exercise behavior; the behavior
// is covered by builders_test.go in the in-package _test. The single check
// here is the import surface.
//
// Lives in the same accesscoretest_test external-test package as the other
// _test files — Go limits a directory to one external _test package, so
// "external" here is enforced by what this file imports, not by a
// freshly-named package.
package accesscoretest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/cells/accesscore/accesscoretest"
	"github.com/ghbvf/gocell/cells/accesscore/slices/identitymanage"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

// TestExternalImportSurface_NoInternalTypes calls every public symbol that
// could plausibly leak an internal/domain or internal/ports type. If any
// signature regresses, the compiler will require the corresponding internal
// import — which Go itself rejects, surfacing a build error.
func TestExternalImportSurface_NoInternalTypes(t *testing.T) {
	ctx := context.Background()

	fix := accesscoretest.NewAccessFixture(t, clock.Real())

	if err := fix.SeedUser(ctx, accesscoretest.SeededUser{
		ID: "u1", Username: "alice", Email: "alice@example.com",
		PasswordHash: "$2a$04$x",
	}); err != nil {
		t.Fatalf("SeedUser: %v", err)
	}
	if err := fix.SeedRole(ctx, accesscoretest.SeededRole{
		ID: "r1", Name: "Admin",
	}); err != nil {
		t.Fatalf("SeedRole: %v", err)
	}
	if err := fix.SeedAssignment(ctx, "u1", "r1"); err != nil {
		t.Fatalf("SeedAssignment: %v", err)
	}

	view, err := fix.GetUser(ctx, "u1")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if view.Status != accesscoretest.UserStatusActive {
		t.Fatalf("GetUser: unexpected status %q", view.Status)
	}

	roleView, err := fix.GetRole(ctx, "r1")
	if err != nil {
		t.Fatalf("GetRole: %v", err)
	}
	if roleView.ID != "r1" {
		t.Fatalf("GetRole: unexpected role %+v", roleView)
	}

	roles, err := fix.UserRoles(ctx, "u1")
	if err != nil {
		t.Fatalf("UserRoles: %v", err)
	}
	if len(roles) != 1 || roles[0].ID != "r1" {
		t.Fatalf("UserRoles: unexpected %+v", roles)
	}

	inv := accesscoretest.NewCredentialInvalidator(t,
		accesscoretest.WithInvalidatorFixture(fix),
	)
	if inv == nil {
		t.Fatal("NewCredentialInvalidator returned nil")
	}

	svc, _, rec := accesscoretest.BuildIdentityManageService(t,
		accesscoretest.WithIdentityFixture(fix),
	)
	if svc == nil || rec == nil {
		t.Fatal("BuildIdentityManageService returned nil components")
	}

	// Compile-only: exercise all four typed stub constructors.
	g := accesscoretest.NewFakeConfigGetter(map[string]accesscoretest.ConfigGetterStub{
		"plain":     accesscoretest.PresentStub("plain", "v", 1),
		"secret":    accesscoretest.SensitiveStub("secret", 2),
		"missing":   accesscoretest.NotFoundStub(),
		"transient": accesscoretest.ErrorStub(errors.New("transient")),
	})
	_, _ = g.GetEntry(ctx, "plain")

	cfgSvc := accesscoretest.BuildConfigReceiveService(t,
		accesscoretest.WithConfigReceiveConfigGetter(g),
	)
	if cfgSvc == nil {
		t.Fatal("BuildConfigReceiveService returned nil")
	}

	// Confirm runtime/auth (public package) sentinel symbols still resolve;
	// these are only here so this file's imports stay non-trivial.
	_ = auth.TestContext("u1", []string{"r1"})
	_ = identitymanage.TopicUserCreated
	_ = errcode.ErrConfigNotFound
}
