package webhooksource

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cellmodules/cellsecrets"
	"github.com/ghbvf/gocell/kernel/clock"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// validLocalAESKey is a 64-char hex string decoding to 32 bytes.
var validLocalAESKey = strings.Repeat("ab", 32)

func TestBuildKeyProviderFromName_LocalAES(t *testing.T) {
	kp, err := buildKeyProviderFromName("memory", providerLocalAES, validLocalAESKey, "", clock.Real(), kernelmetrics.NopProvider{})
	require.NoError(t, err)
	assert.NotNil(t, kp)
}

func TestBuildKeyProviderFromName_LocalAESEmptyKeyFailsClosed(t *testing.T) {
	kp, err := buildKeyProviderFromName("memory", providerLocalAES, "", "", clock.Real(), kernelmetrics.NopProvider{})
	assert.Nil(t, kp)
	require.Error(t, err)
}

func TestBuildKeyProviderFromName_EmptyProviderRejected(t *testing.T) {
	kp, err := buildKeyProviderFromName("memory", "", "", "", clock.Real(), kernelmetrics.NopProvider{})
	assert.Nil(t, kp)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
}

func TestBuildKeyProviderFromName_UnknownProviderRejected(t *testing.T) {
	kp, err := buildKeyProviderFromName("memory", "aws-kms", "", "", clock.Real(), kernelmetrics.NopProvider{})
	assert.Nil(t, kp)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
}

// TestBuildKeyProviderFromName_LocalAESRealModeRejectsDemoKey verifies the
// real-mode demo-key guard fires for the webhook master key (a well-known demo
// key must not encrypt production secrets).
func TestBuildKeyProviderFromName_LocalAESRealModeRejectsDemoKey(t *testing.T) {
	demo := cellsecrets.WellKnownDemoKeys()[0]
	kp, err := buildKeyProviderFromName(cellsecrets.RealAdapterMode, providerLocalAES, demo, "", clock.Real(), kernelmetrics.NopProvider{})
	assert.Nil(t, kp)
	require.Error(t, err)
}

// TestBuildKeyProviderFromName_VaultTransitFailsWithoutEnv verifies the
// vault-transit branch fails closed when Vault is not configured (missing
// VAULT_ADDR fails fast in all modes), rather than hanging on a connect attempt.
func TestBuildKeyProviderFromName_VaultTransitFailsWithoutEnv(t *testing.T) {
	t.Setenv("VAULT_ADDR", "")
	t.Setenv("VAULT_AUTH_METHOD", "")
	kp, err := buildKeyProviderFromName("memory", providerVaultTransit, "", "", clock.Real(), kernelmetrics.NopProvider{})
	assert.Nil(t, kp)
	require.Error(t, err)
}
