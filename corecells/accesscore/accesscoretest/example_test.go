package accesscoretest_test

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/ghbvf/gocell/corecells/accesscore/accesscoretest"
	"github.com/ghbvf/gocell/corecells/accesscore/slices/identitymanage"
	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
	"github.com/ghbvf/gocell/framework/runtime/auth"
)

// TestExampleBuildIdentityManageService demonstrates the recommended way to
// wire BuildIdentityManageService in a test. It shows:
//
//  1. The basic service construction pattern.
//  2. How to swap slog.DiscardHandler for os.Stderr to see verbose logs
//     during local debugging.
//  3. Seeding state via the returned AccessFixture using the value-type
//     SeededUser / SeededRole DTOs (no internal/domain import required).
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
	if err := fix.SeedUser(ctx, accesscoretest.SeededUser{
		ID: "usr-admin", Username: "admin", Email: "admin@example.com",
		PasswordHash: "$2a$04$x",
	}); err != nil {
		t.Fatalf("SeedUser: %v", err)
	}
	if err := fix.SeedRole(ctx, accesscoretest.SeededRole{
		ID: "admin", Name: "Admin",
	}); err != nil {
		t.Fatalf("SeedRole: %v", err)
	}
	if err := fix.SeedAssignment(ctx, "usr-admin", "admin"); err != nil {
		t.Fatalf("SeedAssignment: %v", err)
	}

	// Inject DefaultFixtureTenantID so tenant-scoped repo operations succeed.
	adminCtx := ctxkeys.WithTenantID(auth.TestContext("usr-admin", []string{"admin"}),
		accesscoretest.DefaultFixtureTenantID.String())
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
