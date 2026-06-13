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
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/runtime/auth"
)

const testSvcTenantStr = "10000000-0000-0000-0000-000000000001"

// testCursorCodec is the demo-key cursor codec shared by all policymanage tests
// (same 32-byte demo key the cell installs in non-durable mode). Built once;
// a malformed key is a test-setup bug, so it panics rather than taking *testing.T.
var testCursorCodec = func() *query.CursorCodec {
	c, err := query.NewCursorCodec([]byte("gocell-demo-ACCESS-CORE-key-32!!"))
	if err != nil {
		panic("policymanage_test: cursor codec: " + err.Error())
	}
	return c
}()

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
	svc, err := NewService(clock.Real(), repo, testCursorCodec, slog.Default(), query.RunModeProd,
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
	assert.NotEmpty(t, entry.ID(), "outbox entry must have a non-empty ID")
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
	svc, err := NewService(clock.Real(), repo, testCursorCodec, slog.Default(), query.RunModeProd,
		WithEmitter(outbox.WrapEmitterForCell(testoutbox.MustEmitter(t, failWriter))),
		WithTxManager(persistence.WrapForCell(&noopTxRunner{})))
	require.NoError(t, err)

	_, createErr := svc.Create(testSvcAdminCtx(), CreateInput{
		Name:  "ShouldNotPersist",
		Rules: minimalRules(),
	})
	require.Error(t, createErr)
	// emitPolicyUpdated failure propagates out of runInTx; the in-memory txRunner
	// cannot roll back the repo write (that is the L2 PG guarantee, proven in
	// service_integration_test.go), but the error does surface to the caller.
	assert.ErrorIs(t, createErr, failWriter.Err)
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
	assert.NotEmpty(t, writer.Entries[0].ID(), "outbox entry must have a non-empty ID")
	var pay dto.PolicyUpdated
	require.NoError(t, unmarshalPayload(writer.Entries[0].Payload(), &pay))
	assert.Equal(t, dto.PolicyActionUpdated, pay.Action)
	assert.Equal(t, 2, pay.Version)
	assert.Equal(t, p.ID, pay.PolicyID)
}

func TestService_Update_NoAuth(t *testing.T) {
	svc, _ := newDurableTestService(t)

	// Create with admin ctx so we have a valid policy ID.
	p, err := svc.Create(testSvcAdminCtx(), CreateInput{Name: "P", Rules: minimalRules()})
	require.NoError(t, err)

	// Update without an auth principal in context.
	ctx := ctxkeys.WithTenantID(context.Background(), testSvcTenantStr)
	_, err = svc.Update(ctx, UpdateInput{
		ID: p.ID, Name: "X", Rules: minimalRules(), ExpectedVersion: 1,
	})
	require.Error(t, err)
	var ce *errcode.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, errcode.KindUnauthenticated, ce.Kind)
}

func TestService_Update_EmitterFailureRollsBack(t *testing.T) {
	repo := mem.NewPolicyRepository()

	// Create with a healthy emitter first.
	goodWriter := &recordingWriter{}
	svc, err := NewService(clock.Real(), repo, testCursorCodec, slog.Default(), query.RunModeProd,
		WithEmitter(outbox.WrapEmitterForCell(testoutbox.MustEmitter(t, goodWriter))),
		WithTxManager(persistence.WrapForCell(&noopTxRunner{})))
	require.NoError(t, err)
	p, err := svc.Create(testSvcAdminCtx(), CreateInput{Name: "P", Rules: minimalRules()})
	require.NoError(t, err)

	// Rebuild service with a failing emitter for the Update call.
	failWriter := &recordingWriter{Err: errors.New("outbox down")}
	failSvc, err := NewService(clock.Real(), repo, testCursorCodec, slog.Default(), query.RunModeProd,
		WithEmitter(outbox.WrapEmitterForCell(testoutbox.MustEmitter(t, failWriter))),
		WithTxManager(persistence.WrapForCell(&noopTxRunner{})))
	require.NoError(t, err)

	_, updateErr := failSvc.Update(testSvcAdminCtx(), UpdateInput{
		ID: p.ID, Name: "ShouldFail", Rules: minimalRules(), ExpectedVersion: 1,
	})
	require.Error(t, updateErr)
	// emitPolicyUpdated failure propagates out of runInTx.
	assert.ErrorIs(t, updateErr, failWriter.Err)
}

