//go:build integration

package pgshare

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestNew_ReturnsNonNil asserts New is a thin constructor returning a usable
// *Shared. Name validation is lazy (delegated to pgclone at first
// NewPerTestPool), so even a syntactically bad name constructs without error.
func TestNew_ReturnsNonNil(t *testing.T) {
	t.Parallel()
	s := New("1invalid-name")
	if s == nil || s.inner == nil {
		t.Fatal("New must return a non-nil *Shared with non-nil inner")
	}
}

// TestShared_Shutdown_NoOpWhenUninitialized asserts Shutdown before any
// NewPerTestPool call does not panic (delegates to pgclone, whose shutdown
// closure is nil until boot).
func TestShared_Shutdown_NoOpWhenUninitialized(t *testing.T) {
	t.Parallel()
	s := New("never_initialized")
	s.Shutdown()
}

// Package-level shared instance used by the Docker-bound integration tests
// below — the same pattern pgshare exposes to its callers.
var pgshareSelfTest = New("gocell_pgshare_selftest_template")

// TestMain owns shared-container teardown for the Docker-bound tests below.
func TestMain(m *testing.M) {
	code := m.Run()
	pgshareSelfTest.Shutdown()
	os.Exit(code)
}

// TestNewPerTestPool_IsolatesPerTestDatabases boots the shared container (first
// call), clones two per-test databases, and asserts they are fully isolated —
// a write into one is invisible from the other. Exercises the full Docker path
// including the real adapters/postgres migration callback.
func TestNewPerTestPool_IsolatesPerTestDatabases(t *testing.T) {
	poolA := pgshareSelfTest.NewPerTestPool(t)
	poolB := pgshareSelfTest.NewPerTestPool(t)

	ctx := context.Background()

	tableA := "pgshare_iso_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := poolA.DB().Exec(ctx, "CREATE TABLE "+tableA+" (x int)"); err != nil {
		t.Fatalf("create table in pool A: %v", err)
	}
	if _, err := poolA.DB().Exec(ctx, "INSERT INTO "+tableA+" (x) VALUES (1)"); err != nil {
		t.Fatalf("insert into pool A: %v", err)
	}

	var exists bool
	row := poolB.DB().QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)",
		tableA)
	if err := row.Scan(&exists); err != nil {
		t.Fatalf("isolation probe on pool B: %v", err)
	}
	if exists {
		t.Fatalf("isolation broken: table %s created in pool A is visible from pool B", tableA)
	}

	var count int
	if err := poolA.DB().QueryRow(ctx, "SELECT COUNT(*) FROM "+tableA).Scan(&count); err != nil {
		t.Fatalf("recount on pool A: %v", err)
	}
	if count != 1 {
		t.Fatalf("pool A lost its row: count = %d, want 1", count)
	}
}

// TestNewPerTestPool_TemplateSchemaApplied verifies the adapters/postgres
// migrations are applied to the template DB and inherited by every clone. This
// is the adapterpg-specific path that pgclone's own (pgx-only seed) self-tests
// do not cover.
func TestNewPerTestPool_TemplateSchemaApplied(t *testing.T) {
	pool := pgshareSelfTest.NewPerTestPool(t)
	ctx := context.Background()

	var migrationCount int
	// Platform migrations track in schema_migrations_platform (the "platform"
	// namespace, #1089), not the bare goose default schema_migrations.
	row := pool.DB().QueryRow(ctx, "SELECT COUNT(*) FROM schema_migrations_platform")
	if err := row.Scan(&migrationCount); err != nil {
		t.Fatalf("schema_migrations_platform probe: %v — migrations may not have been applied to template", err)
	}
	if migrationCount == 0 {
		t.Fatal("schema_migrations_platform is empty in cloned DB; template did not receive migrations")
	}
}
