package postgres

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	configcrypto "github.com/ghbvf/gocell/cells/configcore/internal/crypto"
	"github.com/ghbvf/gocell/kernel/clock"
	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/tenant"
)

// PlaintextMigrationConfig controls the batch-encrypt migration behavior.
type PlaintextMigrationConfig struct {
	// BatchSize is the number of rows to encrypt per DB round-trip.
	// Defaults to 50 when zero.
	BatchSize int

	// RateLimitDelay is an optional sleep between batches to reduce DB load.
	// Zero means no delay.
	RateLimitDelay time.Duration
}

// PlaintextMigrationResult summarizes a completed migration run.
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
		// SELECT returns (id, tenant_id, aadIdentity=configKey, value).
		// AAD = AADForConfig(cellID, tenant, configKey). tenant_id is selected so
		// the migration's AAD matches the normal write path, which now binds the
		// owning tenant into the AAD (cross-tenant replay protection, #1479).
		return migTableQueries{
			selectQ: `SELECT id, tenant_id, key, value FROM config_entries` +
				` WHERE sensitive = true AND value_cipher IS NULL ORDER BY id LIMIT $1`,
			updateQ: `UPDATE config_entries SET value = '', value_cipher = $1, value_key_id = $2,` +
				` value_edk = $3, value_nonce = $4 WHERE id = $5 AND value_cipher IS NULL`,
		}, nil
	case "config_versions":
		// config_versions uses config_id (UUID) as the AAD identity, matching the
		// normal write path (encryptVersionValue → AADForVersion(cellID, tenant, configID)).
		// No JOIN needed: config_id and tenant_id are already on the config_versions row.
		//
		// SELECT returns (id, tenant_id, aadIdentity=config_id, value).
		// AAD = AADForVersion(cellID, tenant, config_id).
		return migTableQueries{
			selectQ: `SELECT id, tenant_id, config_id, value FROM config_versions` +
				` WHERE sensitive = true AND value_cipher IS NULL ORDER BY id LIMIT $1`,
			updateQ: `UPDATE config_versions SET value = '', value_cipher = $1, value_key_id = $2,` +
				` value_edk = $3, value_nonce = $4 WHERE id = $5 AND value_cipher IS NULL`,
		}, nil
	default:
		return migTableQueries{}, fmt.Errorf("plaintext-migrator: unknown table %q", table)
	}
}

// pendingRow holds a single row fetched from the pending-encryption query.
// aadIdentity is the value used to compute the row-specific AAD:
//   - config_entries: configKey (human-readable key name)
//   - config_versions: configID (UUID from config_entries.id)
//
// tenant is the owning tenant of the row; it is bound into the AAD so the
// migration's ciphertext matches the normal write path (cross-tenant replay
// protection, #1479).
type pendingRow struct {
	id          string
	tenant      tenant.TenantID
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
	clk         clock.Clock
}

// newPlaintextMigrator creates a migrator backed by the given DBTX and
// transformer. db must already be in a live transaction (the caller is
// responsible for Tx management so the migrator can participate in the
// caller's transaction boundary or run outside one as needed).
func newPlaintextMigrator(
	db DBTX, transformer kcrypto.ValueTransformer,
	cfg PlaintextMigrationConfig, clk clock.Clock,
) (*plaintextMigrator, error) {
	if transformer == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrConfigKeyMissing,
			"plaintext-migrator: transformer must not be nil")
	}
	clock.MustHaveClock(clk, "configcore.newPlaintextMigrator")
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 50
	}
	return &plaintextMigrator{db: db, transformer: transformer, cfg: cfg, clk: clk}, nil
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
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrConfigRepoQuery,
			"plaintext-migrator: query failed", err,
			errcode.WithDetails(errcode.PublicString("table", table)))
	}
	defer rows.Close()

	var batch []pendingRow
	for rows.Next() {
		var r pendingRow
		if scanErr := rows.Scan(&r.id, &r.tenant, &r.aadIdentity, &r.value); scanErr != nil {
			return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrConfigRepoQuery,
				"plaintext-migrator: scan failed", scanErr,
				errcode.WithDetails(errcode.PublicString("table", table)))
		}
		batch = append(batch, r)
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrConfigRepoQuery,
			"plaintext-migrator: rows iter failed", err,
			errcode.WithDetails(errcode.PublicString("table", table)))
	}
	return batch, nil
}

// computeAAD returns the row-specific Additional Authenticated Data for the given table.
// config_entries uses AADForConfig (identity = configKey);
// config_versions uses AADForVersion (identity = configID UUID).
// The tenant is bound into the AAD so the migration's ciphertext matches the
// normal write path's AAD (cross-tenant replay protection, #1479).
func computeAAD(table string, t tenant.TenantID, aadIdentity string) []byte {
	if table == "config_versions" {
		return configcrypto.AADForVersion(cellID, t, aadIdentity)
	}
	return configcrypto.AADForConfig(cellID, t, aadIdentity)
}

// encryptBatch encrypts each row in the batch and writes it back.
func (m *plaintextMigrator) encryptBatch(
	ctx context.Context, updateQ, table string, batch []pendingRow, result *PlaintextMigrationResult,
) error {
	for _, row := range batch {
		aad := computeAAD(table, row.tenant, row.aadIdentity)
		encResult, encErr := m.transformer.Encrypt(ctx, []byte(row.value), aad)
		if encErr != nil {
			return fmt.Errorf("plaintext-migrator: encrypt aad_identity=%s: %w", row.aadIdentity, encErr)
		}
		skipped, err := m.updateRow(ctx, updateQ, row.id, encResult.Ciphertext, encResult.KeyID, encResult.Nonce, encResult.EDK)
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
	timer := m.clk.NewTimerAt(m.clk.Now().Add(m.cfg.RateLimitDelay))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C():
		return nil
	}
}

// updateRow executes the UPDATE for a single row.
// Returns (skipped=true, nil) when the CAS predicate (value_cipher IS NULL) finds
// the row was already encrypted by a concurrent writer — this is not an error.
// Returns an error only for genuine DB failures or unexpected row counts (n > 1).
func (m *plaintextMigrator) updateRow(
	ctx context.Context, q, id string, ct []byte, keyID string, nonce, edk []byte,
) (skipped bool, err error) {
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
