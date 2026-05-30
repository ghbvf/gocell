package mqtt

import (
	"crypto/tls"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// configNegTimeout is used in validation tests that assert negative ConnectTimeout
// is rejected.
const configNegTimeout = -1 * time.Second

// configKeepAliveOverflow is used in TestConfig_Validate_KeepAliveOverflow to
// assert KeepAlive > uint16 max seconds (65535s) is rejected. Extracted to a
// const per TEST-TIME-LITERAL-01.
const configKeepAliveOverflow = time.Duration(65536) * time.Second

// configKeepAliveSubSecond is used in TestConfig_Validate_KeepAliveSubSecond
// to assert KeepAlive ∈ (0, 1s) is rejected before it floor-truncates to
// uint16(0) on the wire.
const configKeepAliveSubSecond = 500 * time.Millisecond

// configNegSessionExpiry is used in TestConfig_Validate_SessionExpiryNegative
// to assert that negative SessionExpiry is rejected. Extracted to a const
// per TEST-TIME-LITERAL-01.
const configNegSessionExpiry = -1 * time.Second

// configSessionExpirySubSecond is used in TestConfig_Validate_SessionExpirySubSecond
// to assert SessionExpiry ∈ (0, 1s) is rejected before it floor-truncates to
// uint32(0) on the wire.
const configSessionExpirySubSecond = 500 * time.Millisecond

// validClientID returns a ClientID for use in tests.
func mustClientID(t *testing.T) ClientID {
	t.Helper()
	cid, err := ParseEphemeralClientID("testcell", "pub")
	require.NoError(t, err)
	return cid
}

// validConfig returns a Config that passes Validate. The broker uses a
// loopback IP literal (127.0.0.1) so the secutil.ValidateTLSEndpoint
// plaintext check accepts it — "localhost" is intentionally NOT accepted
// because it is a DNS name.
func validConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		ClientID:       mustClientID(t),
		Brokers:        []string{"tcp://127.0.0.1:1883"},
		ConnectTimeout: testtime.D5s,
		KeepAlive:      testtime.D30s,
		Backoff: BackoffConfig{
			BaseDelay: testtime.D500ms,
			MaxDelay:  testtime.D30s,
		},
	}
}

// TestConfig_Validate_HappyPath covers a fully valid config.
func TestConfig_Validate_HappyPath(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	require.NoError(t, cfg.Validate())
}

// TestConfig_Validate_ZeroClientID verifies that the zero-value ClientID is rejected.
func TestConfig_Validate_ZeroClientID(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	cfg.ClientID = ClientID{} // zero value — invalid
	err := cfg.Validate()
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTInvalidConfig, ec.Code)
}

// TestConfig_Validate_NoBrokers verifies at-least-one-broker requirement.
func TestConfig_Validate_NoBrokers(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	cfg.Brokers = nil
	err := cfg.Validate()
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTInvalidConfig, ec.Code)
}

// TestConfig_Validate_EmptyBrokerSlice verifies empty slice is also rejected.
func TestConfig_Validate_EmptyBrokerSlice(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	cfg.Brokers = []string{}
	err := cfg.Validate()
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTInvalidConfig, ec.Code)
}

// TestConfig_Validate_InvalidBrokerScheme covers unsupported URL schemes.
func TestConfig_Validate_InvalidBrokerScheme(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	cfg.Brokers = []string{"http://localhost:1883"}
	err := cfg.Validate()
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTInvalidConfig, ec.Code)
}

// TestConfig_Validate_MalformedBrokerURL ensures unparseable URLs are rejected.
func TestConfig_Validate_MalformedBrokerURL(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	cfg.Brokers = []string{"://noscheme"}
	err := cfg.Validate()
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTInvalidConfig, ec.Code)
}

// TestConfig_Validate_BrokerMissingHost verifies that a URL without a host is rejected.
func TestConfig_Validate_BrokerMissingHost(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	cfg.Brokers = []string{"tcp://"}
	err := cfg.Validate()
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTInvalidConfig, ec.Code)
}

