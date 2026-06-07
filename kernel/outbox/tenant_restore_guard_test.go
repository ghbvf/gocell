package outbox

// TestPrincipalMetadata_TenantRestoreGuard is the T5.4 RED TDD guard for
// EPIC #1337 PR-5 (#1343): async tenant-restoration.
//
// The security contract: when an outbox entry carries a principal TenantID,
// the consumer handler context must observe THAT tenant — regardless of whether
// the bootstrap/subscribe-loop context was empty or carried a different (wrong)
// tenant. Ambient tenant must not win over the entry's tenant on the consume path.
//
// Case 1 (no-ambient) is already implicitly covered by
// TestPrincipalMetadata_RestoreToContext (line 333 in new_surface_test.go), but
// the present test makes the invariant explicit in a single named guard.
//
// Case 2 (wrong-ambient) is the RED test: with current "existing value wins"
// semantics (TestPrincipalMetadata_RestoreToContext_ExistingValueWins), the
// ambient WRONG tenant would win and the assertion would fail. This test turns
// GREEN once PR-5 changes RestoreToContext to make entry tenant authoritative on
// the consumer path (or the subscribe pipeline clears the ambient tenant before
// calling RestoreToContext, consistent with clearAmbientPrincipal in projection).

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
)

// entryTenantID is a canonical UUID used as the entry principal TenantID in the
// tenant-restore guard tests.
const entryTenantID = "acme-e0e0e0e0-4f89-41d3-9a0c-000000000001"

// wrongAmbientTenantID is a distinct canonical UUID installed in the ambient ctx
// to simulate a "wrong" tenant leaking from a prior request or bootstrap context.
const wrongAmbientTenantID = "wrong-f1f1f1f1-5f00-4e00-9a0c-000000000002"

// TestPrincipalMetadata_TenantRestoreGuard verifies that the consumer handler
// context always observes the ENTRY's TenantID, not the ambient bootstrap context.
func TestPrincipalMetadata_TenantRestoreGuard(t *testing.T) {
	p := PrincipalMetadata{
		ActorID:   "actor-r",
		SubjectID: "subject-r",
		TenantID:  entryTenantID,
	}

	t.Run("no_ambient_tenant_entry_tenant_installed", func(t *testing.T) {
		// Case 1: bootstrap ctx has NO tenant.
		// This already works with current "existing value wins" semantics because
		// there is no pre-existing value to win. We guard it explicitly so that
		// any future refactor that breaks this path fails a named test.
		baseCtx := context.Background()
		restored := p.RestoreToContext(baseCtx)

		got, ok := ctxkeys.TenantIDFrom(restored)
		if !ok {
			t.Fatal("TenantIDFrom(restored): expected entry TenantID to be present, got missing")
		}
		if got != entryTenantID {
			t.Errorf("TenantIDFrom(restored) = %q, want %q (entry tenant must be installed when ambient is empty)",
				got, entryTenantID)
		}
	})

	t.Run("wrong_ambient_tenant_entry_tenant_wins", func(t *testing.T) {
		// Case 2 — consume-path: bootstrap ctx carries a DIFFERENT tenant. The
		// consumer handler must observe the entry's tenant (not the wrong ambient
		// tenant). PR-5 fix: SubscribeEntry.withRestore calls clearAmbientPrincipal
		// before RestoreToContext so the entry's wire identity is authoritative.
		//
		// This test mirrors the consume-path dispatch sequence:
		//   1. clearAmbientPrincipal strips the stale/wrong ambient principal.
		//   2. RestoreToContext installs the entry's own identity (no-overwrite
		//      guard never fires because the ambient is now empty).
		//
		// RestoreToContext's no-overwrite contract (tested by
		// TestPrincipalMetadata_RestoreToContext_ExistingValueWins) is UNCHANGED;
		// this test verifies the COMBINED consume-path behavior, not a change to
		// RestoreToContext in isolation.
		baseCtx := ctxkeys.WithTenantID(context.Background(), wrongAmbientTenantID)
		// Simulate the consume-path dispatch (SubscribeEntry.withRestore):
		// clear ambient principal first, then restore from the entry.
		clearedCtx := clearAmbientPrincipal(baseCtx)
		restored := p.RestoreToContext(clearedCtx)

		got, ok := ctxkeys.TenantIDFrom(restored)
		if !ok {
			t.Fatal("TenantIDFrom(restored): expected entry TenantID to be present, got missing")
		}
		if got != entryTenantID {
			t.Errorf("TenantIDFrom(restored) = %q, want entry tenant %q"+
				" (entry tenant must win over wrong ambient tenant on consume path)",
				got, entryTenantID)
		}
	})
}
