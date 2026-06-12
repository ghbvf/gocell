//go:build integration

package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	kwh "github.com/ghbvf/gocell/kernel/webhook"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/crypto"
)

// webhookSourceFixture builds a WebhookSourceRepository over a fresh per-test
// database (cloned from the migrated template, so webhook_sources exists) backed
// by a real local-aes ValueTransformer. The same transformer is returned so a
// test can hand-craft a cipher tuple and exercise the AAD binding end-to-end with
// real AES-GCM. The raw *pgxpool.Pool is returned for direct row insertion.
func webhookSourceFixture(t *testing.T) (*WebhookSourceRepository, kcrypto.ValueTransformer, *Pool) {
	t.Helper()
	kp, err := crypto.NewLocalAESKeyProviderFromKeys(strings.Repeat("ab", 32), "")
	require.NoError(t, err)
	vt := crypto.NewValueTransformer(kp)
	pool := migratedPool(t)
	repo, err := NewWebhookSourceRepository(pool.DB(), vt)
	require.NoError(t, err)
	return repo, vt, pool
}

func mustWebhookSource(t *testing.T, id, secret string) kwh.Source {
	t.Helper()
	sid, err := kwh.NewSourceID(id)
	require.NoError(t, err)
	src, err := kwh.NewSource(sid, []byte(secret))
	require.NoError(t, err)
	return src
}

func findSource(sources []kwh.Source, id string) (kwh.Source, bool) {
	for _, s := range sources {
		if string(s.ID()) == id {
			return s, true
		}
	}
	return kwh.Source{}, false
}

// assertSameSecret proves two Sources carry the same secret without reading the
// (unexported, getter-less) secret field: identical (payload, ts, deliveryID)
// inputs produce identical HMAC signatures iff the signing secrets are equal.
func assertSameSecret(t *testing.T, want, got kwh.Source) {
	t.Helper()
	did, err := kwh.NewDeliveryID("delivery-1")
	require.NoError(t, err)
	ts := time.Unix(1700000000, 0)
	payload := []byte("the-webhook-payload-body")

	wSigner, err := kwh.NewHMACSigner(want)
	require.NoError(t, err)
	wHeaders, err := wSigner.Sign(payload, ts, did)
	require.NoError(t, err)

	gSigner, err := kwh.NewHMACSigner(got)
	require.NoError(t, err)
	gHeaders, err := gSigner.Sign(payload, ts, did)
	require.NoError(t, err)

	assert.Equal(t, wHeaders, gHeaders, "decrypted secret must match the original")
}

func TestWebhookSourceRepository_Roundtrip(t *testing.T) {
	ctx := context.Background()
	repo, _, _ := webhookSourceFixture(t)

	src := mustWebhookSource(t, "github", "github-webhook-shared-secret-01")
	require.NoError(t, repo.Upsert(ctx, src))

	loaded, err := repo.LoadAll(ctx)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	got, ok := findSource(loaded, "github")
	require.True(t, ok)
	assert.Equal(t, src.ID(), got.ID())
	assertSameSecret(t, src, got)
}

func TestWebhookSourceRepository_UpsertReplacesSecret(t *testing.T) {
	ctx := context.Background()
	repo, _, _ := webhookSourceFixture(t)

	require.NoError(t, repo.Upsert(ctx, mustWebhookSource(t, "github", "old-secret-rotated-away-0001")))
	rotated := mustWebhookSource(t, "github", "new-secret-after-rotation-0002")
	require.NoError(t, repo.Upsert(ctx, rotated))

	loaded, err := repo.LoadAll(ctx)
	require.NoError(t, err)
	require.Len(t, loaded, 1, "upsert under the same id must replace, not duplicate")
	got, ok := findSource(loaded, "github")
	require.True(t, ok)
	assertSameSecret(t, rotated, got)
}

func TestWebhookSourceRepository_Delete(t *testing.T) {
	ctx := context.Background()
	repo, _, _ := webhookSourceFixture(t)

	require.NoError(t, repo.Upsert(ctx, mustWebhookSource(t, "github", "github-webhook-shared-secret-01")))
	require.NoError(t, repo.Delete(ctx, mustSourceID(t, "github")))

	loaded, err := repo.LoadAll(ctx)
	require.NoError(t, err)
	assert.Empty(t, loaded)

	// Deleting an absent source is idempotent (no error, no rows).
	require.NoError(t, repo.Delete(ctx, mustSourceID(t, "stripe")))
}

func TestWebhookSourceRepository_MultiSourcePersist(t *testing.T) {
	ctx := context.Background()
	repo, _, _ := webhookSourceFixture(t)

	gh := mustWebhookSource(t, "github", "github-webhook-shared-secret-01")
	st := mustWebhookSource(t, "stripe", "stripe-webhook-shared-secret-02")
	require.NoError(t, repo.Upsert(ctx, gh))
	require.NoError(t, repo.Upsert(ctx, st))

	loaded, err := repo.LoadAll(ctx)
	require.NoError(t, err)
	require.Len(t, loaded, 2)
	gotGH, ok := findSource(loaded, "github")
	require.True(t, ok)
	assertSameSecret(t, gh, gotGH)
	gotST, ok := findSource(loaded, "stripe")
	require.True(t, ok)
	assertSameSecret(t, st, gotST)
}