// TestConfig_Validate_TLSSchemeWithoutTLSConfig verifies that a tls/ssl/mqtts/wss
// broker requires c.TLS to be non-nil.
func TestConfig_Validate_TLSSchemeWithoutTLSConfig(t *testing.T) {
	t.Parallel()
	for _, scheme := range []string{"tls", "ssl", "mqtts", "wss"} {
		scheme := scheme
		t.Run(scheme, func(t *testing.T) {
			t.Parallel()
			cfg := validConfig(t)
			cfg.Brokers = []string{scheme + "://broker.example.com:8883"}
			cfg.TLS = nil // intentionally missing
			err := cfg.Validate()
			require.Error(t, err)
			var ec *errcode.Error
			require.True(t, errors.As(err, &ec))
			assert.Equal(t, ErrAdapterMQTTInvalidConfig, ec.Code)
		})
	}
}

// TestConfig_Validate_TLSSchemeWithTLSConfig verifies that tls brokers succeed
// when a verifying TLS config is provided.
func TestConfig_Validate_TLSSchemeWithTLSConfig(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	cfg.Brokers = []string{"tls://broker.example.com:8883"}
	cfg.TLS = &tls.Config{MinVersion: tls.VersionTLS12} // verifying config (no InsecureSkipVerify)
	require.NoError(t, cfg.Validate())
}

// TestConfig_Validate_TLSInsecureSkipVerify_Rejected verifies that a TLS config
// with InsecureSkipVerify=true is rejected fail-closed, regardless of scheme.
func TestConfig_Validate_TLSInsecureSkipVerify_Rejected(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	cfg.Brokers = []string{"tls://broker.example.com:8883"}
	cfg.TLS = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // negative test: asserts this is rejected
	err := cfg.Validate()
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTInvalidConfig, ec.Code)
}

// TestConfig_Validate_TLSMinVersion verifies the downgrade-protection floor:
// an explicit MinVersion below TLS 1.2 is rejected; unset (0 → Go default 1.2)
// and >= TLS 1.2 are accepted.
func TestConfig_Validate_TLSMinVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		minVersion uint16
		wantErr    bool
	}{
		{name: "tls10-rejected", minVersion: tls.VersionTLS10, wantErr: true},
		{name: "tls11-rejected", minVersion: tls.VersionTLS11, wantErr: true},
		{name: "unset-ok", minVersion: 0, wantErr: false},
		{name: "tls12-ok", minVersion: tls.VersionTLS12, wantErr: false},
		{name: "tls13-ok", minVersion: tls.VersionTLS13, wantErr: false},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := validConfig(t)
			cfg.Brokers = []string{"tls://broker.example.com:8883"}
			cfg.TLS = &tls.Config{MinVersion: tc.minVersion} //nolint:gosec // 0 = unset → Go default TLS 1.2
			err := cfg.Validate()
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			var ec *errcode.Error
			require.True(t, errors.As(err, &ec))
			assert.Equal(t, ErrAdapterMQTTInvalidConfig, ec.Code)
		})
	}
}

// TestConfig_Validate_AllValidSchemes verifies all plaintext scheme variants
// are accepted when the host is a loopback IP literal (dev/CI testcontainer
// exception). The same schemes with non-loopback hosts are rejected by
// TestConfig_Validate_PlaintextRemote_Rejected.
func TestConfig_Validate_AllValidSchemes(t *testing.T) {
	t.Parallel()
	for _, scheme := range []string{"tcp", "mqtt", "ws"} {
		scheme := scheme
		t.Run(scheme, func(t *testing.T) {
			t.Parallel()
			cfg := validConfig(t)
			cfg.Brokers = []string{scheme + "://127.0.0.1:1883"}
			require.NoError(t, cfg.Validate())
		})
	}
}

// TestConfig_Validate_PlaintextRemote_Rejected verifies that the plaintext
// schemes (tcp/mqtt/ws) require a loopback host. Remote hosts are rejected
// via pkg/secutil.ValidateTLSEndpoint (C1 F2 — fail-closed against
// silent credential exposure over plaintext).
func TestConfig_Validate_PlaintextRemote_Rejected(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		broker string
	}{
		{"tcp-remote-host", "tcp://broker.example.com:1883"},
		{"mqtt-remote-host", "mqtt://broker.example.com:1883"},
		{"ws-remote-host", "ws://broker.example.com:1883"},
		{"tcp-localhost-dns-not-accepted", "tcp://localhost:1883"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := validConfig(t)
			cfg.Brokers = []string{tc.broker}
			err := cfg.Validate()
			require.Error(t, err, "broker=%s must be rejected", tc.broker)
			var ec *errcode.Error
			require.True(t, errors.As(err, &ec))
			assert.Equal(t, ErrAdapterMQTTInvalidConfig, ec.Code)
		})
	}
}

