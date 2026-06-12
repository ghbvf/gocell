package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/adapters/postgres/internal/pgexec"
	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	kwh "github.com/ghbvf/gocell/kernel/webhook"
	"github.com/ghbvf/gocell/pkg/errcode"
	runtimecrypto "github.com/ghbvf/gocell/runtime/crypto"
	runtimewebhook "github.com/ghbvf/gocell/runtime/webhook"
)

// upsertWebhookSourceSQL inserts or replaces a source's encrypted secret. The
// table has NO plaintext column, so value_cipher (NOT NULL) is always written
// from the sealed [kwh.Source.Encrypt] result — a plaintext secret has nowhere
// to land at rest.
const upsertWebhookSourceSQL = `INSERT INTO webhook_sources
    (source_id, value_cipher, value_key_id, value_edk, value_nonce, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, now(), now())
ON CONFLICT (source_id) DO UPDATE SET
    value_cipher = EXCLUDED.value_cipher,
    value_key_id = EXCLUDED.value_key_id,
    value_edk    = EXCLUDED.value_edk,
    value_nonce  = EXCLUDED.value_nonce,
    updated_at   = now()`

// selectAllWebhookSourcesSQL reads every persisted cipher tuple for the boot-time
// load. Webhook sources are a small, global set, so an unbounded full scan is the
// intended access pattern (no pagination).
const selectAllWebhookSourcesSQL = `SELECT source_id, value_cipher, value_key_id, value_edk, value_nonce
FROM webhook_sources`

// deleteWebhookSourceSQL removes one source by id. A no-op delete (unknown id)
// affects zero rows and is not an error.
const deleteWebhookSourceSQL = `DELETE FROM webhook_sources WHERE source_id = $1`

// WebhookSourceRepository is the PostgreSQL [runtimewebhook.SourceRepo] backing
// for persistent webhook source secrets (#1540). It holds the sealed pgexec
// funnel (never a raw *pgxpool.Pool — PG-REPO-AMBIENT-TX-01) and the
// [kcrypto.ValueTransformer] that seals/unseals each secret.
//
// The repository never handles a plaintext secret: Upsert seals through
// [kwh.Source.Encrypt] and writes only the resulting ciphertext columns; LoadAll
// reads the ciphertext columns and reconstructs sealed [kwh.Source] values via
// [kwh.NewSourceFromCiphertext]. The AAD binding each ciphertext to its source id
// is computed inside the kernel funnel, so this adapter cannot transplant a
// ciphertext across sources or weaken the binding.
type WebhookSourceRepository struct {
	db          pgexec.PGExecutor
	transformer kcrypto.ValueTransformer
}

// compile-time interface check.
var _ runtimewebhook.SourceRepo = (*WebhookSourceRepository)(nil)

// NewWebhookSourceRepository wraps pool in the sealed pgexec funnel and binds the
// value transformer used to seal/unseal secrets.
//
// transformer is validated first: nil is rejected (no plaintext fallback), and
// [runtimecrypto.NoopTransformer] is rejected by a concrete-type guard — passing
// the passthrough transformer would persist webhook source secrets in plaintext,
// which the table schema structurally prevents (no plaintext column). This
// rejection is now ENFORCED by a runtime guard, not merely narrated (contrast
// configcore, which permits Noop for non-sensitive entries). pool must also be
// non-nil.
func NewWebhookSourceRepository(pool *pgxpool.Pool, transformer kcrypto.ValueTransformer) (*WebhookSourceRepository, error) {
	if transformer == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"postgres.NewWebhookSourceRepository: value transformer must not be nil "+
				"(webhook source secrets are never persisted in plaintext)")
	}
	if _, ok := transformer.(runtimecrypto.NoopTransformer); ok {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"postgres.NewWebhookSourceRepository: NoopTransformer (passthrough) would persist "+
				"webhook source secrets in plaintext")
	}
	if pool == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"postgres.NewWebhookSourceRepository: pool must not be nil")
	}
	return &WebhookSourceRepository{db: pgexec.New(pool), transformer: transformer}, nil
}

// Upsert implements [runtimewebhook.SourceRepo].
func (r *WebhookSourceRepository) Upsert(ctx context.Context, src kwh.Source) error {
	res, err := src.Encrypt(ctx, r.transformer)
	if err != nil {
		return err // already an *errcode.Error from the kernel funnel
	}
	if _, err := r.db.Exec(ctx, upsertWebhookSourceSQL,
		string(src.ID()), res.Ciphertext, res.KeyID, res.EDK, res.Nonce); err != nil {
		return classifyPGError(err, ErrAdapterPGQuery, "webhook source upsert")
	}
	return nil
}

// LoadAll implements [runtimewebhook.SourceRepo].
func (r *WebhookSourceRepository) LoadAll(ctx context.Context) ([]kwh.Source, error) {
	rows, err := r.db.Query(ctx, selectAllWebhookSourcesSQL)
	if err != nil {
		return nil, classifyPGError(err, ErrAdapterPGQuery, "webhook source load-all")
	}
	defer rows.Close()

	sources := make([]kwh.Source, 0)
	for rows.Next() {
		var (
			sourceID    string
			cipher, edk []byte
			nonce       []byte
			keyID       string
		)
		if err := rows.Scan(&sourceID, &cipher, &keyID, &edk, &nonce); err != nil {
			return nil, classifyPGError(err, ErrAdapterPGQuery, "webhook source scan")
		}
		id, err := kwh.NewSourceID(sourceID)
		if err != nil {
			return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGSchemaShape,
				"postgres: persisted webhook source_id violates kernel id shape", err,
				errcode.WithInternal(errcode.InternalAttr("_", "webhook source load-all")))
		}
		src, err := kwh.NewSourceFromCiphertext(ctx, r.transformer, id, cipher, keyID, nonce, edk)
		if err != nil {
			return nil, err // fail-closed: a source whose secret cannot be recovered aborts the load
		}
		sources = append(sources, src)
	}
	if err := rows.Err(); err != nil {
		return nil, classifyPGError(err, ErrAdapterPGQuery, "webhook source rows")
	}
	return sources, nil
}

// Delete implements [runtimewebhook.SourceRepo].
func (r *WebhookSourceRepository) Delete(ctx context.Context, id kwh.SourceID) error {
	if _, err := r.db.Exec(ctx, deleteWebhookSourceSQL, string(id)); err != nil {
		return classifyPGError(err, ErrAdapterPGQuery, "webhook source delete")
	}
	return nil
}
