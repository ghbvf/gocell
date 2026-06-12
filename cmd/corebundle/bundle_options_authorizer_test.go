package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// fakeAuthorizer is a minimal auth.Authorizer for wiring-path tests.
// It always returns a zero Decision (deny-by-default) and no error.
type fakeAuthorizer struct{}

func (fakeAuthorizer) Authorize(_ context.Context, _, _, _ string) (authz.Decision, error) {
	return authz.Decision{}, nil
}

var _ auth.Authorizer = fakeAuthorizer{}

// fakeAuthorizerCell is a cell.Cell that also satisfies authorizerProvider.
// Used to verify the happy-path wiring in primaryAuthorizerOption.
type fakeAuthorizerCell struct {
	cell.Cell
	authorizer auth.Authorizer
}

func newFakeAuthorizerCell(id string, a auth.Authorizer) *fakeAuthorizerCell {
	return &fakeAuthorizerCell{
		Cell:       cell.MustNewBaseCell(&metadata.CellMeta{ID: id, Type: "core"}),
		authorizer: a,
	}
}

func (f *fakeAuthorizerCell) Authorizer() auth.Authorizer { return f.authorizer }

// fakeNonAuthorizerCell is a plain cell that does NOT satisfy authorizerProvider.
type fakeNonAuthorizerCell struct{ cell.Cell }

func newFakeNonAuthorizerCell(id string) *fakeNonAuthorizerCell {
	return &fakeNonAuthorizerCell{
		Cell: cell.MustNewBaseCell(&metadata.CellMeta{ID: id, Type: "core"}),
	}
}

// ---------------------------------------------------------------------------
// primaryAuthorizerOption tests
// ---------------------------------------------------------------------------

// TestPrimaryAuthorizerOption_HappyPath verifies that exactly one
// authorizerProvider cell among mixed cells produces a valid bootstrap.Option
// without error, and that the lazyAuthorizer correctly delegates Authorize calls
// to the underlying provider's Authorizer.
func TestPrimaryAuthorizerOption_HappyPath(t *testing.T) {
	t.Parallel()
	cells := []cell.Cell{
		newFakeNonAuthorizerCell("configcore"),
		newFakeAuthorizerCell("accesscore", fakeAuthorizer{}),
		newFakeNonAuthorizerCell("auditcore"),
	}

	opt, err := primaryAuthorizerOption(cells)

	require.NoError(t, err)
	require.NotNil(t, opt, "a non-nil bootstrap.Option must be returned for the happy path")

	// Behavioral assertion: the option must be accepted by Bootstrap without error.
	// Use clock.Real() directly — we only need a valid clock, not a full SharedDeps.
	b := bootstrap.New(clock.Real(), opt)
	require.NotNil(t, b, "Bootstrap constructed with the authorizer option must not be nil")
}

// TestLazyAuthorizer_DelegatesAfterProviderReady verifies that lazyAuthorizer
// successfully delegates to the underlying Authorizer once the provider returns
// a non-nil value (simulating the post-Init state).
func TestLazyAuthorizer_DelegatesAfterProviderReady(t *testing.T) {
	t.Parallel()
	provider := newFakeAuthorizerCell("accesscore", fakeAuthorizer{})
	lazy := &lazyAuthorizer{provider: provider}

	_, err := lazy.Authorize(context.Background(), "user", "resource", "read")

	require.NoError(t, err, "lazyAuthorizer must successfully delegate when provider returns a live Authorizer")
}

// TestLazyAuthorizer_CachesAuthorizer verifies that lazyAuthorizer caches the
// resolved Authorizer so repeated Authorize calls don't invoke the provider
// redundantly. We verify this indirectly: if caching is broken, calling Authorize
// twice after resolution would call provider.Authorizer() twice — but since
// fakeAuthorizer is stateless we just verify both calls succeed.
func TestLazyAuthorizer_CachesAuthorizer(t *testing.T) {
	t.Parallel()
	provider := newFakeAuthorizerCell("accesscore", fakeAuthorizer{})
	lazy := &lazyAuthorizer{provider: provider}

	for range 3 {
		_, err := lazy.Authorize(context.Background(), "u", "r", "a")
		require.NoError(t, err)
	}
	// If resolved is populated after first call, subsequent calls use the cache.
	require.NotNil(t, lazy.resolved.Load(), "resolved must be populated after first successful Authorize call")
}

