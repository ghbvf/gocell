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
	"strconv"
	"strings"
	"time"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/pkg/migration"
)

// defaultMigrationTimeout is the default overall timeout for applying
// all pending migrations. 60 s is generous for ephemeral test environments.
const defaultMigrationTimeout = 60 * time.Second

// parseRebuildPermits parses the -rebuild flag value into ForwardRebuildPermits.
// Input format: comma-separated "<migrationNumber>:<reason>" pairs, e.g.
// "43:audit v2 cutover,44:outbox principal cutover".
// reason may contain spaces but not commas (commas are the pair separator).
func parseRebuildPermits(s string) ([]adapterpg.ForwardRebuildPermit, error) {
	var permits []adapterpg.ForwardRebuildPermit
	for _, entry := range strings.Split(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		idx := strings.IndexByte(entry, ':')
		if idx < 0 {
			return nil, fmt.Errorf("invalid rebuild entry %q: expected <migrationNumber>:<reason>", entry)
		}
		numStr := strings.TrimSpace(entry[:idx])
		reason := strings.TrimSpace(entry[idx+1:])
		num, err := strconv.ParseInt(numStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid migration number in %q: %w", entry, err)
		}
		permit, err := adapterpg.AllowForwardRebuild(num, reason)
		if err != nil {
			return nil, fmt.Errorf("AllowForwardRebuild(%d, %q): %w", num, reason, err)
		}
		permits = append(permits, permit)
	}
	return permits, nil
}

func main() {
	dsn := flag.String("dsn", os.Getenv("GOCELL_PG_DSN"), "postgres connection string (default: $GOCELL_PG_DSN)")
	timeout := flag.Duration("timeout", defaultMigrationTimeout, "overall migration timeout")
	rebuild := flag.String("rebuild", "",
		"comma-separated list of <migrationNumber>:<reason> permits for destructive forward-rebuild "+
			"(e.g. \"43:audit v2 cutover,44:outbox principal cutover\"); "+
			"migrationNumber is the integer version prefix of the SQL filename with leading zeros stripped "+
			"(e.g. 043_*.sql → 43); reason may contain spaces but must not contain commas "+
			"(commas are the pair separator); "+
			"only needed when the target table is already populated — fresh DB use default Up instead")
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

	migrationsFS, err := adapterpg.MigrationsFS()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pg-migrate: migrations fs: %v\n", err)
		os.Exit(1)
	}
	migrator, err := adapterpg.NewMigrator(pool, migrationsFS, migration.PlatformNamespace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pg-migrate: build migrator: %v\n", err)
		os.Exit(1)
	}

	if *rebuild == "" {
		if err := migrator.Up(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "pg-migrate: apply migrations: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "pg-migrate: migrations applied successfully\n")
		return
	}

	permits, err := parseRebuildPermits(*rebuild)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pg-migrate: parse -rebuild: %v\n", err)
		os.Exit(1)
	}
	if err := migrator.ForwardRebuild(ctx, permits...); err != nil {
		fmt.Fprintf(os.Stderr, "pg-migrate: forward-rebuild: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "pg-migrate: forward-rebuild completed successfully (permits: %s)\n", *rebuild)
}