// TestService_Update_InvalidInput_EqAttrStrayValues asserts that Update validates
// the patch before entering the transaction: an eq_attr condition with stray
// Values is rejected with KindInvalid (422) without hitting the repo.
// Symmetry with TestService_Create_InvalidInput_NoRules which validates on the
// Create path.
func TestService_Update_InvalidInput_EqAttrStrayValues(t *testing.T) {
	svc, _ := newDurableTestService(t)

	// Create a valid policy first.
	p, err := svc.Create(testSvcAdminCtx(), CreateInput{Name: "P", Rules: minimalRules()})
	require.NoError(t, err)

	invalidRules := []abac.Rule{
		{
			ID:     "r1",
			Name:   "Bad cross-attr",
			Effect: authz.EffectAllow,
			Conditions: []abac.Condition{
				{
					Source:    abac.SourceSubject,
					Key:       "sub",
					Operator:  abac.OpEqualsAttr,
					RHSSource: abac.SourceResource,
					RHSKey:    "id",
					Values:    []string{"stray-value"}, // stray Values on eq_attr: invalid
				},
			},
		},
	}

	_, err = svc.Update(testSvcAdminCtx(), UpdateInput{
		ID: p.ID, Name: "BadUpdate", Rules: invalidRules, ExpectedVersion: p.Version,
	})
	require.Error(t, err)
	var ce *errcode.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, errcode.KindInvalid, ce.Kind, "Update must reject invalid policy before hitting repo")
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
	assert.NotEmpty(t, writer.Entries[0].ID(), "outbox entry must have a non-empty ID")
	var pay dto.PolicyUpdated
	require.NoError(t, unmarshalPayload(writer.Entries[0].Payload(), &pay))
	assert.Equal(t, dto.PolicyActionDeleted, pay.Action)
}

func TestService_Delete_NoAuth(t *testing.T) {
	svc, _ := newDurableTestService(t)

	p, err := svc.Create(testSvcAdminCtx(), CreateInput{Name: "P", Rules: minimalRules()})
	require.NoError(t, err)

	ctx := ctxkeys.WithTenantID(context.Background(), testSvcTenantStr)
	_, err = svc.Delete(ctx, p.ID, 1)
	require.Error(t, err)
	var ce *errcode.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, errcode.KindUnauthenticated, ce.Kind)
}

func TestService_Delete_EmitterFailureRollsBack(t *testing.T) {
	repo := mem.NewPolicyRepository()

	goodWriter := &recordingWriter{}
	svc, err := NewService(clock.Real(), repo, testCursorCodec, slog.Default(), query.RunModeProd,
		WithEmitter(outbox.WrapEmitterForCell(testoutbox.MustEmitter(t, goodWriter))),
		WithTxManager(persistence.WrapForCell(&noopTxRunner{})))
	require.NoError(t, err)
	p, err := svc.Create(testSvcAdminCtx(), CreateInput{Name: "P", Rules: minimalRules()})
	require.NoError(t, err)

	failWriter := &recordingWriter{Err: errors.New("outbox down")}
	failSvc, err := NewService(clock.Real(), repo, testCursorCodec, slog.Default(), query.RunModeProd,
		WithEmitter(outbox.WrapEmitterForCell(testoutbox.MustEmitter(t, failWriter))),
		WithTxManager(persistence.WrapForCell(&noopTxRunner{})))
	require.NoError(t, err)

	_, deleteErr := failSvc.Delete(testSvcAdminCtx(), p.ID, 1)
	require.Error(t, deleteErr)
	// emitPolicyUpdated failure propagates out of runInTx.
	assert.ErrorIs(t, deleteErr, failWriter.Err)
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

	result, err := svc.List(testSvcAdminCtx(), query.PageParams{Limit: 10})
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
	page1, err := svc.List(ctx, query.PageParams{Limit: 2})
	require.NoError(t, err)
	assert.Len(t, page1.Items, 2)
	assert.True(t, page1.HasMore)
	assert.NotEmpty(t, page1.NextCursor)

	// Second page.
	page2, err := svc.List(ctx, query.PageParams{Cursor: page1.NextCursor, Limit: 2})
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

	result, err := svc.List(ctx, query.PageParams{Limit: 10})
	require.NoError(t, err)
	require.Len(t, result.Items, 5)

	// Verify items are sorted by ID ascending.
	for i := 1; i < len(result.Items); i++ {
		assert.LessOrEqual(t, result.Items[i-1].ID, result.Items[i].ID, "list must be sorted by ID asc")
	}
}

