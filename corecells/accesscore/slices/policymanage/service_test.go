package policymanage

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/corecells/internal/testoutbox"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/runtime/auth"
)

const testSvcTenantStr = "10000000-0000-0000-0000-000000000001"

// testSvcAdminCtx returns a context with an admin principal and valid tenant.
func testSvcAdminCtx() context.Context {
	return ctxkeys.WithTenantID(auth.TestContext("svc-admin", []string{"admin"}), testSvcTenantStr)
}

// noopTxRunner executes fn directly without a real transaction.
type noopTxRunner struct{}

func (n *noopTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	ctx, drainAfterCommit := persistence.WithAfterCommitRegistry(ctx)
	mark := persistence.AfterCommitMark(ctx)
	if err := fn(ctx); err != nil {
		persistence.TruncateAfterCommitTo(ctx, mark)
		return err
	}
	if drainAfterCommit {
		persistence.RunAfterCommitHooks(ctx)
	}
	return nil
}

var _ persistence.TxRunner = (*noopTxRunner)(nil)

// recordingWriter records outbox entries and can simulate failures.
type recordingWriter struct {
	Entries []outbox.Entry
	Err     error
}

func (w *recordingWriter) Write(_ context.Context, entry outbox.Entry) error {
	if w.Err != nil {
		return w.Err
	}
	w.Entries = append(w.Entries, entry)
	return nil
}

var _ outbox.Writer = (*recordingWriter)(nil)

// newDurableTestService returns a Service backed by in-memory repo, recording
// writer, and noop TxRunner.
func newDurableTestService(t testing.TB) (*Service, *recordingWriter) {
	t.Helper()
	repo := mem.NewPolicyRepository()
	writer := &recordingWriter{}
	svc, err := NewService(clock.Real(), repo, slog.Default(),
		WithEmitter(outbox.WrapEmitterForCell(testoutbox.MustEmitter(t, writer))),
		WithTxManager(persistence.WrapForCell(&noopTxRunner{})))
	require.NoError(t, err)
	return svc, writer
}

// minimalRules returns one valid allow rule for test policy payloads.
func minimalRules() []abac.Rule {
	return []abac.Rule{
		{
			ID:     "r1",
			Name:   "Allow all",
			Effect: authz.EffectAllow,
		},
	}
}

// --- Create tests ---

func TestService_Create_HappyPath(t *testing.T) {
	svc, writer := newDurableTestService(t)

	p, err := svc.Create(testSvcAdminCtx(), CreateInput{
		Name:  "TestPolicy",
		Rules: minimalRules(),
	})
	require.NoError(t, err)
	assert.NotEmpty(t, p.ID)
	assert.Equal(t, "TestPolicy", p.Name)
	assert.Equal(t, 1, p.Version)
	assert.Equal(t, tenant.TenantID(testSvcTenantStr), p.TenantID)

	// Exactly one outbox event, action=created, correct policyId+version.
	require.Len(t, writer.Entries, 1, "Create must emit exactly one outbox entry")
	entry := writer.Entries[0]
	assert.Equal(t, TopicPolicyUpdated, entry.EventType())
	payload := entry.Payload()
	var pay dto.PolicyUpdated
	require.NoError(t, unmarshalPayload(payload, &pay))
	assert.Equal(t, p.ID, pay.PolicyID)
	assert.Equal(t, 1, pay.Version)
	assert.Equal(t, dto.PolicyActionCreated, pay.Action)
	assert.Equal(t, "svc-admin", pay.ActorID)
}

func TestService_Create_NoAuth(t *testing.T) {
	svc, _ := newDurableTestService(t)

	ctx := ctxkeys.WithTenantID(context.Background(), testSvcTenantStr)
	_, err := svc.Create(ctx, CreateInput{Name: "X", Rules: minimalRules()})
	require.Error(t, err)
	var ce *errcode.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, errcode.KindUnauthenticated, ce.Kind)
}

