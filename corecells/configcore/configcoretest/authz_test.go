package configcoretest

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/runtime/auth"
)

// TestCapturingAuthorizer_RecordsAndReturns covers CapturingAuthorizer.Authorize:
// it must record the (subject, resource, action) it was called with and return
// the fixed Decision. These helpers are exercised by every configcore slice's
// _test.go, but cross-package coverage is not attributed here, so this in-package
// test pins them directly.
func TestCapturingAuthorizer_RecordsAndReturns(t *testing.T) {
	allow, err := authz.Allow(authz.Obligations{})
	require.NoError(t, err)
	c := &CapturingAuthorizer{Decision: allow}

	dec, aerr := c.Authorize(context.Background(), "subj-1", "/api/v1/config/", "config:write")
	require.NoError(t, aerr)
	assert.True(t, dec.IsAllow(), "Authorize must return the fixed Decision")
	assert.Equal(t, "subj-1", c.GotSubject)
	assert.Equal(t, "/api/v1/config/", c.GotResource)
	assert.Equal(t, "config:write", c.GotAction)
}

// TestCapturingAuthorizer_PropagatesErr covers the Err passthrough path.
func TestCapturingAuthorizer_PropagatesErr(t *testing.T) {
	want := errors.New("pdp unavailable")
	c := &CapturingAuthorizer{Decision: authz.Deny("denied"), Err: want}

	dec, err := c.Authorize(context.Background(), "s", "/r", "a")
	require.ErrorIs(t, err, want)
	assert.False(t, dec.IsAllow())
	assert.Equal(t, "a", c.GotAction)
}

// TestWithAuthorizer_RoundTrips covers WithAuthorizer: the injected Authorizer
// must be retrievable via the canonical auth.AuthorizerFromContext funnel (the
// same one RequirePermission reads).
func TestWithAuthorizer_RoundTrips(t *testing.T) {
	c := &CapturingAuthorizer{Decision: authz.Deny("x")}
	ctx := WithAuthorizer(context.Background(), c)

	got, ok := auth.AuthorizerFromContext(ctx)
	require.True(t, ok, "WithAuthorizer must inject an Authorizer retrievable via AuthorizerFromContext")

	// Prove it is the same authorizer by exercising it and reading the capture.
	_, _ = got.Authorize(context.Background(), "s", "/r", "round:trip")
	assert.Equal(t, "round:trip", c.GotAction)
}
