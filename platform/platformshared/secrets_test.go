package platformshared_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/platform/platformshared"
)

// testDemoKey is the first well-known demo key for RejectDemoKey tests.
const testDemoKey = "dev-hmac-key-replace-in-prod!!!!"

// testStarterDemoKey is the corebundlestarter demo secret that must also be
// caught by RejectDemoKey.
const testStarterDemoKey = "starter-dev-secret-32-bytes-ok!!"

// testFreshKey is a key that is not in WellKnownDemoKeys.
const testFreshKey = "totally-fresh-key-not-in-list!!!"

// testFreshKey2 is a second fresh key, distinct from testFreshKey.
// Used to test cursor key rotation (primary ≠ previous required).
const testFreshKey2 = "another-fresh-key-not-in-list!!!"

// testCursorDevDefault is the dev-default cursor key for access.
const testCursorDevDefault = "corebundle-access-cursor-key32!!"

func TestRejectDemoKey_RealMode_RejectsDemoKey(t *testing.T) {
	err := platformshared.RejectDemoKey(platformshared.RealAdapterMode, "TEST_KEY", []byte(testDemoKey))
	require.Error(t, err, "real mode must reject a well-known demo key")
	assert.Contains(t, err.Error(), "well-known demo key")
}

func TestRejectDemoKey_RealMode_RejectsStarterSecret(t *testing.T) {
	// Ensure examples/corebundlestarter's devServiceSecret is caught.
	err := platformshared.RejectDemoKey(platformshared.RealAdapterMode, "GOCELL_SERVICE_SECRET", []byte(testStarterDemoKey))
	require.Error(t, err, "real mode must reject the corebundlestarter demo secret")
}

func TestRejectDemoKey_RealMode_AllowsFreshKey(t *testing.T) {
	err := platformshared.RejectDemoKey(platformshared.RealAdapterMode, "TEST_KEY", []byte(testFreshKey))
	assert.NoError(t, err, "real mode must allow a key not in WellKnownDemoKeys")
}

func TestRejectDemoKey_DevMode_AllowsDemoKey(t *testing.T) {
	err := platformshared.RejectDemoKey("dev", "TEST_KEY", []byte(testDemoKey))
	assert.NoError(t, err, "dev mode must not reject demo keys")
}

func TestRejectDemoKey_RealMode_RejectsAllWellKnownKeys(t *testing.T) {
	for _, demo := range platformshared.WellKnownDemoKeys {
		err := platformshared.RejectDemoKey(platformshared.RealAdapterMode, "TEST_KEY", []byte(demo))
		assert.Error(t, err, "real mode must reject demo key %q", demo)
	}
}

func TestResolveAndRejectDemoKey_RealMode_EmptyPrimary_ReturnsError(t *testing.T) {
	_, err := platformshared.ResolveAndRejectDemoKey(platformshared.RealAdapterMode, "ENV_KEY", "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be set in adapter mode")
}

func TestResolveAndRejectDemoKey_RealMode_DemoKeyPrimary_ReturnsError(t *testing.T) {
	_, err := platformshared.ResolveAndRejectDemoKey(platformshared.RealAdapterMode, "ENV_KEY", testDemoKey, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "well-known demo key")
}

func TestResolveAndRejectDemoKey_DevMode_FallsBackToDevDefault(t *testing.T) {
	key, err := platformshared.ResolveAndRejectDemoKey("dev", "ENV_KEY", "", testFreshKey)
	require.NoError(t, err)
	assert.Equal(t, []byte(testFreshKey), key)
}

func TestResolveAndRejectDemoKey_DevMode_PrimaryTakesPrecedence(t *testing.T) {
	key, err := platformshared.ResolveAndRejectDemoKey("dev", "ENV_KEY", testFreshKey, testDemoKey)
	require.NoError(t, err)
	assert.Equal(t, []byte(testFreshKey), key)
}

func TestBuildCursorCodec_DevMode_DevDefaultPath(t *testing.T) {
	codec, err := platformshared.BuildCursorCodec(platformshared.CursorCodecConfig{
		AdapterMode: "dev",
		EnvName:     "GOCELL_ACCESSCORE_CURSOR_KEY",
		PrevEnvName: "GOCELL_ACCESSCORE_CURSOR_PREVIOUS_KEY",
		Primary:     "",
		Previous:    "",
		DevDefault:  testCursorDevDefault,
		Label:       "access",
	})
	require.NoError(t, err)
	assert.NotNil(t, codec)
}

