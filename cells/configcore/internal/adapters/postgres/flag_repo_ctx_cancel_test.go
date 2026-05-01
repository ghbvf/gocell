package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

// TestFlagRepo_CtxCanceled covers the ctx-cancel branch added in PR-A50+A51 +
// the PR275 P2-3 split: every IO surface in flag_repo.go must translate
// context.Canceled / context.DeadlineExceeded through ctxcancel.Wrap with
// branch-aware classification:
//
//   - context.Canceled         → ErrClientCanceled (HTTP 499 + slog.Warn)
//   - context.DeadlineExceeded → ErrServerTimeout  (HTTP 504 + slog.Error)
//
// Verifies the wrapCtxCancel hook on scanFlagOrMapError + wrapNonScanQueryErr.
func TestFlagRepo_CtxCanceled_ReturnsClientCanceled(t *testing.T) {
	tests := []struct {
		name    string
		scanErr error
	}{
		{"ctx canceled", context.Canceled},
		{"ctx deadline exceeded", context.DeadlineExceeded},
	}
	assertCtxCancelErr := func(t *testing.T, err error, scanErr error) {
		t.Helper()
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)

		expectedCode := errcode.ErrClientCanceled
		expected4xx := true
		if errors.Is(scanErr, context.DeadlineExceeded) {
			expectedCode = errcode.ErrServerTimeout
			expected4xx = false
		}
		assert.Equal(t, expectedCode, ec.Code,
			"Canceled→ErrClientCanceled (499) / DeadlineExceeded→ErrServerTimeout (504)")
		assert.Equal(t, expected4xx, errcode.IsExpected4xx(err),
			"499 routes through log4xx → slog.Warn; 504 routes through log5xx → slog.Error")
		assert.Contains(t, ec.InternalMessage, "ctx canceled",
			"must hit wrapCtxCancel path")
	}
	for _, tc := range tests {
		t.Run("GetByKey/"+tc.name, func(t *testing.T) {
			db := &mockDB{queryRowResult: &mockRow{scanErr: tc.scanErr}}
			repo := newFlagRepositoryFromDBTX(db)
			_, err := repo.GetByKey(context.Background(), "dark-mode")
			assertCtxCancelErr(t, err, tc.scanErr)
		})
		t.Run("Update/"+tc.name, func(t *testing.T) {
			db := &mockDB{queryRowResult: &mockRow{scanErr: tc.scanErr}}
			repo := newFlagRepositoryFromDBTX(db)
			_, err := repo.Update(context.Background(), "dark-mode", true, 100, "desc")
			assertCtxCancelErr(t, err, tc.scanErr)
		})
		t.Run("Delete/"+tc.name, func(t *testing.T) {
			db := &mockDB{queryRowResult: &mockRow{scanErr: tc.scanErr}}
			repo := newFlagRepositoryFromDBTX(db)
			_, err := repo.Delete(context.Background(), "dark-mode")
			assertCtxCancelErr(t, err, tc.scanErr)
		})
		t.Run("Toggle/"+tc.name, func(t *testing.T) {
			db := &mockDB{queryRowResult: &mockRow{scanErr: tc.scanErr}}
			repo := newFlagRepositoryFromDBTX(db)
			_, err := repo.Toggle(context.Background(), "dark-mode", true)
			assertCtxCancelErr(t, err, tc.scanErr)
		})
		t.Run("Create/"+tc.name, func(t *testing.T) {
			db := &mockDB{execErr: tc.scanErr}
			repo := newFlagRepositoryFromDBTX(db)
			err := repo.Create(context.Background(), &domain.FeatureFlag{ID: "flg-1", Key: "dark-mode"})
			assertCtxCancelErr(t, err, tc.scanErr)
		})
		t.Run("List/Query/"+tc.name, func(t *testing.T) {
			db := &mockDB{queryErr: tc.scanErr}
			repo := newFlagRepositoryFromDBTX(db)
			_, err := repo.List(context.Background(), query.ListParams{
				Sort: []query.SortColumn{{Name: "key", Direction: query.SortASC}},
			})
			assertCtxCancelErr(t, err, tc.scanErr)
		})
	}
}
