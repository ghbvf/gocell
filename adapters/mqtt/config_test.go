package mqtt

import (
	"crypto/tls"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
)

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
		ConnectTimeout: 5 * time.Second,
		KeepAlive:      30 * time.Second,
		Backoff: BackoffConfig{
			BaseDelay: 500 * time.Millisecond,
			MaxDelay:  30 * time.Second,
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
	cfg.ConnectTimeout = -1 * time.Second
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
	cfg.Backoff.BaseDelay = 5 * time.Second
	cfg.Backoff.MaxDelay = 1 * time.Second // less than base
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
	cfg.Backoff.BaseDelay = 5 * time.Second
	cfg.Backoff.MaxDelay = 5 * time.Second // equal is fine
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