// TestConfig_Validate_KeepAliveOverflow verifies KeepAlive >65535s is
// rejected (uint16 wire field upper bound).
func TestConfig_Validate_KeepAliveOverflow(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	cfg.KeepAlive = configKeepAliveOverflow
	err := cfg.Validate()
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTInvalidConfig, ec.Code)
}

// TestConfig_Validate_KeepAliveSubSecond covers R3 round-2 finding:
// uint16(time.Duration(500*time.Millisecond).Seconds()) == 0 would silently
// disable the broker KeepAlive at the wire. Validate must reject < 1s
// before Open's uint16 cast.
func TestConfig_Validate_KeepAliveSubSecond(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	cfg.KeepAlive = configKeepAliveSubSecond
	err := cfg.Validate()
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTInvalidConfig, ec.Code)
}

// TestConfig_Validate_SessionExpirySubSecond covers R3 round-2 finding:
// SessionExpiry == 500ms requested persistent session but uint32 floor
// truncation drops it to 0 (clean session) on the wire. Validate must
// reject SessionExpiry ∈ (0, 1s).
func TestConfig_Validate_SessionExpirySubSecond(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	cfg.SessionExpiry = configSessionExpirySubSecond
	err := cfg.Validate()
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTInvalidConfig, ec.Code)
}

// TestConfig_Validate_SessionExpiryNegative verifies negative SessionExpiry
// is rejected.
func TestConfig_Validate_SessionExpiryNegative(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	cfg.SessionExpiry = configNegSessionExpiry
	err := cfg.Validate()
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTInvalidConfig, ec.Code)
}

// TestConfig_Validate_ZeroConnectTimeout verifies ConnectTimeout > 0.
func TestConfig_Validate_ZeroConnectTimeout(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	cfg.ConnectTimeout = 0
	err := cfg.Validate()
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTInvalidConfig, ec.Code)
}

// TestConfig_Validate_NegativeConnectTimeout verifies negative timeout is rejected.
func TestConfig_Validate_NegativeConnectTimeout(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	cfg.ConnectTimeout = configNegTimeout
	err := cfg.Validate()
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTInvalidConfig, ec.Code)
}

// TestConfig_Validate_ZeroKeepAlive verifies KeepAlive > 0.
func TestConfig_Validate_ZeroKeepAlive(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	cfg.KeepAlive = 0
	err := cfg.Validate()
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTInvalidConfig, ec.Code)
}

// TestConfig_Validate_BackoffZeroBaseDelay verifies Backoff.BaseDelay > 0.
func TestConfig_Validate_BackoffZeroBaseDelay(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	cfg.Backoff.BaseDelay = 0
	err := cfg.Validate()
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTInvalidConfig, ec.Code)
}

// TestConfig_Validate_BackoffMaxDelayLessThanBase verifies MaxDelay >= BaseDelay.
func TestConfig_Validate_BackoffMaxDelayLessThanBase(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	cfg.Backoff.BaseDelay = testtime.D5s
	cfg.Backoff.MaxDelay = testtime.D1s // less than base
	err := cfg.Validate()
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTInvalidConfig, ec.Code)
}

// TestConfig_Validate_BackoffEqualBaseAndMax verifies that MaxDelay == BaseDelay is accepted.
func TestConfig_Validate_BackoffEqualBaseAndMax(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	cfg.Backoff.BaseDelay = testtime.D5s
	cfg.Backoff.MaxDelay = testtime.D5s // equal is fine
	require.NoError(t, cfg.Validate())
}

