package bootstrap

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

// ---------------------------------------------------------------------------
// Fakes (relocated from cmd/corebundle with the PrimaryAuthorizerOption logic)
// ---------------------------------------------------------------------------

// fakeAuthorizer is a minimal auth.Authorizer for wiring-path tests.
// It always returns a zero Decision (deny-by-default) and no error.
type fakeAuthorizer struct{}

func (fakeAuthorizer) Authorize(_ context.Context, _, _, _ string) (authz.Decision, error) {
	return authz.Decision{}, nil
}

var _ auth.Authorizer = fakeAuthorizer{}

// countingAuthorizer counts how many times Authorize is called. Used to verify
// that lazyAuthorizer caches the resolved Authorizer and does not call the
// provider more than once.
type countingAuthorizer struct {
	calls atomic.Int64
}

func (c *countingAuthorizer) Authorize(_ context.Context, _, _, _ string) (authz.Decision, error) {
	c.calls.Add(1)
	return authz.Decision{}, nil
}

var _ auth.Authorizer = (*countingAuthorizer)(nil)

// countingAuthorizerCell wraps a countingAuthorizer behind an authorizerProvider.
// It also counts how many times Authorizer() is called so we can assert
// exactly-once resolution across multiple Authorize invocations.
type countingAuthorizerCell struct {
	cell.Cell
	authorizer    *countingAuthorizer
	providerCalls atomic.Int64
}

func newCountingAuthorizerCell(id string) *countingAuthorizerCell {
	return &countingAuthorizerCell{
		Cell:       cell.MustNewBaseCell(&metadata.CellMeta{ID: id, Type: "core"}),
		authorizer: &countingAuthorizer{},
	}
}

func (c *countingAuthorizerCell) Authorizer() auth.Authorizer {
	c.providerCalls.Add(1)
	return c.authorizer
}

// fakeAuthorizerCell is a cell.Cell that also satisfies authorizerProvider.
// Used to verify the happy-path wiring in PrimaryAuthorizerOption.
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
// PrimaryAuthorizerOption tests
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

	opt, err := PrimaryAuthorizerOption(cells)

	require.NoError(t, err)
	require.NotNil(t, opt, "a non-nil bootstrap.Option must be returned for the happy path")

	// Behavioral assertion: the option must be accepted by Bootstrap without error.
	// Use clock.Real() directly — we only need a valid clock, not a full SharedDeps.
	b := New(clock.Real(), opt)
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
// resolved Authorizer so provider.Authorizer() is called exactly once regardless
// of how many Authorize invocations follow. A broken always-resolve impl would
// call the provider on every request; this test catches that by asserting the
// provider call count == 1 across 3 Authorize calls.
func TestLazyAuthorizer_CachesAuthorizer(t *testing.T) {
	t.Parallel()
	provider := newCountingAuthorizerCell("accesscore")
	lazy := &lazyAuthorizer{provider: provider}

	const calls = 3
	for range calls {
		_, err := lazy.Authorize(context.Background(), "u", "r", "a")
		require.NoError(t, err)
	}

	assert.Equal(t, int64(1), provider.providerCalls.Load(),
		"provider.Authorizer() must be called exactly once across %d Authorize invocations (cache miss on every call = broken caching)", calls)
	assert.Equal(t, int64(calls), provider.authorizer.calls.Load(),
		"the underlying Authorizer must be called once per Authorize invocation")
	require.NotNil(t, lazy.resolved.Load(), "resolved must be populated after first successful Authorize call")
}

// TestLazyAuthorizer_ResolveAuthorizer_Success verifies the F8 startup hook: a
// live provider resolves without error and caches the Authorizer, so a later
// Authorize takes the cached fast path (provider.Authorizer() not called again).
func TestLazyAuthorizer_ResolveAuthorizer_Success(t *testing.T) {
	t.Parallel()
	provider := newCountingAuthorizerCell("accesscore")
	lazy := &lazyAuthorizer{provider: provider}

	require.NoError(t, lazy.ResolveAuthorizer(), "ResolveAuthorizer must succeed when the provider yields a live Authorizer")
	require.NotNil(t, lazy.resolved.Load(), "ResolveAuthorizer must cache the resolved Authorizer")

	_, err := lazy.Authorize(context.Background(), "u", "r", "a")
	require.NoError(t, err)
	assert.Equal(t, int64(1), provider.providerCalls.Load(),
		"provider.Authorizer() must be called exactly once (resolved at startup, cached for the request)")
}

