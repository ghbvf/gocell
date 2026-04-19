package postgres

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/crypto"
)

// PlaintextMigrationConfig controls the batch-encrypt migration behaviour.
type PlaintextMigrationConfig struct {
	// BatchSize is the number of rows to encrypt per DB round-trip.
	// Defaults to 50 when zero.
	BatchSize int

	// RateLimitDelay is an optional sleep between batches to reduce DB load.
	// Zero means no delay.
	RateLimitDelay time.Duration
}

// PlaintextMigrationResult summarises a completed migration run.
type PlaintextMigrationResult struct {
	// Processed is the number of rows that were encrypted during this run.
	Processed int
	// Skipped is the number of rows that were already encrypted (idempotent).
	Skipped int
}

// plaintextMigrator encrypts sensitive config_entries rows that were written
// before the ValueTransformer was wired (value_cipher IS NULL AND sensitive=true).
// It is idempotent: rows already encrypted (value_cipher IS NOT NULL) are
// skipped without modification.
//
// The migrator is intentionally NOT tied to the ConfigRepository write path so
// that it can be run as a one-off admin tool independently of normal traffic.
type plaintextMigrator struct {
	db          DBTX
	transformer crypto.ValueTransformer
	cfg         PlaintextMigrationConfig
}

// newPlaintextMigrator creates a migrator backed by the given DBTX and
// transformer. db must already be in a live transaction (the caller is
// responsible for Tx management so the migrator can participate in the
// caller's transaction boundary or run outside one as needed).
func newPlaintextMigrator(db DBTX, transformer crypto.ValueTransformer, cfg PlaintextMigrationConfig) (*plaintextMigrator, error) {
	if transformer == nil {
		return nil, errcode.New(errcode.ErrConfigKeyMissing,
			"plaintext-migrator: transformer must not be nil")
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 50
	}
	return &plaintextMigrator{db: db, transformer: transformer, cfg: cfg}, nil
}

// MigrateConfigEntries scans config_entries for sensitive rows with no
// value_cipher and encrypts them in batches. Returns a summary of rows
// processed and skipped. Fail-closed: stops on the first encryption or DB
// error.
func (m *plaintextMigrator) MigrateConfigEntries(ctx context.Context) (PlaintextMigrationResult, error) {
	return m.migrateTable(ctx, "config_entries")
}

// MigrateConfigVersions scans config_versions for sensitive rows with no
// value_cipher and encrypts them in batches.
func (m *plaintextMigrator) MigrateConfigVersions(ctx context.Context) (PlaintextMigrationResult, error) {
	return m.migrateTable(ctx, "config_versions")
}

// migrateTable is the shared implementation for both tables.
func (m *plaintextMigrator) migrateTable(ctx context.Context, table string) (PlaintextMigrationResult, error) {
	const (
		selectConfigEntries  = `SELECT id, key, value FROM config_entries WHERE sensitive = true AND value_cipher IS NULL LIMIT $1`
		selectConfigVersions = `SELECT id, key, value FROM config_versions WHERE sensitive = true AND value_cipher IS NULL LIMIT $1`
		updateConfigEntries  = `UPDATE config_entries SET value = '', value_cipher = $1, value_key_id = $2, value_edk = $3, value_nonce = $4 WHERE id = $5`
		updateConfigVersions = `UPDATE config_versions SET value = '', value_cipher = $1, value_key_id = $2, value_edk = $3, value_nonce = $4 WHERE id = $5`
	)

	var selectQ, updateQ string
	switch table {
	case "config_entries":
		selectQ, updateQ = selectConfigEntries, updateConfigEntries
	case "config_versions":
		selectQ, updateQ = selectConfigVersions, updateConfigVersions
	default:
		return PlaintextMigrationResult{}, fmt.Errorf("plaintext-migrator: unknown table %q", table)
	}

	var result PlaintextMigrationResult

	for {
		rows, err := m.db.Query(ctx, selectQ, m.cfg.BatchSize)
		if err != nil {
			return result, errcode.Wrap(errcode.ErrConfigRepoQuery,
				fmt.Sprintf("plaintext-migrator: query %s", table), err)
		}

		type pendingRow struct {
			id    string
			key   string
			value string
		}
		var batch []pendingRow
		for rows.Next() {
			var r pendingRow
			if scanErr := rows.Scan(&r.id, &r.key, &r.value); scanErr != nil {
				rows.Close()
				return result, errcode.Wrap(errcode.ErrConfigRepoQuery,
					fmt.Sprintf("plaintext-migrator: scan %s", table), scanErr)
			}
			batch = append(batch, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return result, errcode.Wrap(errcode.ErrConfigRepoQuery,
				fmt.Sprintf("plaintext-migrator: rows err %s", table), err)
		}

		if len(batch) == 0 {
			break // nothing left to migrate
		}

		for _, row := range batch {
			aad := crypto.AADForConfig("config-core", row.key)
			ct, keyID, nonce, edk, encErr := m.transformer.Encrypt(ctx, []byte(row.value), aad)
			if encErr != nil {
				return result, fmt.Errorf("plaintext-migrator: encrypt key=%s: %w", row.key, encErr)
			}
			execErr := m.updateRow(ctx, updateQ, row.id, ct, keyID, nonce, edk)
			if execErr != nil {
				return result, fmt.Errorf("plaintext-migrator: update key=%s: %w", row.key, execErr)
			}
			result.Processed++
			slog.Info("plaintext-migrator: encrypted row",
				slog.String("table", table),
				slog.String("key", row.key),
				slog.String("key_id", keyID))
		}

		if m.cfg.RateLimitDelay > 0 {
			select {
			case <-ctx.Done():
				return result, ctx.Err()
			case <-time.After(m.cfg.RateLimitDelay):
			}
		}

		if len(batch) < m.cfg.BatchSize {
			break // last batch was smaller than limit — no more rows
		}
	}

	slog.Info("plaintext-migrator: migration complete",
		slog.String("table", table),
		slog.Int("processed", result.Processed),
		slog.Int("skipped", result.Skipped))
	return result, nil
}

// updateRow executes the UPDATE for a single row.
func (m *plaintextMigrator) updateRow(ctx context.Context, q, id string, ct []byte, keyID string, nonce, edk []byte) error {
	n, err := m.db.Exec(ctx, q, ct, keyID, edk, nonce, id)
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("expected 1 row updated, got %d (id=%s)", n, id)
	}
	return nil
}
