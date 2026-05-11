package flagwrite

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/configcore/internal/mem"
	flagsdelete "github.com/ghbvf/gocell/generated/contracts/http/config/flags/delete/v1"
	toggle "github.com/ghbvf/gocell/generated/contracts/http/config/flags/toggle/v1"
	update "github.com/ghbvf/gocell/generated/contracts/http/config/flags/update/v1"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

// PR464 P2.2: typed 404 / 409 envelope adapter regression coverage.
// fakeFlagRepoErr wraps mem.FlagRepository and overrides Update/Toggle/Delete
// to inject controlled errcode.Error responses, asserting each adapter's
// errors.As + ce.Code switch returns the typed envelope (not framework
// fallback) so codegen-declared status codes stay locked.

const testFlagwriteAdmin = "admin-test"

type stubFlagTxRunner struct{}

func (stubFlagTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

type fakeFlagRepoErr struct {
	*mem.FlagRepository
	updateErr error
	toggleErr error
	deleteErr error
}

func (f *fakeFlagRepoErr) Update(_ context.Context, _ string, _ int, _ bool, _ int, _ string) (*domain.FeatureFlag, error) {
	return nil, f.updateErr
}

func (f *fakeFlagRepoErr) Toggle(_ context.Context, _ string, _ int, _ bool) (*domain.FeatureFlag, error) {
	return nil, f.toggleErr
}

func (f *fakeFlagRepoErr) Delete(_ context.Context, _ string, _ int) (*domain.FeatureFlag, error) {
	return nil, f.deleteErr
}

func adminCtx() context.Context {
	return auth.TestContext(testFlagwriteAdmin, []string{auth.RoleAdmin})
}

func newFlagAdapters(t *testing.T, updateErr, toggleErr, deleteErr error) (UpdateAdapter, ToggleAdapter, FlagDeleteAdapter) {
	t.Helper()
	repo := &fakeFlagRepoErr{
		FlagRepository: mem.NewFlagRepository(clock.Real()),
		updateErr:      updateErr,
		toggleErr:      toggleErr,
		deleteErr:      deleteErr,
	}
	svc, err := NewService(repo, slog.Default(), clock.Real(), WithTxManager(stubFlagTxRunner{}))
	require.NoError(t, err)
	return UpdateAdapter{S: svc}, ToggleAdapter{S: svc}, FlagDeleteAdapter{S: svc}
}

// --- Update typed envelope ---

func TestFlagUpdateAdapter_NotFound_Returns404Typed(t *testing.T) {
	updateAd, _, _ := newFlagAdapters(t,
		errcode.New(errcode.KindNotFound, errcode.ErrFlagNotFound, "flag not found"),
		nil, nil)
	resp, err := updateAd.Update(adminCtx(), &update.Request{
		Key: "missing", Enabled: true, RolloutPercentage: 100, Description: "d", ExpectedVersion: 1,
	})
	require.NoError(t, err)
	typed, ok := resp.(update.Update404ErrorResponse)
	require.True(t, ok, "expected Update404ErrorResponse, got %T", resp)
	assert.Equal(t, errcode.ErrFlagNotFound, typed.Body.Code)
}

func TestFlagUpdateAdapter_VersionConflict_Returns409Typed(t *testing.T) {
	updateAd, _, _ := newFlagAdapters(t,
		errcode.New(errcode.KindConflict, errcode.ErrVersionConflict, "concurrent update detected; reload and retry"),
		nil, nil)
	resp, err := updateAd.Update(adminCtx(), &update.Request{
		Key: "stale", Enabled: true, RolloutPercentage: 100, Description: "d", ExpectedVersion: 1,
	})
	require.NoError(t, err)
	typed, ok := resp.(update.Update409ErrorResponse)
	require.True(t, ok, "expected Update409ErrorResponse, got %T", resp)
	assert.Equal(t, errcode.ErrVersionConflict, typed.Body.Code)
}

// --- Toggle typed envelope ---

func TestFlagToggleAdapter_NotFound_Returns404Typed(t *testing.T) {
	_, toggleAd, _ := newFlagAdapters(t, nil,
		errcode.New(errcode.KindNotFound, errcode.ErrFlagNotFound, "flag not found"), nil)
	resp, err := toggleAd.Toggle(adminCtx(), &toggle.Request{
		Key: "missing", Enabled: true, ExpectedVersion: 1,
	})
	require.NoError(t, err)
	typed, ok := resp.(toggle.Toggle404ErrorResponse)
	require.True(t, ok, "expected Toggle404ErrorResponse, got %T", resp)
	assert.Equal(t, errcode.ErrFlagNotFound, typed.Body.Code)
}

func TestFlagToggleAdapter_VersionConflict_Returns409Typed(t *testing.T) {
	_, toggleAd, _ := newFlagAdapters(t, nil,
		errcode.New(errcode.KindConflict, errcode.ErrVersionConflict, "concurrent update detected; reload and retry"), nil)
	resp, err := toggleAd.Toggle(adminCtx(), &toggle.Request{
		Key: "stale", Enabled: true, ExpectedVersion: 1,
	})
	require.NoError(t, err)
	typed, ok := resp.(toggle.Toggle409ErrorResponse)
	require.True(t, ok, "expected Toggle409ErrorResponse, got %T", resp)
	assert.Equal(t, errcode.ErrVersionConflict, typed.Body.Code)
}

// --- Delete typed envelope ---

func TestFlagDeleteAdapter_NotFound_Returns404Typed(t *testing.T) {
	_, _, deleteAd := newFlagAdapters(t, nil, nil,
		errcode.New(errcode.KindNotFound, errcode.ErrFlagNotFound, "flag not found"))
	resp, err := deleteAd.Delete(adminCtx(), &flagsdelete.Request{Key: "missing", ExpectedVersion: 1})
	require.NoError(t, err)
	typed, ok := resp.(flagsdelete.Delete404ErrorResponse)
	require.True(t, ok, "expected Delete404ErrorResponse, got %T", resp)
	assert.Equal(t, errcode.ErrFlagNotFound, typed.Body.Code)
}

func TestFlagDeleteAdapter_VersionConflict_Returns409Typed(t *testing.T) {
	_, _, deleteAd := newFlagAdapters(t, nil, nil,
		errcode.New(errcode.KindConflict, errcode.ErrVersionConflict, "concurrent update detected; reload and retry"))
	resp, err := deleteAd.Delete(adminCtx(), &flagsdelete.Request{Key: "stale", ExpectedVersion: 1})
	require.NoError(t, err)
	typed, ok := resp.(flagsdelete.Delete409ErrorResponse)
	require.True(t, ok, "expected Delete409ErrorResponse, got %T", resp)
	assert.Equal(t, errcode.ErrVersionConflict, typed.Body.Code)
}