func TestService_Create_InvalidInput_NoRules(t *testing.T) {
	svc, _ := newDurableTestService(t)

	_, err := svc.Create(testSvcAdminCtx(), CreateInput{Name: "P", Rules: nil})
	require.Error(t, err)
	var ce *errcode.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, errcode.KindInvalid, ce.Kind)
}

func TestService_Create_EmitterFailureRollsBack(t *testing.T) {
	repo := mem.NewPolicyRepository()
	failWriter := &recordingWriter{Err: errors.New("outbox down")}
	svc, err := NewService(clock.Real(), repo, slog.Default(),
		WithEmitter(outbox.WrapEmitterForCell(testoutbox.MustEmitter(t, failWriter))),
		WithTxManager(persistence.WrapForCell(&noopTxRunner{})))
	require.NoError(t, err)

	_, createErr := svc.Create(testSvcAdminCtx(), CreateInput{
		Name:  "ShouldNotPersist",
		Rules: minimalRules(),
	})
	require.Error(t, createErr)
	assert.ErrorIs(t, createErr, failWriter.Err)

	// The noopTxRunner does not give real DB rollback, but the WriterEmitter
	// error should propagate out of runInTx before any domain state is committed.
	// For the L2 rollback proof on PG, see service_integration_test.go.
}

// --- Update tests ---

func TestService_Update_HappyPath(t *testing.T) {
	svc, writer := newDurableTestService(t)

	p, err := svc.Create(testSvcAdminCtx(), CreateInput{Name: "P1", Rules: minimalRules()})
	require.NoError(t, err)
	writer.Entries = nil

	updated, err := svc.Update(testSvcAdminCtx(), UpdateInput{
		ID:              p.ID,
		Name:            "P1-updated",
		Rules:           minimalRules(),
		ExpectedVersion: 1,
	})
	require.NoError(t, err)
	assert.Equal(t, "P1-updated", updated.Name)
	assert.Equal(t, 2, updated.Version)

	require.Len(t, writer.Entries, 1)
	var pay dto.PolicyUpdated
	require.NoError(t, unmarshalPayload(writer.Entries[0].Payload(), &pay))
	assert.Equal(t, dto.PolicyActionUpdated, pay.Action)
	assert.Equal(t, 2, pay.Version)
	assert.Equal(t, p.ID, pay.PolicyID)
}

func TestService_Update_VersionConflict(t *testing.T) {
	svc, _ := newDurableTestService(t)

	p, err := svc.Create(testSvcAdminCtx(), CreateInput{Name: "P", Rules: minimalRules()})
	require.NoError(t, err)

	_, err = svc.Update(testSvcAdminCtx(), UpdateInput{
		ID: p.ID, Name: "X", Rules: minimalRules(), ExpectedVersion: 99,
	})
	require.Error(t, err)
	var ce *errcode.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, errcode.KindConflict, ce.Kind)
}

func TestService_Update_NotFound(t *testing.T) {
	svc, _ := newDurableTestService(t)

	_, err := svc.Update(testSvcAdminCtx(), UpdateInput{
		ID: "pol-nonexistent", Name: "X", Rules: minimalRules(), ExpectedVersion: 1,
	})
	require.Error(t, err)
	var ce *errcode.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, errcode.KindNotFound, ce.Kind)
}

// --- Delete tests ---

func TestService_Delete_HappyPath(t *testing.T) {
	svc, writer := newDurableTestService(t)

	p, err := svc.Create(testSvcAdminCtx(), CreateInput{Name: "ToDelete", Rules: minimalRules()})
	require.NoError(t, err)
	writer.Entries = nil

	deleted, err := svc.Delete(testSvcAdminCtx(), p.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, p.ID, deleted.ID)

	require.Len(t, writer.Entries, 1)
	var pay dto.PolicyUpdated
	require.NoError(t, unmarshalPayload(writer.Entries[0].Payload(), &pay))
	assert.Equal(t, dto.PolicyActionDeleted, pay.Action)
}

