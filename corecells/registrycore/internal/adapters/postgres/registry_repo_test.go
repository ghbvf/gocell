package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

var unitTenant = tenant.TenantID("00000000-0000-0000-0000-000000000001")

// newRegistryFromDBTX is a test-only constructor that bypasses the Session layer,
// injecting a mock DBTX directly. Production code always goes through NewRegistry.
func newRegistryFromDBTX(db DBTX) *Registry {
	return &Registry{db: db, clk: clock.Real()}
}

// --- mock DBTX (routes QueryRow/Query by SQL substring) ---

type mockDB struct {
	execN     int64
	execErr   error
	rowsBySQL map[string]*mockRow // SQL substring → single-row result
	queryRows *mockRows
	queryErr  error
}

func (m *mockDB) Exec(context.Context, string, ...any) (int64, error) {
	if m.execErr != nil {
		return 0, m.execErr
	}
	return m.execN, nil
}

func (m *mockDB) Query(_ context.Context, _ string, _ ...any) (Rows, error) {
	if m.queryErr != nil {
		return nil, m.queryErr
	}
	if m.queryRows == nil {
		return &mockRows{}, nil
	}
	return m.queryRows, nil
}

func (m *mockDB) QueryRow(_ context.Context, sql string, _ ...any) RowScanner {
	for sub, row := range m.rowsBySQL {
		if strings.Contains(sql, sub) {
			return row
		}
	}
	return &mockRow{scanErr: pgx.ErrNoRows}
}

type mockRow struct {
	values  []any
	scanErr error
}

func (r *mockRow) Scan(dest ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}
	return assignScan(dest, r.values)
}

type mockRows struct {
	rows    [][]any
	idx     int
	scanErr error
}

func (r *mockRows) Next() bool { return r.idx < len(r.rows) }
func (r *mockRows) Close()     {}
func (r *mockRows) Err() error { return nil }
func (r *mockRows) Scan(dest ...any) error {
	if r.scanErr != nil {
		return r.scanErr
	}
	row := r.rows[r.idx]
	r.idx++
	return assignScan(dest, row)
}

func assignScan(dest, values []any) error {
	for i, v := range values {
		switch d := dest[i].(type) {
		case *string:
			*d = v.(string)
		case *int:
			*d = v.(int)
		case *time.Time:
			*d = v.(time.Time)
		}
	}
	return nil
}

func projectionValues(kind, submitter, approver, state string) []any {
	now := time.Unix(0, 0).UTC()
	return []any{kind, "" /* payload_schema */, submitter, approver, state, now, now}
}

func assertCode(t *testing.T, err error, want errcode.Code) {
	t.Helper()
	var ce *errcode.Error
	require.True(t, errors.As(err, &ce), "want *errcode.Error, got %v", err)
	assert.Equal(t, want, ce.Code)
}

func validSubmit() registry.SubmitInput {
	return registry.SubmitInput{ID: "http.foo.v1", Kind: "http", Submitter: "alice"}
}

func TestRegistry_Create_InvalidTenant(t *testing.T) {
	r := newRegistryFromDBTX(&mockDB{})
	_, err := r.Create(context.Background(), tenant.TenantID(""), validSubmit())
	assertCode(t, err, errcode.ErrValidationFailed)
}

func TestRegistry_Create_InvalidInput(t *testing.T) {
	r := newRegistryFromDBTX(&mockDB{})
	_, err := r.Create(context.Background(), unitTenant, registry.SubmitInput{ID: "", Kind: "http", Submitter: "alice"})
	assertCode(t, err, errcode.ErrValidationFailed)
}

func TestRegistry_Create_Duplicate(t *testing.T) {
	r := newRegistryFromDBTX(&mockDB{execN: 0}) // ON CONFLICT DO NOTHING → 0 rows
	_, err := r.Create(context.Background(), unitTenant, validSubmit())
	assertCode(t, err, errcode.ErrRegistrationDuplicate)
}