func TestWebhookSourceRepository_EmptyLoadAll(t *testing.T) {
	ctx := context.Background()
	repo, _, _ := webhookSourceFixture(t)

	loaded, err := repo.LoadAll(ctx)
	require.NoError(t, err)
	assert.NotNil(t, loaded)
	assert.Empty(t, loaded)
}

// TestWebhookSourceRepository_AADTransplantRejected proves the AAD binding holds
// against real AES-GCM: a ciphertext encrypted for "github" but stored under
// source_id "stripe" fails the tag check on load (the loader recomputes the AAD
// from "stripe"), so LoadAll fails closed rather than serving the wrong secret.
func TestWebhookSourceRepository_AADTransplantRejected(t *testing.T) {
	ctx := context.Background()
	repo, vt, pool := webhookSourceFixture(t)

	// Seal a secret bound to "github".
	gh := mustWebhookSource(t, "github", "github-webhook-shared-secret-01")
	res, err := gh.Encrypt(ctx, vt)
	require.NoError(t, err)

	// Plant that ciphertext under a DIFFERENT source id (transplant attack).
	_, err = pool.DB().Exec(ctx, upsertWebhookSourceSQL,
		"stripe", res.Ciphertext, res.KeyID, res.EDK, res.Nonce)
	require.NoError(t, err)

	_, err = repo.LoadAll(ctx)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrWebhookSecretCryptoFailed, ec.Code)
}

func mustSourceID(t *testing.T, id string) kwh.SourceID {
	t.Helper()
	sid, err := kwh.NewSourceID(id)
	require.NoError(t, err)
	return sid
}

// TestVerifyExpectedShape_FrozenColumnSet_WebhookSources verifies that adding
// an unexpected column to webhook_sources causes VerifyExpectedShape to return
// ErrAdapterPGSchemaShape. The extra column is dropped after the assertion so
// the per-test database is left in a clean state for pool reuse.
func TestVerifyExpectedShape_FrozenColumnSet_WebhookSources(t *testing.T) {
	ctx := context.Background()
	pool := migratedPool(t)

	// Inject an unexpected column (simulates an out-of-band DDL that would
	// allow persisting a plaintext secret — the exact threat the frozen-set
	// check guards against).
	_, err := pool.DB().Exec(ctx, `ALTER TABLE webhook_sources ADD COLUMN value bytea`)
	require.NoError(t, err, "ADD COLUMN must succeed (superuser in testcontainer)")

	t.Cleanup(func() {
		_, _ = pool.DB().Exec(ctx, `ALTER TABLE webhook_sources DROP COLUMN IF EXISTS value`)
	})

	err = VerifyExpectedShape(ctx, pool)
	require.Error(t, err, "VerifyExpectedShape must reject an unexpected column on webhook_sources")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "error must be *errcode.Error")
	assert.Equal(t, ErrAdapterPGSchemaShape, ec.Code, "error code must be ErrAdapterPGSchemaShape")
	assert.Contains(t, ec.Message, "unexpected column on frozen table",
		"message must describe the frozen-set fault")
}

// TestWebhookSourceRepository_LoadAll_CorruptSourceID verifies that a persisted
// row with an invalid source_id (e.g. uppercase, violating the kernel shape
// rule) causes LoadAll to return ErrAdapterPGSchemaShape (adapter data-shape
// error), not the kernel's ErrWebhookConfigInvalid (which would look like a
// caller config error rather than DB corruption).
func TestWebhookSourceRepository_LoadAll_CorruptSourceID(t *testing.T) {
	ctx := context.Background()
	repo, vt, pool := webhookSourceFixture(t)

	// Build a valid cipher envelope to satisfy NOT NULL column constraints,
	// then insert it under an invalid source_id (uppercase violates
	// ^[a-z][a-z0-9_-]*$ enforced by kwh.NewSourceID).
	gh := mustWebhookSource(t, "github", "github-webhook-shared-secret-01")
	res, err := gh.Encrypt(ctx, vt)
	require.NoError(t, err)

	_, err = pool.DB().Exec(ctx, upsertWebhookSourceSQL,
		"GITHUB", res.Ciphertext, res.KeyID, res.EDK, res.Nonce)
	require.NoError(t, err, "direct INSERT with invalid source_id must succeed at DB level")

	t.Cleanup(func() {
		_, _ = pool.DB().Exec(ctx, `DELETE FROM webhook_sources WHERE source_id = 'GITHUB'`)
	})

	_, err = repo.LoadAll(ctx)
	require.Error(t, err, "LoadAll must fail when a persisted source_id violates kernel shape")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "error must be *errcode.Error")
	assert.Equal(t, ErrAdapterPGSchemaShape, ec.Code,
		"corrupt persisted source_id must surface as ErrAdapterPGSchemaShape (DB corruption), not ErrWebhookConfigInvalid (caller error)")
}