// TestLazyAuthorizer_ResolveAuthorizer_NilFailsFast verifies the F8 startup
// fail-fast: a provider that yields nil makes ResolveAuthorizer return a
// KindUnavailable error, so bootstrap aborts at boot rather than 503-ing on the
// first request.
func TestLazyAuthorizer_ResolveAuthorizer_NilFailsFast(t *testing.T) {
	t.Parallel()
	lazy := &lazyAuthorizer{provider: newFakeAuthorizerCell("accesscore", nil)}

	err := lazy.ResolveAuthorizer()
	require.Error(t, err, "ResolveAuthorizer must fail-fast when the provider yields nil")
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "nil-provider startup error must be an errcode.Error")
	assert.Equal(t, errcode.KindUnavailable, ec.Kind)
	assert.Equal(t, errcode.ErrServiceUnavailable, ec.Code)
	assert.Nil(t, lazy.resolved.Load(), "a failed resolve must not cache anything")
}

// TestPrimaryAuthorizerOption_NoProvider verifies that an assembly with no
// authorizerProvider cell causes PrimaryAuthorizerOption to fail-fast with an
// error containing "no cell implements authorizerProvider".
func TestPrimaryAuthorizerOption_NoProvider(t *testing.T) {
	t.Parallel()
	cells := []cell.Cell{
		newFakeNonAuthorizerCell("configcore"),
		newFakeNonAuthorizerCell("auditcore"),
	}

	opt, err := PrimaryAuthorizerOption(cells)

	require.Error(t, err, "zero authorizerProvider cells must fail-fast")
	assert.Nil(t, opt)
	assert.Contains(t, err.Error(), "no cell implements authorizerProvider",
		"error must identify the missing provider so operators can diagnose the misconfiguration")
}

// TestPrimaryAuthorizerOption_EmptyCells verifies that an empty cell list also
// fails-fast (covers the nil/empty slice edge case).
func TestPrimaryAuthorizerOption_EmptyCells(t *testing.T) {
	t.Parallel()
	opt, err := PrimaryAuthorizerOption(nil)

	require.Error(t, err)
	assert.Nil(t, opt)
	assert.Contains(t, err.Error(), "no cell implements authorizerProvider")
}

// TestPrimaryAuthorizerOption_MultipleProviders verifies that two cells both
// implementing authorizerProvider cause PrimaryAuthorizerOption to fail-fast
// with an error containing "multiple cells implement authorizerProvider".
func TestPrimaryAuthorizerOption_MultipleProviders(t *testing.T) {
	t.Parallel()
	cells := []cell.Cell{
		newFakeAuthorizerCell("accesscore1", fakeAuthorizer{}),
		newFakeAuthorizerCell("accesscore2", fakeAuthorizer{}),
	}

	opt, err := PrimaryAuthorizerOption(cells)

	require.Error(t, err, "two authorizerProvider cells must fail-fast")
	assert.Nil(t, opt)
	assert.Contains(t, err.Error(), "multiple cells implement authorizerProvider",
		"error must identify the ambiguous PDP so operators can resolve the misconfiguration")
}

// TestPrimaryAuthorizerOption_NilAuthorizerFromProvider verifies that a cell
// implementing authorizerProvider but returning a nil Authorizer produces an
// option without error at construction time (the nil check cannot run yet
// because Init() hasn't run when options are built). The nil provider is caught
// at bootstrap startup by ResolveAuthorizer (F8,
// TestLazyAuthorizer_ResolveAuthorizer_NilFailsFast); this test pins the
// request-path fail-closed guard that remains as defense-in-depth.
func TestPrimaryAuthorizerOption_NilAuthorizerFromProvider(t *testing.T) {
	t.Parallel()
	cells := []cell.Cell{
		newFakeAuthorizerCell("accesscore", nil), // returns nil Authorizer
	}

	// Option construction must succeed — nil check is deferred to request time.
	opt, err := PrimaryAuthorizerOption(cells)
	require.NoError(t, err, "option construction must not fail when provider returns nil (Init may not have run yet)")
	require.NotNil(t, opt)

	// The wrapped lazyAuthorizer must fail-closed (non-nil error) on first call.
	lazy := &lazyAuthorizer{provider: newFakeAuthorizerCell("accesscore", nil)}
	_, authErr := lazy.Authorize(context.Background(), "user", "resource", "read")
	require.Error(t, authErr, "lazyAuthorizer must fail closed when provider returns nil Authorizer")
	// The error is an errcode.Error with KindUnavailable so RequirePermission maps
	// it to 503. The internal detail carries the provider type for diagnostics; the
	// message is surfaced via the errcode Message field.
	var ec *errcode.Error
	require.True(t, errors.As(authErr, &ec), "lazyAuthorizer nil-provider error must be an errcode.Error")
	assert.Equal(t, errcode.KindUnavailable, ec.Kind,
		"nil-provider error must be KindUnavailable so RequirePermission maps it to 503")
	assert.Equal(t, errcode.ErrServiceUnavailable, ec.Code)
	assert.Contains(t, ec.Message, "nil Authorizer",
		"error message must identify the nil Authorizer to help operators diagnose the cell init state")
}