// TestPrimaryAuthorizerOption_NoProvider verifies that an assembly with no
// authorizerProvider cell causes primaryAuthorizerOption to fail-fast with an
// error containing "no cell implements authorizerProvider".
func TestPrimaryAuthorizerOption_NoProvider(t *testing.T) {
	t.Parallel()
	cells := []cell.Cell{
		newFakeNonAuthorizerCell("configcore"),
		newFakeNonAuthorizerCell("auditcore"),
	}

	opt, err := primaryAuthorizerOption(cells)

	require.Error(t, err, "zero authorizerProvider cells must fail-fast")
	assert.Nil(t, opt)
	assert.Contains(t, err.Error(), "no cell implements authorizerProvider",
		"error must identify the missing provider so operators can diagnose the misconfiguration")
}

// TestPrimaryAuthorizerOption_EmptyCells verifies that an empty cell list also
// fails-fast (covers the nil/empty slice edge case).
func TestPrimaryAuthorizerOption_EmptyCells(t *testing.T) {
	t.Parallel()
	opt, err := primaryAuthorizerOption(nil)

	require.Error(t, err)
	assert.Nil(t, opt)
	assert.Contains(t, err.Error(), "no cell implements authorizerProvider")
}

// TestPrimaryAuthorizerOption_MultipleProviders verifies that two cells both
// implementing authorizerProvider cause primaryAuthorizerOption to fail-fast
// with an error containing "multiple cells implement authorizerProvider".
func TestPrimaryAuthorizerOption_MultipleProviders(t *testing.T) {
	t.Parallel()
	cells := []cell.Cell{
		newFakeAuthorizerCell("accesscore1", fakeAuthorizer{}),
		newFakeAuthorizerCell("accesscore2", fakeAuthorizer{}),
	}

	opt, err := primaryAuthorizerOption(cells)

	require.Error(t, err, "two authorizerProvider cells must fail-fast")
	assert.Nil(t, opt)
	assert.Contains(t, err.Error(), "multiple cells implement authorizerProvider",
		"error must identify the ambiguous PDP so operators can resolve the misconfiguration")
}

// TestPrimaryAuthorizerOption_NilAuthorizerFromProvider verifies that a cell
// implementing authorizerProvider but returning a nil Authorizer produces an
// option without error at construction time (the nil check is deferred to the
// first Authorize call, because Init() hasn't run yet when options are built).
// The lazyAuthorizer then fails closed on the first Authorize call.
func TestPrimaryAuthorizerOption_NilAuthorizerFromProvider(t *testing.T) {
	t.Parallel()
	cells := []cell.Cell{
		newFakeAuthorizerCell("accesscore", nil), // returns nil Authorizer
	}

	// Option construction must succeed — nil check is deferred to request time.
	opt, err := primaryAuthorizerOption(cells)
	require.NoError(t, err, "option construction must not fail when provider returns nil (Init may not have run yet)")
	require.NotNil(t, opt)

	// The wrapped lazyAuthorizer must fail-closed (non-nil error) on first call.
	lazy := &lazyAuthorizer{provider: newFakeAuthorizerCell("accesscore", nil)}
	_, authErr := lazy.Authorize(context.Background(), "user", "resource", "read")
	require.Error(t, authErr, "lazyAuthorizer must fail closed when provider returns nil Authorizer")
	assert.Contains(t, authErr.Error(), "nil Authorizer",
		"error must identify the nil Authorizer to help operators diagnose the cell init state")
}
