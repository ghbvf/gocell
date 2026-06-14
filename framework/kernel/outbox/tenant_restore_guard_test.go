package outbox

// TestSubscribeEntry_ConsumePath_EntryTenantWins is the T5.4 guard for
// EPIC #1337 PR-5 (#1343): async tenant-restoration on the consumer path.
//
// Security contract (spec FR-A9 / P1 cross-tenant pollution): when an outbox
// entry carries a principal TenantID, the consumer handler MUST observe THAT
// tenant — regardless of whether the broker-delivery / subscribe-loop context
// was empty OR carried a different (wrong) tenant. An ambient tenant must never
// win over the entry's tenant on the consume path.
//
// This test drives the REAL consume dispatch: NewSubscriberWithMiddleware ->
// SubscribeEntry installs the built-in withRestore wrapper into the stub
// subscriber (cap.handler IS that wrapper), and we invoke it with a synthetic
// entry under a wrong-ambient ctx. The PR-5 fix is that withRestore calls
// clearAmbientPrincipal BEFORE entry.RestoreContext — so removing that clear (a
// future regression) makes the wrong-ambient subtest fail HERE. That is the
// whole point of driving the real path instead of composing clear+restore by
// hand (which would tautologically pass and guard nothing).
//
// RestoreToContext's own no-overwrite contract (tested by
// TestPrincipalMetadata_RestoreToContext_ExistingValueWins) is UNCHANGED; only
// the consume path pre-clears ambient principal.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
)

// entryTenantID is the canonical entry-principal TenantID under test.
const entryTenantID = "acme-e0e0e0e0-4f89-41d3-9a0c-000000000001"

// wrongAmbientTenantID is a distinct tenant installed on the delivery ctx to
// simulate a wrong tenant leaking from a prior request / bootstrap context.
const wrongAmbientTenantID = "wrong-f1f1f1f1-5f00-4e00-9a0c-000000000002"

func TestSubscribeEntry_ConsumePath_EntryTenantWins(t *testing.T) {
	capSub := &captureSubscriber{}
	wrapped, err := NewSubscriberWithMiddleware(capSub, testConsumerBase(t))
	require.NoError(t, err)

	var seenTenant string
	var seenOK bool
	require.NoError(t, wrapped.SubscribeEntry(context.Background(),
		testFullSub("test", "cg-tenant-restore"),
		func(ctx context.Context, _ Entry) HandleResult {
			seenTenant, seenOK = ctxkeys.TenantIDFrom(ctx)
			return Ack()
		}))
	require.NotNil(t, capSub.handler, "SubscribeEntry must install the built-in restore wrapper")

	// Synthetic entry carrying the authoritative wire principal (white-box).
	entry := Entry{
		id: "evt-tenant",
		principal: PrincipalMetadata{
			ActorID:   "actor-r",
			SubjectID: "subject-r",
			TenantID:  entryTenantID,
		},
	}

	t.Run("no_ambient_tenant_entry_tenant_installed", func(t *testing.T) {
		seenTenant, seenOK = "", false
		res, _ := capSub.handler(context.Background(), entry)
		require.Equal(t, DispositionAck, res.Disposition)
		require.True(t, seenOK, "entry TenantID must be installed when ambient is empty")
		assert.Equal(t, entryTenantID, seenTenant)
	})

	t.Run("wrong_ambient_tenant_entry_tenant_wins", func(t *testing.T) {
		seenTenant, seenOK = "", false
		// The delivery ctx carries a WRONG tenant (the P1 leak scenario).
		wrongCtx := ctxkeys.WithTenantID(context.Background(), wrongAmbientTenantID)
		res, _ := capSub.handler(wrongCtx, entry)
		require.Equal(t, DispositionAck, res.Disposition)
		require.True(t, seenOK)
		assert.Equal(t, entryTenantID, seenTenant,
			"consume path MUST clear the wrong ambient tenant so the entry's tenant wins "+
				"(T5.4 / P1 cross-tenant pollution guard); if this fails, SubscribeEntry stopped "+
				"clearing ambient principal before RestoreContext")
	})
}