func TestBuildCursorCodec_RealMode_EmptyPrimary_ReturnsError(t *testing.T) {
	_, err := platformshared.BuildCursorCodec(platformshared.CursorCodecConfig{
		AdapterMode: platformshared.RealAdapterMode,
		EnvName:     "GOCELL_ACCESSCORE_CURSOR_KEY",
		PrevEnvName: "GOCELL_ACCESSCORE_CURSOR_PREVIOUS_KEY",
		Primary:     "",
		Previous:    "",
		DevDefault:  testCursorDevDefault,
		Label:       "access",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be set in adapter mode")
}

func TestBuildCursorCodec_RealMode_DemoKeyPrimary_ReturnsError(t *testing.T) {
	_, err := platformshared.BuildCursorCodec(platformshared.CursorCodecConfig{
		AdapterMode: platformshared.RealAdapterMode,
		EnvName:     "GOCELL_ACCESSCORE_CURSOR_KEY",
		PrevEnvName: "GOCELL_ACCESSCORE_CURSOR_PREVIOUS_KEY",
		Primary:     testCursorDevDefault,
		Previous:    "",
		DevDefault:  testCursorDevDefault,
		Label:       "access",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "well-known demo key")
}

func TestBuildCursorCodec_WithPreviousKey_LogsRotation(t *testing.T) {
	// Primary and previous must differ; both are fresh (not demo keys).
	codec, err := platformshared.BuildCursorCodec(platformshared.CursorCodecConfig{
		AdapterMode: "dev",
		EnvName:     "GOCELL_ACCESSCORE_CURSOR_KEY",
		PrevEnvName: "GOCELL_ACCESSCORE_CURSOR_PREVIOUS_KEY",
		Primary:     testFreshKey,
		Previous:    testFreshKey2,
		DevDefault:  testCursorDevDefault,
		Label:       "access",
	})
	require.NoError(t, err)
	assert.NotNil(t, codec)
}

func TestBuildCursorCodec_RealMode_DemoKeyPrevious_ReturnsError(t *testing.T) {
	_, err := platformshared.BuildCursorCodec(platformshared.CursorCodecConfig{
		AdapterMode: platformshared.RealAdapterMode,
		EnvName:     "GOCELL_ACCESSCORE_CURSOR_KEY",
		PrevEnvName: "GOCELL_ACCESSCORE_CURSOR_PREVIOUS_KEY",
		Primary:     testFreshKey,
		Previous:    testCursorDevDefault,
		DevDefault:  testCursorDevDefault,
		Label:       "access",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "well-known demo key")
}

func TestBuildHMACKey_DevMode_FallsBack(t *testing.T) {
	key, err := platformshared.BuildHMACKey(platformshared.HMACKeyConfig{
		AdapterMode: "dev",
		EnvName:     "GOCELL_TEST_HMAC_KEY",
		Primary:     "",
		DevDefault:  testFreshKey,
	})
	require.NoError(t, err)
	assert.Equal(t, []byte(testFreshKey), key)
}

func TestBuildHMACKey_RealMode_EmptyPrimary_ReturnsError(t *testing.T) {
	_, err := platformshared.BuildHMACKey(platformshared.HMACKeyConfig{
		AdapterMode: platformshared.RealAdapterMode,
		EnvName:     "GOCELL_TEST_HMAC_KEY",
		Primary:     "",
		DevDefault:  testFreshKey,
	})
	require.Error(t, err)
}

func TestInternalAddrToBaseURL(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want string
	}{
		{
			name: "empty addr returns loopback default",
			addr: "",
			want: "http://127.0.0.1:9090",
		},
		{
			name: "port-only addr prepends loopback",
			addr: ":9090",
			want: "http://127.0.0.1:9090",
		},
		{
			name: "0.0.0.0 replaced by 127.0.0.1",
			addr: "0.0.0.0:8080",
			want: "http://127.0.0.1:8080",
		},
		{
			name: "host:port prefixed with http://",
			addr: "myhost:9000",
			want: "http://myhost:9000",
		},
		{
			name: "127.0.0.1:port passed through",
			addr: "127.0.0.1:9091",
			want: "http://127.0.0.1:9091",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := platformshared.InternalAddrToBaseURL(tc.addr)
			assert.Equal(t, tc.want, got)
		})
	}
}
