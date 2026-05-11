package postgres

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// TestClassifyProbeFailure_NotFoundCode_ReturnsTrue: probe error carries the
// expected NotFound code → caller should wrap as 404, helper returns (true, nil).
func TestClassifyProbeFailure_NotFoundCode_ReturnsTrue(t *testing.T) {
	t.Parallel()
	probeErr := errcode.New(errcode.KindNotFound, errcode.ErrConfigRepoNotFound, "config not found")
	notFound, infraErr := classifyProbeFailure(probeErr, errcode.ErrConfigRepoNotFound, "Update", "site.title", "config_entry")
	assert.True(t, notFound, "ErrConfigRepoNotFound must be classified as confirmed absent")
	assert.NoError(t, infraErr, "NotFound branch must not produce infraErr")
}

// TestClassifyProbeFailure_DifferentErrcode_ReturnsInfraInternal: probe error is
// an *errcode.Error but Code != notFoundCode → infra fault, not 404.
func TestClassifyProbeFailure_DifferentErrcode_ReturnsInfraInternal(t *testing.T) {
	t.Parallel()
	// Simulate an unrelated errcode (e.g. transient DB error wrapped as
	// ErrInternal during scan) — must NOT collapse into NotFound.
	probeErr := errcode.New(errcode.KindInternal, errcode.ErrInternal, "scan failure during probe")
	notFound, infraErr := classifyProbeFailure(probeErr, errcode.ErrConfigRepoNotFound, "Update", "key1", "config_entry")
	assert.False(t, notFound, "non-NotFound errcode must NOT be classified as absent")
	require.Error(t, infraErr)
	var ec *errcode.Error
	require.True(t, errors.As(infraErr, &ec))
	assert.Equal(t, errcode.KindInternal, ec.Kind, "infra failure must map to KindInternal (500), not KindNotFound (404)")
	assert.Equal(t, errcode.ErrInternal, ec.Code)
	assert.Contains(t, ec.InternalMessage, "config_entry repo: Update probe failed key=key1",
		"Internal must carry op/key/entity for ops correlation")
	assert.Equal(t, errcode.CategoryInfra, ec.Category,
		"infra failures must be tagged CategoryInfra for routing")
}

// TestClassifyProbeFailure_PlainGoError_ReturnsInfraInternal: probe error is a
// plain Go error (io.EOF / context.Canceled / scan corruption) → infra fault.
func TestClassifyProbeFailure_PlainGoError_ReturnsInfraInternal(t *testing.T) {
	t.Parallel()
	probeErr := io.ErrUnexpectedEOF
	notFound, infraErr := classifyProbeFailure(probeErr, errcode.ErrFlagNotFound, "Delete", "flag.x", "feature_flag")
	assert.False(t, notFound, "non-errcode error must NOT be classified as absent")
	require.Error(t, infraErr)
	var ec *errcode.Error
	require.True(t, errors.As(infraErr, &ec))
	assert.Equal(t, errcode.KindInternal, ec.Kind)
	assert.True(t, errors.Is(infraErr, io.ErrUnexpectedEOF),
		"wrapped infra error must preserve the underlying cause via %%w")
	assert.Contains(t, ec.InternalMessage, "feature_flag repo: Delete probe failed key=flag.x")
}

// TestClassifyProbeFailure_DifferentNotFoundCodes: passing the wrong notFoundCode
// (e.g. ErrFlagNotFound when probe returned ErrConfigRepoNotFound) → infra path,
// not NotFound — guards against cross-entity misrouting.
func TestClassifyProbeFailure_DifferentNotFoundCodes(t *testing.T) {
	t.Parallel()
	probeErr := errcode.New(errcode.KindNotFound, errcode.ErrConfigRepoNotFound, "config not found")
	notFound, infraErr := classifyProbeFailure(probeErr, errcode.ErrFlagNotFound, "Update", "k", "feature_flag")
	assert.False(t, notFound, "code mismatch must not classify as absent")
	require.Error(t, infraErr)
	// Cross-entity probe mismatch is itself a bug — surfaces as Internal.
	var ec *errcode.Error
	require.True(t, errors.As(infraErr, &ec))
	assert.Equal(t, errcode.KindInternal, ec.Kind)
}

// TestClassifyProbeFailure_InternalDoesNotLeakDetails: Internal payload contains
// op/key/entity for ops correlation; Details (wire-visible) must be empty so
// repo internals do not leak via the 500 response envelope.
func TestClassifyProbeFailure_InternalDoesNotLeakDetails(t *testing.T) {
	t.Parallel()
	probeErr := errors.New("probe boom")
	_, infraErr := classifyProbeFailure(probeErr, errcode.ErrConfigRepoNotFound, "Update", "secret.key", "config_entry")
	require.Error(t, infraErr)
	var ec *errcode.Error
	require.True(t, errors.As(infraErr, &ec))
	// secret.key must appear ONLY in Internal, not the public Details slice.
	assert.NotEmpty(t, ec.InternalMessage)
	if _, ok := ec.FindAttr("key"); ok {
		t.Error("key must not leak via Details on infra fault")
	}
	// And the const-literal Message must not contain runtime key either.
	assert.False(t, strings.Contains(ec.Message, "secret.key"),
		"Message must be a const literal, runtime data goes to Internal")
}
