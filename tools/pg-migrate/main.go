// Command pg-migrate applies all embedded postgres migrations against the
// database at GOCELL_PG_DSN (or the -dsn flag if provided). Intended for the
// e2e docker-compose harness as a one-shot service that runs after postgres
// becomes healthy and before corebundle starts; corebundle's
// VerifyExpectedVersion guard requires the schema to already be at the latest
// version.
//
// Production deployments run migrations through their own ops process — this
// tool exists for ephemeral test environments only.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
)

func main() {
	dsn := flag.String("dsn", os.Getenv("GOCELL_PG_DSN"), "postgres connection string (default: $GOCELL_PG_DSN)")
	timeout := flag.Duration("timeout", 60*time.Second, "overall migration timeout")
	flag.Parse()

	if *dsn == "" {
		fmt.Fprintln(os.Stderr, "pg-migrate: DSN required (set -dsn or $GOCELL_PG_DSN)")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: *dsn})
	if err != nil {
		fmt.Fprintf(os.Stderr, "pg-migrate: open pool: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = pool.Close(ctx) }()

	migrator, err := adapterpg.NewMigrator(pool, adapterpg.MigrationsFS(), "schema_migrations")
	if err != nil {
		fmt.Fprintf(os.Stderr, "pg-migrate: build migrator: %v\n", err)
		os.Exit(1)
	}
	if err := migrator.Up(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "pg-migrate: apply migrations: %v\n", err)
		os.Exit(1)
	}
}