func TestRegistry_Create_ExecError(t *testing.T) {
	r := newRegistryFromDBTX(&mockDB{execErr: errors.New("boom")})
	_, err := r.Create(context.Background(), unitTenant, validSubmit())
	assertCode(t, err, errcode.ErrRegistrationRepoQuery)
}

func TestRegistry_Create_Success(t *testing.T) {
	db := &mockDB{
		execN:     1,
		rowsBySQL: map[string]*mockRow{"MAX(seq)": {values: []any{1}}},
	}
	r := newRegistryFromDBTX(db)
	reg, err := r.Create(context.Background(), unitTenant, validSubmit())
	require.NoError(t, err)
	assert.Equal(t, registry.StateSubmitted(), reg.State)
	assert.Equal(t, "alice", reg.Submitter)
}

func TestRegistry_Get_NotFound(t *testing.T) {
	r := newRegistryFromDBTX(&mockDB{}) // default QueryRow → pgx.ErrNoRows
	_, ok, err := r.Get(context.Background(), unitTenant, "missing")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestRegistry_Get_Found(t *testing.T) {
	db := &mockDB{rowsBySQL: map[string]*mockRow{
		"FROM contract_registrations WHERE tenant_id": {values: projectionValues("http", "alice", "", "submitted")},
	}}
	r := newRegistryFromDBTX(db)
	got, ok, err := r.Get(context.Background(), unitTenant, "http.foo.v1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, registry.StateSubmitted(), got.State)
}

func TestRegistry_Get_CorruptState(t *testing.T) {
	db := &mockDB{rowsBySQL: map[string]*mockRow{
		"FROM contract_registrations WHERE tenant_id": {values: projectionValues("http", "alice", "", "bogus")},
	}}
	r := newRegistryFromDBTX(db)
	_, _, err := r.Get(context.Background(), unitTenant, "http.foo.v1")
	assertCode(t, err, errcode.ErrRegistrationRepoQuery)
}

func TestRegistry_Transition_NotFound(t *testing.T) {
	r := newRegistryFromDBTX(&mockDB{}) // FOR UPDATE QueryRow → pgx.ErrNoRows
	_, err := r.Transition(context.Background(), unitTenant,
		registry.AdvanceInput{ID: "missing", To: registry.StateProbing(), Actor: "system"})
	assertCode(t, err, errcode.ErrRegistrationNotFound)
}

func TestRegistry_Transition_InvalidActor(t *testing.T) {
	r := newRegistryFromDBTX(&mockDB{})
	_, err := r.Transition(context.Background(), unitTenant, registry.AdvanceInput{ID: "x", To: registry.StateProbing(), Actor: ""})
	assertCode(t, err, errcode.ErrValidationFailed)
}

func TestRegistry_Transition_IllegalRejected(t *testing.T) {
	db := &mockDB{rowsBySQL: map[string]*mockRow{
		"FOR UPDATE": {values: projectionValues("http", "alice", "", "submitted")},
	}}
	r := newRegistryFromDBTX(db)
	// submitted → active is illegal (only approved → active).
	_, err := r.Transition(context.Background(), unitTenant, registry.AdvanceInput{ID: "x", To: registry.StateActive(), Actor: "admin"})
	assertCode(t, err, errcode.ErrRegistrationInvalidTransition)
}

func TestRegistry_Transition_LegalSuccess(t *testing.T) {
	db := &mockDB{
		execN: 1,
		rowsBySQL: map[string]*mockRow{
			"FOR UPDATE": {values: projectionValues("http", "alice", "", "submitted")},
			"MAX(seq)":   {values: []any{2}},
		},
	}
	r := newRegistryFromDBTX(db)
	got, err := r.Transition(context.Background(), unitTenant, registry.AdvanceInput{ID: "x", To: registry.StateProbing(), Actor: "system"})
	require.NoError(t, err)
	assert.Equal(t, registry.StateProbing(), got.State)
}

func TestRegistry_List_Success(t *testing.T) {
	db := &mockDB{queryRows: &mockRows{rows: [][]any{
		append([]any{"a"}, projectionValues("http", "alice", "", "submitted")...),
		append([]any{"b"}, projectionValues("event", "bob", "admin", "approved")...),
	}}}
	r := newRegistryFromDBTX(db)
	out, err := r.List(context.Background(), unitTenant, "", 10)
	require.NoError(t, err)
	require.Len(t, out, 2)
	assert.Equal(t, "a", out[0].ID)
	assert.Equal(t, registry.StateApproved(), out[1].State)
	assert.Equal(t, "admin", out[1].Approver)
}

func TestRegistry_List_QueryError(t *testing.T) {
	r := newRegistryFromDBTX(&mockDB{queryErr: errors.New("boom")})
	_, err := r.List(context.Background(), unitTenant, "", 10)
	assertCode(t, err, errcode.ErrRegistrationRepoQuery)
}

func TestRegistry_History_Success(t *testing.T) {
	now := time.Unix(0, 0).UTC()
	db := &mockDB{queryRows: &mockRows{rows: [][]any{
		{1, "", "submitted", "alice", "", now},
		{2, "submitted", "probing", "system", "", now},
	}}}
	r := newRegistryFromDBTX(db)
	evs, err := r.History(context.Background(), unitTenant, "x")
	require.NoError(t, err)
	require.Len(t, evs, 2)
	assert.True(t, evs[0].From.IsZero())
	assert.Equal(t, registry.StateProbing(), evs[1].To)
	assert.Equal(t, 2, evs[1].Seq)
}

func TestRegistry_Create_AppendEventSeqError(t *testing.T) {
	db := &mockDB{
		execN:     1, // projection insert succeeds
		rowsBySQL: map[string]*mockRow{"MAX(seq)": {scanErr: errors.New("boom")}},
	}
	r := newRegistryFromDBTX(db)
	_, err := r.Create(context.Background(), unitTenant, validSubmit())
	assertCode(t, err, errcode.ErrRegistrationRepoQuery)
}

func TestRegistry_Transition_UpdateExecError(t *testing.T) {
	db := &mockDB{
		execErr:   errors.New("boom"), // the UPDATE fails
		rowsBySQL: map[string]*mockRow{"FOR UPDATE": {values: projectionValues("http", "alice", "", "submitted")}},
	}
	r := newRegistryFromDBTX(db)
	_, err := r.Transition(context.Background(), unitTenant, registry.AdvanceInput{ID: "x", To: registry.StateProbing(), Actor: "s"})
	assertCode(t, err, errcode.ErrRegistrationRepoQuery)
}

func TestRegistry_History_ScanError(t *testing.T) {
	db := &mockDB{queryRows: &mockRows{rows: [][]any{{0, "", "", "", "", time.Time{}}}, scanErr: errors.New("boom")}}
	r := newRegistryFromDBTX(db)
	_, err := r.History(context.Background(), unitTenant, "x")
	assertCode(t, err, errcode.ErrRegistrationRepoQuery)
}

func TestRegistry_List_CorruptState(t *testing.T) {
	db := &mockDB{queryRows: &mockRows{rows: [][]any{
		append([]any{"a"}, projectionValues("http", "alice", "", "bogus")...),
	}}}
	r := newRegistryFromDBTX(db)
	_, err := r.List(context.Background(), unitTenant, "", 10)
	assertCode(t, err, errcode.ErrRegistrationRepoQuery)
}

func TestRegistry_AllMethods_InvalidTenant(t *testing.T) {
	r := newRegistryFromDBTX(&mockDB{})
	zero := tenant.TenantID("")
	_, _, gErr := r.Get(context.Background(), zero, "x")
	assertCode(t, gErr, errcode.ErrValidationFailed)
	_, lErr := r.List(context.Background(), zero, "", 10)
	assertCode(t, lErr, errcode.ErrValidationFailed)
	_, hErr := r.History(context.Background(), zero, "x")
	assertCode(t, hErr, errcode.ErrValidationFailed)
	_, tErr := r.Transition(context.Background(), zero, registry.AdvanceInput{ID: "x", To: registry.StateProbing(), Actor: "s"})
	assertCode(t, tErr, errcode.ErrValidationFailed)
}
