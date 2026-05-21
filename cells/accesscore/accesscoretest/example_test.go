package accesscoretest_test

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/ghbvf/gocell/cells/accesscore/accesscoretest"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/slices/identitymanage"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/auth"
)

// TestExampleBuildIdentityManageService demonstrates the recommended way to
// wire BuildIdentityManageService in a test. It shows:
//
//  1. The basic service construction pattern.
//  2. How to swap slog.DiscardHandler for os.Stderr to see verbose logs
//     during local debugging.
//  3. Seeding state via the returned AccessFixture.
//
// To swap in verbose logging, uncomment the WithIdentityLogger option:
//
//	svc, fix, rec := accesscoretest.BuildIdentityManageService(t,
//	    accesscoretest.WithIdentityLogger(
//	        slog.New(slog.NewTextHandler(os.Stderr, nil)),
//	    ),
//	)
func TestExampleBuildIdentityManageService(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Optionally swap the default discard logger for stderr output:
	_ = slog.New(slog.NewTextHandler(os.Stderr, nil)) // swap in via WithIdentityLogger

	svc, fix, rec := accesscoretest.BuildIdentityManageService(t)

	// Seed an admin user so last-admin protection does not block Create.
	admin, err := domain.NewUser("admin", "admin@example.com", "$2a$04$x", clock.Real().Now())
	if err != nil {
		t.Fatalf("domain.NewUser: %v", err)
	}
	admin.ID = "usr-admin"
	if err := fix.SeedUser(ctx, admin); err != nil {
		t.Fatalf("SeedUser: %v", err)
	}
	adminRole := &domain.Role{ID: "admin", Name: "Admin"}
	if err := fix.SeedRole(ctx, adminRole); err != nil {
		t.Fatalf("SeedRole: %v", err)
	}
	if err := fix.SeedAssignment(ctx, admin.ID, "admin"); err != nil {
		t.Fatalf("SeedAssignment: %v", err)
	}

	adminCtx := auth.TestContext("usr-admin", []string{"admin"})
	created, err := svc.Create(adminCtx, identitymanage.CreateInput{
		Username: "alice",
		Email:    "alice@example.com",
		Password: "hunter12",
	})
	if err != nil {
		t.Fatalf("svc.Create: %v", err)
	}
	t.Logf("created user ID=%s", created.ID)

	// Verify event was emitted.
	entries := rec.EntriesByType(identitymanage.TopicUserCreated)
	if len(entries) != 1 {
		t.Errorf("expected 1 user-created event, got %d", len(entries))
	}
}
