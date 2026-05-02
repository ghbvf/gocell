package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/auditcore/internal/domain"
	"github.com/ghbvf/gocell/cells/auditcore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/ctxcancel"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

// TestAuditRepository_CtxCancel_AllIOBoundaries locks the PR271-FU3 contract:
// every IO error returned from auditcore postgres adapter must run through
// ctxcancel.Wrap before falling through to the generic ErrAuditRepoQuery
// mapping.
//
// PR275 P2-3 split: the expected code is branch-aware:
//   - context.Canceled wrappers         → ErrClientCanceled (HTTP 499, slog.Warn)
//   - context.DeadlineExceeded wrappers → ErrServerTimeout  (HTTP 504, slog.Error)
//
// Coverage matrix: every Append / GetRange / Query IO error site × every
// {Canceled, DeadlineExceeded} variant. Without this dual mapping a real
// server-side timeout would either pollute the 5xx error rate (old
// "everything → 499" design) or be invisible to retry-on-504 SDKs.
func TestAuditRepository_CtxCancel_AllIOBoundaries(t *testing.T) {
	tests := []struct {
		name string
		// fixture installs the ctx-cancel error on the appropriate mock surface.
		fixture func(*mockDB, error)
		// invoke runs the repo method that owns the IO boundary under test.
		invoke func(*AuditRepository) error
	}{
		{
			name: "Append exec error",
			fixture: func(db *mockDB, ce error) {
				db.execErr = ce
			},
			invoke: func(r *AuditRepository) error {
				return r.Append(context.Background(), &domain.AuditEntry{
					ID:        "ae-1",
					EventType: "test",
					Timestamp: time.Now(),
				})
			},
		},
		{
			name: "GetRange query error",
			fixture: func(db *mockDB, ce error) {
				db.queryErr = ce
			},
			invoke: func(r *AuditRepository) error {
				_, err := r.GetRange(context.Background(), 0, 10)
				return err
			},
		},
		{
			name: "Query (filtered) query error",
			fixture: func(db *mockDB, ce error) {
				db.queryErr = ce
			},
			invoke: func(r *AuditRepository) error {
				_, err := r.Query(context.Background(),
					ports.AuditFilters{EventType: "login"},
					query.ListParams{
						Limit: 10,
						Sort: []query.SortColumn{
							{Name: "timestamp", Direction: query.SortDESC},
							{Name: "id", Direction: query.SortASC},
						},
					},
				)
				return err
			},
		},
		{
			name: "scan error during GetRange iteration",
			fixture: func(db *mockDB, ce error) {
				db.queryRows = &mockRowSet{scanErr: ce}
			},
			invoke: func(r *AuditRepository) error {
				_, err := r.GetRange(context.Background(), 0, 10)
				return err
			},
		},
		{
			name: "rows.Err() during GetRange iteration",
			fixture: func(db *mockDB, ce error) {
				db.queryRows = &mockRowSet{iterErr: ce}
			},
			invoke: func(r *AuditRepository) error {
				_, err := r.GetRange(context.Background(), 0, 10)
				return err
			},
		},
		// Query() also flows through scanAuditEntries — the same scan / iter
		// IO sites must surface ctx-cancel through ErrClientCanceled when
		// reached via the filtered/keyset path, not just the GetRange path.
		{
			name: "scan error during Query iteration",
			fixture: func(db *mockDB, ce error) {
				db.queryRows = &mockRowSet{scanErr: ce}
			},
			invoke: func(r *AuditRepository) error {
				_, err := r.Query(context.Background(),
					ports.AuditFilters{},
					query.ListParams{
						Limit: 10,
						Sort: []query.SortColumn{
							{Name: "timestamp", Direction: query.SortDESC},
							{Name: "id", Direction: query.SortASC},
						},
					},
				)
				return err
			},
		},
		{
			name: "rows.Err() during Query iteration",
			fixture: func(db *mockDB, ce error) {
				db.queryRows = &mockRowSet{iterErr: ce}
			},
			invoke: func(r *AuditRepository) error {
				_, err := r.Query(context.Background(),
					ports.AuditFilters{},
					query.ListParams{
						Limit: 10,
						Sort: []query.SortColumn{
							{Name: "timestamp", Direction: query.SortDESC},
							{Name: "id", Direction: query.SortASC},
						},
					},
				)
				return err
			},
		},
	}

	causes := []struct {
		name  string
		cause error
	}{
		{name: "context.Canceled", cause: context.Canceled},
		{name: "context.DeadlineExceeded", cause: context.DeadlineExceeded},
		{name: "wrapped Canceled", cause: fmt.Errorf("pgx: %w", context.Canceled)},
		{name: "wrapped DeadlineExceeded", cause: fmt.Errorf("pgx: %w", context.DeadlineExceeded)},
	}

	for _, tt := range tests {
		for _, c := range causes {
			name := tt.name + " / " + c.name
			t.Run(name, func(t *testing.T) {
				db := &mockDB{}
				tt.fixture(db, c.cause)
				repo := NewAuditRepository(db, clock.Real())

				err := tt.invoke(repo)
				require.Error(t, err)

				var ec *errcode.Error
				require.ErrorAs(t, err, &ec,
					"ctx-cancel must surface as *errcode.Error, not raw context.* sentinel")

				expectedCode := errcode.ErrClientCanceled
				expectedReason := ctxcancel.ReasonCanceled
				if errors.Is(c.cause, context.DeadlineExceeded) {
					expectedCode = errcode.ErrServerTimeout
					expectedReason = ctxcancel.ReasonDeadlineExceeded
				}
				assert.Equal(t, expectedCode, ec.Code,
					"Canceled→ErrClientCanceled (499) / DeadlineExceeded→ErrServerTimeout (504); "+
						"never ErrAuditRepoQuery (500) — that would pollute the 5xx error rate "+
						"with client-direction noise or hide real timeouts")
				assert.True(t, ctxcancel.Detect(err),
					"ctxcancel.Detect must traverse the Cause chain back to context.* sentinel")
				// PR275 P2-2: pin Details["reason"] alongside Code so a regression
				// in reasonOf() (e.g. always returning the same constant) cannot
				// pass the test by status alone.
				assert.Equal(t, expectedReason, ctxcancel.ReasonFromDetails(ec.Details),
					"Details[reason] must mirror the originating ctx error variant — "+
						"the canonical low-cardinality observation field for dashboards")
			})
		}
	}
}

// TestAuditRepository_CtxCancel_NonCancelStillInfra ensures the FU3 wiring
// does NOT swallow real infra errors. A plain DB error must still surface as
// ErrAuditRepoQuery → HTTP 500, not be misclassified as ctx-cancel.
func TestAuditRepository_CtxCancel_NonCancelStillInfra(t *testing.T) {
	db := &mockDB{execErr: errors.New("connection refused")}
	repo := NewAuditRepository(db, clock.Real())

	err := repo.Append(context.Background(), &domain.AuditEntry{ID: "ae-x"})
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuditRepoQuery, ec.Code,
		"non-cancel infra errors must keep ErrAuditRepoQuery code")
	assert.False(t, ctxcancel.Detect(err),
		"plain infra error must not be detected as ctx-cancel")
}
