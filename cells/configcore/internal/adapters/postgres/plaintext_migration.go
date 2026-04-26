package postgres

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	configcrypto "github.com/ghbvf/gocell/cells/configcore/internal/crypto"
	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	"github.com/ghbvf/gocell/pkg/errcode"
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

// migTableQueries holds the SQL for a specific table's migration.
type migTableQueries struct {
	selectQ string
	updateQ string
}

// tableQueries returns the SQL queries for the given table name.
func tableQueries(table string) (migTableQueries, error) {
	switch table {
	case "config_entries":
		// ORDER BY id makes batch pagination deterministic: PostgreSQL page
		// ordering is undefined without an explicit sort, which can cause
		// the same unencrypted row to appear in two successive batches, or
		// be skipped entirely, under concurrent writes.
		//
		// SELECT returns (id, aadIdentity=configKey, value).
		// AAD = AADForConfig(cellID, configKey).
		return migTableQueries{
			selectQ: `SELECT id, key, value FROM config_entries WHERE sensitive = true AND value_cipher IS NULL ORDER BY id LIMIT $1`,
			updateQ: `UPDATE config_entries SET value = '', value_cipher = $1, value_key_id = $2, value_edk = $3, value_nonce = $4 WHERE id = $5 AND value_cipher IS NULL`,
		}, nil
	case "config_versions":
		// config_versions uses config_id (UUID) as the AAD identity, matching the
		// normal write path (encryptVersionValue → AADForVersion(cellID, configID)).
		// No JOIN needed: config_id is already on the config_versions row.
		//
		// SELECT returns (id, aadIdentity=config_id, value).
		// AAD = AADForVersion(cellID, config_id).
		return migTableQueries{
			selectQ: `SELECT id, config_id, value FROM config_versions WHERE sensitive = true AND value_cipher IS NULL ORDER BY id LIMIT $1`,
			updateQ: `UPDATE config_versions SET value = '', value_cipher = $1, value_key_id = $2, value_edk = $3, value_nonce = $4 WHERE id = $5 AND value_cipher IS NULL`,
		}, nil
	default:
		return migTableQueries{}, fmt.Errorf("plaintext-migrator: unknown table %q", table)
	}
}

// pendingRow holds a single row fetched from the pending-encryption query.
// aadIdentity is the value used to compute the row-specific AAD:
//   - config_entries: configKey (human-readable key name)
//   - config_versions: configID (UUID from config_entries.id)
type pendingRow struct {
	id          string
	aadIdentity string // configKey for entries, configID for versions
	value       string
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
	transformer kcrypto.ValueTransformer
	cfg         PlaintextMigrationConfig
}

// newPlaintextMigrator creates a migrator backed by the given DBTX and
// transformer. db must already be in a live transaction (the caller is
// responsible for Tx management so the migrator can participate in the
// caller's transaction boundary or run outside one as needed).
func newPlaintextMigrator(db DBTX, transformer kcrypto.ValueTransformer, cfg PlaintextMigrationConfig) (*plaintextMigrator, error) {
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
	q, err := tableQueries(table)
	if err != nil {
		return PlaintextMigrationResult{}, err
	}

	var result PlaintextMigrationResult
	for {
		batch, err := m.fetchBatch(ctx, q.selectQ, table)
		if err != nil {
			return result, err
		}
		if len(batch) == 0 {
			break
		}
		if err := m.encryptBatch(ctx, q.updateQ, table, batch, &result); err != nil {
			return result, err
		}
		if err := m.waitRateLimit(ctx); err != nil {
			return result, err
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

// fetchBatch queries the DB for the next batch of unencrypted rows.
func (m *plaintextMigrator) fetchBatch(ctx context.Context, selectQ, table string) ([]pendingRow, error) {
	rows, err := m.db.Query(ctx, selectQ, m.cfg.BatchSize)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrConfigRepoQuery,
			fmt.Sprintf("plaintext-migrator: query %s", table), err)
	}
	defer rows.Close()

	var batch []pendingRow
	for rows.Next() {
		var r pendingRow
		if scanErr := rows.Scan(&r.id, &r.aadIdentity, &r.value); scanErr != nil {
			return nil, errcode.Wrap(errcode.ErrConfigRepoQuery,
				fmt.Sprintf("plaintext-migrator: scan %s", table), scanErr)
		}
		batch = append(batch, r)
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(errcode.ErrConfigRepoQuery,
			fmt.Sprintf("plaintext-migrator: rows err %s", table), err)
	}
	return batch, nil
}

// computeAAD returns the row-specific Additional Authenticated Data for the given table.
// config_entries uses AADForConfig (identity = configKey);
// config_versions uses AADForVersion (identity = configID UUID).
func computeAAD(table, aadIdentity string) []byte {
	if table == "config_versions" {
		return configcrypto.AADForVersion(cellID, aadIdentity)
	}
	return configcrypto.AADForConfig(cellID, aadIdentity)
}

// encryptBatch encrypts each row in the batch and writes it back.
func (m *plaintextMigrator) encryptBatch(ctx context.Context, updateQ, table string, batch []pendingRow, result *PlaintextMigrationResult) error {
	for _, row := range batch {
		aad := computeAAD(table, row.aadIdentity)
		ct, keyID, nonce, edk, encErr := m.transformer.Encrypt(ctx, []byte(row.value), aad)
		if encErr != nil {
			return fmt.Errorf("plaintext-migrator: encrypt aad_identity=%s: %w", row.aadIdentity, encErr)
		}
		skipped, err := m.updateRow(ctx, updateQ, row.id, ct, keyID, nonce, edk)
		if err != nil {
			return fmt.Errorf("plaintext-migrator: update aad_identity=%s: %w", row.aadIdentity, err)
		}
		if skipped {
			result.Skipped++
			slog.Info("plaintext-migrator: row already encrypted by concurrent write, skipped",
				slog.String("table", table),
				slog.String("id", row.id))
			continue
		}
		result.Processed++
		// keyID is intentionally redacted from the log plane: cryptographic
		// identifiers belong on Prometheus labels with bounded cardinality, not
		// on slog where they pollute log indices and complicate redaction
		// pipelines. table + aad_identity together suffice to correlate.
		slog.Info("plaintext-migrator: encrypted row",
			slog.String("table", table),
			slog.String("aad_identity", row.aadIdentity))
	}
	return nil
}

// waitRateLimit sleeps between batches if RateLimitDelay is configured.
func (m *plaintextMigrator) waitRateLimit(ctx context.Context) error {
	if m.cfg.RateLimitDelay <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(m.cfg.RateLimitDelay):
		return nil
	}
}

// updateRow executes the UPDATE for a single row.
// Returns (skipped=true, nil) when the CAS predicate (value_cipher IS NULL) finds
// the row was already encrypted by a concurrent writer — this is not an error.
// Returns an error only for genuine DB failures or unexpected row counts (n > 1).
func (m *plaintextMigrator) updateRow(ctx context.Context, q, id string, ct []byte, keyID string, nonce, edk []byte) (skipped bool, err error) {
	n, err := m.db.Exec(ctx, q, ct, keyID, edk, nonce, id)
	if err != nil {
		return false, err
	}
	if n == 0 {
		// CAS predicate failed: another writer already set value_cipher on this row.
		return true, nil
	}
	if n > 1 {
		return false, fmt.Errorf("expected 1 row updated, got %d (id=%s)", n, id)
	}
	return false, nil
}
