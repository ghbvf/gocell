package webhook

import (
	"context"

	kwh "github.com/ghbvf/gocell/kernel/webhook"
)

// SourceRepo is the persistence seam behind a durable, encrypted webhook
// [kwh.SourceStore]. The composition root (cellmodules/webhooksource) calls
// [SourceRepo.LoadAll] once at boot, feeds the recovered sources into a
// [kwh.SourceRegistry] snapshot, and injects that snapshot via
// bootstrap.WithWebhookSourceStore — so the runtime Lookup hot path stays a pure
// in-memory read (matching the eager-register-at-startup, immutable-at-runtime
// contract of the in-memory default).
//
// The seam deals exclusively in the sealed [kwh.Source] type: Upsert takes a
// Source (built from the operator-supplied secret via [kwh.NewSource]) and
// LoadAll returns Sources. A raw secret []byte never crosses this interface —
// the secret is sealed/unsealed through [kwh.Source.Encrypt] /
// [kwh.NewSourceFromCiphertext] inside the implementation, so a backing store
// holds only ciphertext and never a loose plaintext secret. The AAD that binds
// each ciphertext to its source id is computed by the kernel funnel, not passed
// across this interface, so an implementation cannot weaken the binding.
//
// Implemented by adapters/postgres.WebhookSourceRepository.
type SourceRepo interface {
	// Upsert seals src's secret and persists it under src.ID(), inserting a new
	// row or replacing the existing one (idempotent re-seed / secret rotation).
	Upsert(ctx context.Context, src kwh.Source) error

	// LoadAll unseals and returns every persisted source. The returned order is
	// unspecified. An empty store yields an empty (non-nil) slice, not an error.
	LoadAll(ctx context.Context) ([]kwh.Source, error)

	// Delete removes the persisted source registered under id. Deleting an id
	// that is not present is not an error (idempotent).
	Delete(ctx context.Context, id kwh.SourceID) error
}
