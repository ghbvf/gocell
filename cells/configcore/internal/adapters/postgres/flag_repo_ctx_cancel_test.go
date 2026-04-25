package postgres

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFlagRepo_CtxCanceled_ReturnsClientCanceled covers the ctx-cancel
// branch added in PR-A50+A51 — every IO surface in flag_repo.go must
// translate context.Canceled / context.DeadlineExceeded into
// ErrClientCanceled (HTTP 499 + slog.Warn) instead of the prior
// ErrFlagRepoQuery (500). Verifies the wrapCtxCancel hook on
// scanFlagOrMapError + wrapNonScanQueryErr.
func TestFlagRepo_CtxCanceled_ReturnsClientCanceled(t *testing.T) {
	tests := []struct {
		name    string
		scanErr error
	}{
		{"ctx canceled", context.Canceled},
		{"ctx deadline exceeded", context.DeadlineExceeded},
	}
	assertCtxCancelErr := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err)
		require.True(t, errcode.IsExpected4xx(err),
			"flag-repo ctx cancel must route through log4xx → slog.Warn")
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrClientCanceled, ec.Code)
		assert.Contains(t, ec.InternalMessage, "ctx canceled",
			"must hit wrapCtxCancel path")
	}
	for _, tc := range tests {
		tc := tc
		t.Run("GetByKey/"+tc.name, func(t *testing.T) {
			db := &mockDB{queryRowResult: &mockRow{scanErr: tc.scanErr}}
			repo := newFlagRepositoryFromDBTX(db)
			_, err := repo.GetByKey(context.Background(), "dark-mode")
			assertCtxCancelErr(t, err)
		})
		t.Run("Update/"+tc.name, func(t *testing.T) {
			db := &mockDB{queryRowResult: &mockRow{scanErr: tc.scanErr}}
			repo := newFlagRepositoryFromDBTX(db)
			_, err := repo.Update(context.Background(), "dark-mode", true, 100, "desc")
			assertCtxCancelErr(t, err)
		})
		t.Run("Delete/"+tc.name, func(t *testing.T) {
			db := &mockDB{queryRowResult: &mockRow{scanErr: tc.scanErr}}
			repo := newFlagRepositoryFromDBTX(db)
			_, err := repo.Delete(context.Background(), "dark-mode")
			assertCtxCancelErr(t, err)
		})
		t.Run("Toggle/"+tc.name, func(t *testing.T) {
			db := &mockDB{queryRowResult: &mockRow{scanErr: tc.scanErr}}
			repo := newFlagRepositoryFromDBTX(db)
			_, err := repo.Toggle(context.Background(), "dark-mode", true)
			assertCtxCancelErr(t, err)
		})
		t.Run("Create/"+tc.name, func(t *testing.T) {
			db := &mockDB{execErr: tc.scanErr}
			repo := newFlagRepositoryFromDBTX(db)
			err := repo.Create(context.Background(), &domain.FeatureFlag{ID: "flg-1", Key: "dark-mode"})
			assertCtxCancelErr(t, err)
		})
		t.Run("List/Query/"+tc.name, func(t *testing.T) {
			db := &mockDB{queryErr: tc.scanErr}
			repo := newFlagRepositoryFromDBTX(db)
			_, err := repo.List(context.Background(), query.ListParams{
				Sort: []query.SortColumn{{Name: "key", Direction: query.SortASC}},
			})
			assertCtxCancelErr(t, err)
		})
	}
}