// TestConfig_BrokerURLs_ReturnsParsedURLs verifies that brokerURLs returns the
// parsed form of the Brokers slice.
func TestConfig_BrokerURLs_ReturnsParsedURLs(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	cfg.Brokers = []string{"tcp://a.example.com:1883", "tcp://b.example.com:1883"}
	urls, err := cfg.brokerURLs()
	require.NoError(t, err)
	require.Len(t, urls, 2)
	assert.Equal(t, "tcp", urls[0].Scheme)
	assert.Equal(t, "a.example.com:1883", urls[0].Host)
	assert.Equal(t, "tcp", urls[1].Scheme)
	assert.Equal(t, "b.example.com:1883", urls[1].Host)
}

// TestConfig_BrokerURLs_ReturnsErrorOnInvalidURL verifies brokerURLs propagates
// parse/validate errors.
func TestConfig_BrokerURLs_ReturnsErrorOnInvalidURL(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	cfg.Brokers = []string{"http://invalid:1883"}
	_, err := cfg.brokerURLs()
	require.Error(t, err)
}

// TestConfig_ParseBrokerURL_CredentialsRedacted verifies that a broker URL
// containing userinfo (user:pass@host) has credentials stripped before being
// placed in the Public Details channel, preventing exposure via 4xx wire or slog.
func TestConfig_ParseBrokerURL_CredentialsRedacted(t *testing.T) {
	t.Parallel()
	// Unsupported scheme so we hit the error path — credentials must be redacted.
	cfg := validConfig(t)
	cfg.Brokers = []string{"http://admin:supersecret@broker.example.com:1883"}
	err := cfg.Validate()
	require.Error(t, err)

	// The error string (code + message) must not contain the raw password.
	assert.NotContains(t, err.Error(), "supersecret",
		"password must not appear in error string")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTInvalidConfig, ec.Code)

	// Inspect the Details slice directly for the broker key.
	// redactConnectURL produces "http://xxxxx@broker.example.com:1883" for
	// this URL, preserving the host but masking credentials.
	found := false
	for _, d := range ec.Details {
		if d.Key() == "broker" {
			v := fmt.Sprintf("%v", d.Value())
			// Password must be redacted (url.URL.Redacted() replaces it with xxxxx).
			assert.NotContains(t, v, "supersecret",
				"broker detail must not contain password")
			// Host should still be visible so operators can identify the broker.
			assert.Contains(t, v, "broker.example.com",
				"host should still appear in redacted URL")
			found = true
		}
	}
	assert.True(t, found, "no 'broker' detail found in error details")
}

// TestConfig_Validate_MultipleBrokersMixed verifies that one invalid broker
// among valid ones fails validation. The first broker uses a loopback host
// so it passes; the second uses an unsupported scheme and fails.
func TestConfig_Validate_MultipleBrokersMixed(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	cfg.Brokers = []string{"tcp://127.0.0.1:1883", "http://bad:1883"}
	err := cfg.Validate()
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTInvalidConfig, ec.Code)
}

// TestConfig_Validate_MaximumPacketSize_Accepted verifies that
// MaximumPacketSize is preserved through Validate (wired into the CONNECT
// packet via ConnectPacketBuilder at Open time; see connection.go).
func TestConfig_Validate_MaximumPacketSize_Accepted(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	cfg.MaximumPacketSize = 1024 * 1024
	require.NoError(t, cfg.Validate())
}

// TestConfig_Validate_PublishTimeout_Negative verifies that a negative
// PublishTimeout is rejected by Validate.
func TestConfig_Validate_PublishTimeout_Negative(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	cfg.PublishTimeout = testtime.DNeg1s
	err := cfg.Validate()
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTInvalidConfig, ec.Code)
	assert.Contains(t, ec.Message, "PublishTimeout")
}

// TestConfig_Validate_PublishTimeout_Zero verifies that PublishTimeout == 0
// is accepted (means no adapter-imposed timeout; caller ctx deadline applies).
func TestConfig_Validate_PublishTimeout_Zero(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	cfg.PublishTimeout = 0
	require.NoError(t, cfg.Validate())
}

// TestConfig_Validate_PublishTimeout_Positive verifies that a positive
// PublishTimeout is accepted.
func TestConfig_Validate_PublishTimeout_Positive(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	cfg.PublishTimeout = testtime.D5s
	require.NoError(t, cfg.Validate())
}