func TestService_List_LimitZeroDefaultsToPageSize(t *testing.T) {
	svc, _ := newDurableTestService(t)
	ctx := testSvcAdminCtx()

	// Create 3 policies; limit=0 should be normalised to DefaultPageSize (50).
	for i := range 3 {
		_, err := svc.Create(ctx, CreateInput{
			Name:  "P" + string(rune('0'+i)),
			Rules: minimalRules(),
		})
		require.NoError(t, err)
	}

	result, err := svc.List(ctx, query.PageParams{Limit: 0})
	require.NoError(t, err)
	// All 3 items fit within DefaultPageSize — no truncation expected.
	assert.Len(t, result.Items, 3)
	assert.False(t, result.HasMore)
}

func TestService_List_LimitAboveMaxClamped(t *testing.T) {
	svc, _ := newDurableTestService(t)
	ctx := testSvcAdminCtx()

	// Create 3 policies; requesting limit=600 must be clamped to ≤500.
	for i := range 3 {
		_, err := svc.Create(ctx, CreateInput{
			Name:  "Q" + string(rune('0'+i)),
			Rules: minimalRules(),
		})
		require.NoError(t, err)
	}

	result, err := svc.List(ctx, query.PageParams{Limit: 600})
	require.NoError(t, err)
	// All 3 items fit within clamped limit.
	assert.Len(t, result.Items, 3)
	assert.False(t, result.HasMore)
}

// --- NewService error tests ---

func TestNewService_MissingTxManager(t *testing.T) {
	repo := mem.NewPolicyRepository()
	_, err := NewService(clock.Real(), repo, testCursorCodec, slog.Default(), query.RunModeProd)
	require.Error(t, err)
}

func TestNewService_MissingCodec(t *testing.T) {
	repo := mem.NewPolicyRepository()
	_, err := NewService(clock.Real(), repo, nil, slog.Default(), query.RunModeProd,
		WithTxManager(persistence.WrapForCell(&noopTxRunner{})))
	require.Error(t, err)
	var ce *errcode.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, errcode.ErrCellMissingCodec, ce.Code)
}

// --- F1: scoped-tx read coverage ---

// recordingTxRunner records RunInTx invocations and the tenant scope carried on
// the context, to prove the read paths run inside a tenant-scoped transaction.
type recordingTxRunner struct {
	calls  int
	scopes []string
}

func (r *recordingTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	r.calls++
	if tid, ok := tenant.ScopeFromContext(ctx); ok {
		r.scopes = append(r.scopes, tid.String())
	}
	return fn(ctx)
}

var _ persistence.TxRunner = (*recordingTxRunner)(nil)

// TestService_Reads_RunInScopedTx proves Get and List route their repo reads
// through scopedtx.Do — RunInTx with the caller's tenant scope on the context
// (F1). Under PG FORCE RLS a bare-pool read returns 0 rows for the restricted
// app-serving role unless the tx sets app.tenant_id; wrapping every read in the
// scoped tx is what installs that GUC. The write path (Create) already runs in a
// tx via runInTx, but without the explicit tenant.WithScope the read path needs —
// so only the two reads contribute scoped entries here.
func TestService_Reads_RunInScopedTx(t *testing.T) {
	repo := mem.NewPolicyRepository()
	spy := &recordingTxRunner{}
	svc, err := NewService(clock.Real(), repo, testCursorCodec, slog.Default(), query.RunModeProd,
		WithEmitter(outbox.WrapEmitterForCell(testoutbox.MustEmitter(t, &recordingWriter{}))),
		WithTxManager(persistence.WrapForCell(spy)))
	require.NoError(t, err)

	ctx := testSvcAdminCtx()
	p, err := svc.Create(ctx, CreateInput{Name: "P", Rules: minimalRules()})
	require.NoError(t, err)
	callsAfterCreate := spy.calls

	_, err = svc.Get(ctx, p.ID)
	require.NoError(t, err)
	_, err = svc.List(ctx, query.PageParams{Limit: 10})
	require.NoError(t, err)

	assert.Equal(t, callsAfterCreate+2, spy.calls, "Get and List must each run inside a tx")
	require.Len(t, spy.scopes, 2, "both reads must carry an explicit tenant scope")
	for _, s := range spy.scopes {
		assert.Equal(t, testSvcTenantStr, s, "read tx must be scoped to the caller's tenant")
	}
}

// --- helper ---

func unmarshalPayload(payload []byte, v any) error {
	return json.Unmarshal(payload, v)
}