func TestService_Delete_VersionConflict(t *testing.T) {
	svc, _ := newDurableTestService(t)
	p, err := svc.Create(testSvcAdminCtx(), CreateInput{Name: "D", Rules: minimalRules()})
	require.NoError(t, err)

	_, err = svc.Delete(testSvcAdminCtx(), p.ID, 99)
	require.Error(t, err)
	var ce *errcode.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, errcode.KindConflict, ce.Kind)
}

func TestService_Delete_NotFound(t *testing.T) {
	svc, _ := newDurableTestService(t)

	_, err := svc.Delete(testSvcAdminCtx(), "pol-ghost", 1)
	require.Error(t, err)
	var ce *errcode.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, errcode.KindNotFound, ce.Kind)
}

// --- Get tests ---

func TestService_Get_HappyPath(t *testing.T) {
	svc, _ := newDurableTestService(t)
	p, err := svc.Create(testSvcAdminCtx(), CreateInput{Name: "GetMe", Rules: minimalRules()})
	require.NoError(t, err)

	got, err := svc.Get(testSvcAdminCtx(), p.ID)
	require.NoError(t, err)
	assert.Equal(t, p.ID, got.ID)
	assert.Equal(t, "GetMe", got.Name)
}

func TestService_Get_NotFound(t *testing.T) {
	svc, _ := newDurableTestService(t)

	_, err := svc.Get(testSvcAdminCtx(), "pol-ghost")
	require.Error(t, err)
	var ce *errcode.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, errcode.KindNotFound, ce.Kind)
}

// --- List tests ---

func TestService_List_Empty(t *testing.T) {
	svc, _ := newDurableTestService(t)

	result, err := svc.List(testSvcAdminCtx(), "", 10)
	require.NoError(t, err)
	assert.Empty(t, result.Items)
	assert.False(t, result.HasMore)
	assert.Empty(t, result.NextCursor)
}

func TestService_List_Pagination(t *testing.T) {
	svc, _ := newDurableTestService(t)
	ctx := testSvcAdminCtx()

	// Create 3 policies.
	for i := range 3 {
		_, err := svc.Create(ctx, CreateInput{
			Name:  "P" + string(rune('0'+i)),
			Rules: minimalRules(),
		})
		require.NoError(t, err)
	}

	// First page of 2.
	page1, err := svc.List(ctx, "", 2)
	require.NoError(t, err)
	assert.Len(t, page1.Items, 2)
	assert.True(t, page1.HasMore)
	assert.NotEmpty(t, page1.NextCursor)

	// Second page.
	page2, err := svc.List(ctx, page1.NextCursor, 2)
	require.NoError(t, err)
	assert.Len(t, page2.Items, 1)
	assert.False(t, page2.HasMore)
}

func TestService_List_SortedByID(t *testing.T) {
	svc, _ := newDurableTestService(t)
	ctx := testSvcAdminCtx()

	for range 5 {
		_, err := svc.Create(ctx, CreateInput{Name: "X", Rules: minimalRules()})
		require.NoError(t, err)
	}

	result, err := svc.List(ctx, "", 10)
	require.NoError(t, err)
	require.Len(t, result.Items, 5)

	// Verify items are sorted by ID ascending.
	for i := 1; i < len(result.Items); i++ {
		assert.LessOrEqual(t, result.Items[i-1].ID, result.Items[i].ID, "list must be sorted by ID asc")
	}
}

// --- NewService error tests ---

func TestNewService_MissingTxManager(t *testing.T) {
	repo := mem.NewPolicyRepository()
	_, err := NewService(clock.Real(), repo, slog.Default())
	require.Error(t, err)
}

// --- helper ---

func unmarshalPayload(payload []byte, v any) error {
	return json.Unmarshal(payload, v)
}
