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

// validClientID returns a ClientID for use in tests.
func mustClientID(t *testing.T) ClientID {
	t.Helper()
	cid, err := ParseClientID("testcell", "pub")
	require.NoError(t, err)
	return cid
}

// validConfig returns a Config that passes Validate.
func validConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		ClientID:       mustClientID(t),
		Brokers:        []string{"tcp://localhost:1883"},
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
// when a TLS config is provided.
func TestConfig_Validate_TLSSchemeWithTLSConfig(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	cfg.Brokers = []string{"tls://broker.example.com:8883"}
	cfg.TLS = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // test only
	require.NoError(t, cfg.Validate())
}

// TestConfig_Validate_AllValidSchemes verifies all non-TLS scheme variants are accepted.
func TestConfig_Validate_AllValidSchemes(t *testing.T) {
	t.Parallel()
	for _, scheme := range []string{"tcp", "mqtt", "ws"} {
		scheme := scheme
		t.Run(scheme, func(t *testing.T) {
			t.Parallel()
			cfg := validConfig(t)
			cfg.Brokers = []string{scheme + "://broker.example.com:1883"}
			require.NoError(t, cfg.Validate())
		})
	}
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
// among valid ones fails validation.
func TestConfig_Validate_MultipleBrokersMixed(t *testing.T) {
	t.Parallel()
	cfg := validConfig(t)
	cfg.Brokers = []string{"tcp://good:1883", "http://bad:1883"}
	err := cfg.Validate()
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTInvalidConfig, ec.Code)
}
