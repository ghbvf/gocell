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

// validBroker is a loopback IP literal (127.0.0.1) so the
// secutil.ValidateTLSEndpoint plaintext check accepts it — "localhost" is
// intentionally NOT accepted because it is a DNS name.
const validBroker = "tcp://127.0.0.1:1883"

// mustClientID returns a ClientID for use in tests.
func mustClientID(t *testing.T) ClientID {
	t.Helper()
	cid, err := ParseEphemeralClientID("testcell", "pub")
	require.NoError(t, err)
	return cid
}

// newTestConfig builds a Config through the sealed NewConfig funnel with a valid
// clientID + loopback broker, applying opts. Most validation tests pass a single
// boundary-violating option (or bad positional broker) and assert the returned
// error; this is the public construction path callers actually hit.
func newTestConfig(t *testing.T, opts ...ConfigOption) (Config, error) {
	t.Helper()
	return NewConfig(mustClientID(t), []string{validBroker}, opts...)
}

// mustValidConfig returns a valid sealed Config (NewConfig must succeed). Used by
// the brokerURLs white-box tests, which then mutate the unexported brokers field
// directly to exercise an internal parse path NewConfig itself would reject.
func mustValidConfig(t *testing.T) Config {
	t.Helper()
	cfg, err := newTestConfig(t)
	require.NoError(t, err)
	return cfg
}

// assertInvalidConfig asserts err carries the ErrAdapterMQTTInvalidConfig code
// and returns the *errcode.Error so message/detail-specific tests can inspect it.
func assertInvalidConfig(t *testing.T, err error) *errcode.Error {
	t.Helper()
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTInvalidConfig, ec.Code)
	return ec
}

// requireInvalidConfig is the void form for the common case (no message/detail
// inspection), so callers do not trip errcheck on an ignored *errcode.Error.
func requireInvalidConfig(t *testing.T, err error) {
	t.Helper()
	_ = assertInvalidConfig(t, err)
}

// ─── NewConfig constructor behavior ───────────────────────────────────────────

// TestNewConfig_HappyPath covers the minimal valid construction (clientID +
// loopback broker, all timing/backoff knobs defaulted).
func TestNewConfig_HappyPath(t *testing.T) {
	t.Parallel()
	_, err := newTestConfig(t)
	require.NoError(t, err)
}

// TestNewConfig_AppliesDefaults verifies the constructor seeds the timing and
// backoff defaults when no overriding option is supplied (white-box: the fields
// are unexported, so this asserts the sealed values directly).
func TestNewConfig_AppliesDefaults(t *testing.T) {
	t.Parallel()
	cfg, err := newTestConfig(t)
	require.NoError(t, err)
	assert.Equal(t, defaultConnectTimeout, cfg.connectTimeout)
	assert.Equal(t, defaultConnectDeadline, cfg.connectDeadline)
	assert.Equal(t, defaultKeepAlive, cfg.keepAlive)
	assert.Equal(t, defaultBackoff, cfg.backoff)
}

// TestNewConfig_OptionsOverrideDefaults verifies a With* option overrides the
// corresponding default.
func TestNewConfig_OptionsOverrideDefaults(t *testing.T) {
	t.Parallel()
	cfg, err := NewConfig(mustClientID(t), []string{validBroker},
		WithConnectTimeout(testtime.D5s),
		WithConnectDeadline(testtime.D30s),
		WithKeepAlive(testtime.D5s),
		WithBackoff(BackoffConfig{BaseDelay: testtime.D1s, MaxDelay: testtime.D5s}),
		WithMaximumPacketSize(2048),
		WithPublishTimeout(testtime.D5s),
	)
	require.NoError(t, err)
	assert.Equal(t, testtime.D5s, cfg.connectTimeout)
	assert.Equal(t, testtime.D30s, cfg.connectDeadline)
	assert.Equal(t, testtime.D5s, cfg.keepAlive)
	assert.Equal(t, BackoffConfig{BaseDelay: testtime.D1s, MaxDelay: testtime.D5s}, cfg.backoff)
	assert.Equal(t, uint32(2048), cfg.maximumPacketSize)
	assert.Equal(t, testtime.D5s, cfg.publishTimeout)
}

// TestNewConfig_DefensivelyCopiesBrokers verifies that mutating the caller's
// slice after construction cannot change the validated transport list — the
// seal would be meaningless if a caller could swap in an unvalidated broker
// (e.g. a remote plaintext endpoint) after NewConfig ran its scheme/host checks.
func TestNewConfig_DefensivelyCopiesBrokers(t *testing.T) {
	t.Parallel()
	brokers := []string{validBroker}
	cfg, err := NewConfig(mustClientID(t), brokers)
	require.NoError(t, err)
	brokers[0] = "tcp://attacker.example.com:1883"
	require.Len(t, cfg.brokers, 1)
	assert.Equal(t, validBroker, cfg.brokers[0])
}

// TestNewConfig_ReturnsZeroConfigOnError verifies the constructor returns the
// zero Config (not a partially-populated one) alongside the validation error, so
// an ignored error cannot leak a half-built Config.
func TestNewConfig_ReturnsZeroConfigOnError(t *testing.T) {
	t.Parallel()
	cfg, err := NewConfig(ClientID{}, []string{validBroker})
	require.Error(t, err)
	assert.Equal(t, Config{}, cfg)
}

// ─── Validation via the NewConfig funnel ──────────────────────────────────────

// TestNewConfig_ConnectDeadline covers the bootstrap connect-deadline budget: it
// is mandatory and must be > 0. A zero or negative value would let the
// first-connection wait fall back to the (effectively unbounded) lifecycle ctx —
// the exact #1388 hang this field exists to prevent — so NewConfig fails-closed.
func TestNewConfig_ConnectDeadline(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		dur     time.Duration
		wantErr bool
	}{
		{"zero-rejected", 0, true},
		{"negative-rejected", -testtime.D5s, true},
		{"positive-accepted", testtime.D10s, false},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := newTestConfig(t, WithConnectDeadline(tc.dur))
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			ec := assertInvalidConfig(t, err)
			// Assert the failure is specifically the ConnectDeadline gate, not an
			// unrelated field (e.g. validateTimings short-circuiting elsewhere).
			assert.Contains(t, ec.Message, "ConnectDeadline")
		})
	}
}

// TestNewConfig_ZeroClientID verifies that the zero-value ClientID is rejected.
func TestNewConfig_ZeroClientID(t *testing.T) {
	t.Parallel()
	_, err := NewConfig(ClientID{}, []string{validBroker})
	requireInvalidConfig(t, err)
}

// TestNewConfig_NoBrokers verifies the at-least-one-broker requirement.
func TestNewConfig_NoBrokers(t *testing.T) {
	t.Parallel()
	_, err := NewConfig(mustClientID(t), nil)
	requireInvalidConfig(t, err)
}

// TestNewConfig_EmptyBrokerSlice verifies an empty slice is also rejected.
func TestNewConfig_EmptyBrokerSlice(t *testing.T) {
	t.Parallel()
	_, err := NewConfig(mustClientID(t), []string{})
	requireInvalidConfig(t, err)
}

// TestNewConfig_InvalidBrokerScheme covers unsupported URL schemes.
func TestNewConfig_InvalidBrokerScheme(t *testing.T) {
	t.Parallel()
	_, err := NewConfig(mustClientID(t), []string{"http://localhost:1883"})
	requireInvalidConfig(t, err)
}

// TestNewConfig_MalformedBrokerURL ensures unparseable URLs are rejected.
func TestNewConfig_MalformedBrokerURL(t *testing.T) {
	t.Parallel()
	_, err := NewConfig(mustClientID(t), []string{"://noscheme"})
	requireInvalidConfig(t, err)
}

// TestNewConfig_BrokerMissingHost verifies that a URL without a host is rejected.
func TestNewConfig_BrokerMissingHost(t *testing.T) {
	t.Parallel()
	_, err := NewConfig(mustClientID(t), []string{"tcp://"})
	requireInvalidConfig(t, err)
}

// TestNewConfig_TLSSchemeWithoutTLSConfig verifies that a tls/ssl/mqtts/wss
// broker requires a non-nil TLS config.
func TestNewConfig_TLSSchemeWithoutTLSConfig(t *testing.T) {
	t.Parallel()
	for _, scheme := range []string{"tls", "ssl", "mqtts", "wss"} {
		scheme := scheme
		t.Run(scheme, func(t *testing.T) {
			t.Parallel()
			_, err := NewConfig(mustClientID(t), []string{scheme + "://broker.example.com:8883"})
			requireInvalidConfig(t, err)
		})
	}
}

// TestNewConfig_TLSSchemeWithTLSConfig verifies that tls brokers succeed when a
// verifying TLS config is provided.
func TestNewConfig_TLSSchemeWithTLSConfig(t *testing.T) {
	t.Parallel()
	_, err := NewConfig(mustClientID(t), []string{"tls://broker.example.com:8883"},
		WithTLS(&tls.Config{MinVersion: tls.VersionTLS12})) // verifying config (no InsecureSkipVerify)
	require.NoError(t, err)
}

// TestNewConfig_TLSInsecureSkipVerify_Rejected verifies that a TLS config with
// InsecureSkipVerify=true is rejected fail-closed, regardless of scheme.
func TestNewConfig_TLSInsecureSkipVerify_Rejected(t *testing.T) {
	t.Parallel()
	_, err := NewConfig(mustClientID(t), []string{"tls://broker.example.com:8883"},
		WithTLS(&tls.Config{InsecureSkipVerify: true})) //nolint:gosec // negative test: asserts this is rejected
	requireInvalidConfig(t, err)
}

// TestNewConfig_TLSMinVersion verifies the downgrade-protection floor: an
// explicit MinVersion below TLS 1.2 is rejected; unset (0 → Go default 1.2) and
// >= TLS 1.2 are accepted.
func TestNewConfig_TLSMinVersion(t *testing.T) {
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
			_, err := NewConfig(mustClientID(t), []string{"tls://broker.example.com:8883"},
				WithTLS(&tls.Config{MinVersion: tc.minVersion})) //nolint:gosec // 0 = unset → Go default TLS 1.2
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			requireInvalidConfig(t, err)
		})
	}
}

// TestNewConfig_AllValidSchemes verifies all plaintext scheme variants are
// accepted when the host is a loopback IP literal (dev/CI testcontainer
// exception). The same schemes with non-loopback hosts are rejected by
// TestNewConfig_PlaintextRemote_Rejected.
func TestNewConfig_AllValidSchemes(t *testing.T) {
	t.Parallel()
	for _, scheme := range []string{"tcp", "mqtt", "ws"} {
		scheme := scheme
		t.Run(scheme, func(t *testing.T) {
			t.Parallel()
			_, err := NewConfig(mustClientID(t), []string{scheme + "://127.0.0.1:1883"})
			require.NoError(t, err)
		})
	}
}

// TestNewConfig_PlaintextRemote_Rejected verifies that the plaintext schemes
// (tcp/mqtt/ws) require a loopback host. Remote hosts are rejected via
// pkg/secutil.ValidateTLSEndpoint (C1 F2 — fail-closed against silent credential
// exposure over plaintext).
func TestNewConfig_PlaintextRemote_Rejected(t *testing.T) {
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
			_, err := NewConfig(mustClientID(t), []string{tc.broker})
			requireInvalidConfig(t, err)
		})
	}
}

// TestNewConfig_KeepAliveOverflow verifies KeepAlive >65535s is rejected (uint16
// wire field upper bound).
func TestNewConfig_KeepAliveOverflow(t *testing.T) {
	t.Parallel()
	_, err := newTestConfig(t, WithKeepAlive(configKeepAliveOverflow))
	requireInvalidConfig(t, err)
}

// TestNewConfig_KeepAliveSubSecond covers R3 round-2 finding:
// uint16(time.Duration(500*time.Millisecond).Seconds()) == 0 would silently
// disable the broker KeepAlive at the wire. NewConfig must reject < 1s before
// Open's uint16 cast.
func TestNewConfig_KeepAliveSubSecond(t *testing.T) {
	t.Parallel()
	_, err := newTestConfig(t, WithKeepAlive(configKeepAliveSubSecond))
	requireInvalidConfig(t, err)
}

// TestNewConfig_SessionExpirySubSecond covers R3 round-2 finding: SessionExpiry
// == 500ms requested persistent session but uint32 floor truncation drops it to
// 0 (clean session) on the wire. NewConfig must reject SessionExpiry ∈ (0, 1s).
func TestNewConfig_SessionExpirySubSecond(t *testing.T) {
	t.Parallel()
	_, err := newTestConfig(t, WithSessionExpiry(configSessionExpirySubSecond))
	requireInvalidConfig(t, err)
}

// TestNewConfig_SessionExpiryNegative verifies negative SessionExpiry is rejected.
func TestNewConfig_SessionExpiryNegative(t *testing.T) {
	t.Parallel()
	_, err := newTestConfig(t, WithSessionExpiry(configNegSessionExpiry))
	requireInvalidConfig(t, err)
}

// TestNewConfig_ZeroConnectTimeout verifies ConnectTimeout > 0.
func TestNewConfig_ZeroConnectTimeout(t *testing.T) {
	t.Parallel()
	_, err := newTestConfig(t, WithConnectTimeout(0))
	requireInvalidConfig(t, err)
}

// TestNewConfig_NegativeConnectTimeout verifies negative timeout is rejected.
func TestNewConfig_NegativeConnectTimeout(t *testing.T) {
	t.Parallel()
	_, err := newTestConfig(t, WithConnectTimeout(configNegTimeout))
	requireInvalidConfig(t, err)
}

// TestNewConfig_ZeroKeepAlive verifies KeepAlive > 0.
func TestNewConfig_ZeroKeepAlive(t *testing.T) {
	t.Parallel()
	_, err := newTestConfig(t, WithKeepAlive(0))
	requireInvalidConfig(t, err)
}

// TestNewConfig_BackoffZeroBaseDelay verifies Backoff.BaseDelay > 0.
func TestNewConfig_BackoffZeroBaseDelay(t *testing.T) {
	t.Parallel()
	_, err := newTestConfig(t, WithBackoff(BackoffConfig{BaseDelay: 0, MaxDelay: testtime.D30s}))
	requireInvalidConfig(t, err)
}

// TestNewConfig_BackoffMaxDelayLessThanBase verifies MaxDelay >= BaseDelay.
func TestNewConfig_BackoffMaxDelayLessThanBase(t *testing.T) {
	t.Parallel()
	_, err := newTestConfig(t, WithBackoff(BackoffConfig{BaseDelay: testtime.D5s, MaxDelay: testtime.D1s}))
	requireInvalidConfig(t, err)
}

// TestNewConfig_BackoffEqualBaseAndMax verifies that MaxDelay == BaseDelay is accepted.
func TestNewConfig_BackoffEqualBaseAndMax(t *testing.T) {
	t.Parallel()
	_, err := newTestConfig(t, WithBackoff(BackoffConfig{BaseDelay: testtime.D5s, MaxDelay: testtime.D5s}))
	require.NoError(t, err)
}

// TestNewConfig_MaximumPacketSize_Accepted verifies that MaximumPacketSize is
// accepted (it is wired into the CONNECT packet via ConnectPacketBuilder at Open
// time; see connection.go).
func TestNewConfig_MaximumPacketSize_Accepted(t *testing.T) {
	t.Parallel()
	_, err := newTestConfig(t, WithMaximumPacketSize(1024*1024))
	require.NoError(t, err)
}

// TestNewConfig_PublishTimeout_Negative verifies that a negative PublishTimeout
// is rejected.
func TestNewConfig_PublishTimeout_Negative(t *testing.T) {
	t.Parallel()
	_, err := newTestConfig(t, WithPublishTimeout(testtime.DNeg1s))
	ec := assertInvalidConfig(t, err)
	assert.Contains(t, ec.Message, "PublishTimeout")
}

// TestNewConfig_PublishTimeout_Zero verifies that PublishTimeout == 0 is accepted
// (means no adapter-imposed timeout; caller ctx deadline applies).
func TestNewConfig_PublishTimeout_Zero(t *testing.T) {
	t.Parallel()
	_, err := newTestConfig(t, WithPublishTimeout(0))
	require.NoError(t, err)
}

// TestNewConfig_PublishTimeout_Positive verifies that a positive PublishTimeout
// is accepted.
func TestNewConfig_PublishTimeout_Positive(t *testing.T) {
	t.Parallel()
	_, err := newTestConfig(t, WithPublishTimeout(testtime.D5s))
	require.NoError(t, err)
}

// TestNewConfig_MultipleBrokersMixed verifies that one invalid broker among
// valid ones fails construction. The first broker uses a loopback host so it
// passes; the second uses an unsupported scheme and fails.
func TestNewConfig_MultipleBrokersMixed(t *testing.T) {
	t.Parallel()
	_, err := NewConfig(mustClientID(t), []string{validBroker, "http://bad:1883"})
	requireInvalidConfig(t, err)
}

// TestNewConfig_CredentialsRedacted verifies that a broker URL containing
// userinfo (user:pass@host) has credentials stripped before being placed in the
// Public Details channel, preventing exposure via 4xx wire or slog.
func TestNewConfig_CredentialsRedacted(t *testing.T) {
	t.Parallel()
	// Unsupported scheme so we hit the error path — credentials must be redacted.
	_, err := NewConfig(mustClientID(t), []string{"http://admin:supersecret@broker.example.com:1883"})

	// The error string (code + message) must not contain the raw password.
	assert.NotContains(t, err.Error(), "supersecret",
		"password must not appear in error string")

	ec := assertInvalidConfig(t, err)

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

// ─── brokerURLs internal helper (white-box) ───────────────────────────────────
//
// brokerURLs is an unexported method exercised by Open after validation. These
// tests build a valid sealed Config via NewConfig, then mutate the unexported
// brokers field directly (white-box, package mqtt) to reach parse states
// NewConfig itself rejects — there is no other way to feed brokerURLs an
// already-constructed Config carrying non-loopback or malformed brokers.

// TestConfig_BrokerURLs_ReturnsParsedURLs verifies that brokerURLs returns the
// parsed form of the brokers slice.
func TestConfig_BrokerURLs_ReturnsParsedURLs(t *testing.T) {
	t.Parallel()
	cfg := mustValidConfig(t)
	cfg.brokers = []string{"tcp://a.example.com:1883", "tcp://b.example.com:1883"}
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
	cfg := mustValidConfig(t)
	cfg.brokers = []string{"http://invalid:1883"}
	_, err := cfg.brokerURLs()
	require.Error(t, err)
}

// ─── Security: defensive copies ──────────────────────────────────────────────

// TestNewConfig_DefensivelyClonesTLS verifies that post-construction mutation of
// the caller's *tls.Config cannot weaken TLS settings that NewConfig validated.
// WithTLS must clone the config so InsecureSkipVerify/MinVersion changes are
// isolated to the caller's copy and do not affect the sealed Config.
func TestNewConfig_DefensivelyClonesTLS(t *testing.T) {
	t.Parallel()
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	cfg, err := NewConfig(mustClientID(t), []string{"tls://broker.example.com:8883"}, WithTLS(tlsCfg))
	require.NoError(t, err)

	// Mutate the caller's copy after construction.
	tlsCfg.InsecureSkipVerify = true
	tlsCfg.MinVersion = tls.VersionTLS10

	// Sealed Config must retain the validated values.
	assert.False(t, cfg.tlsConfig.InsecureSkipVerify,
		"InsecureSkipVerify must not reflect caller mutation after construction")
	assert.Equal(t, uint16(tls.VersionTLS12), cfg.tlsConfig.MinVersion,
		"MinVersion must not reflect caller mutation after construction")
}

// TestNewConfig_DefensivelyCopiesAuthPassword verifies that post-construction
// mutation of the caller's password slice cannot corrupt the stored credential.
// WithAuth must copy the Password bytes so later caller mutations are isolated.
func TestNewConfig_DefensivelyCopiesAuthPassword(t *testing.T) {
	t.Parallel()
	pw := []byte("secret")
	cfg, err := NewConfig(mustClientID(t), []string{validBroker}, WithAuth(AuthConfig{Username: "u", Password: pw}))
	require.NoError(t, err)

	// Mutate the caller's slice after construction.
	pw[0] = 'X'

	assert.Equal(t, []byte("secret"), cfg.auth.Password,
		"stored Password must not reflect caller mutation after construction")
}

// ─── SessionExpiry boundary + WithAuth round-trip ────────────────────────────

// configSessionExpiryOverflow is the smallest SessionExpiry value above
// maxSessionExpiryDuration; used to assert overflow is rejected.
const configSessionExpiryOverflow = maxSessionExpiryDuration + time.Second

// TestNewConfig_SessionExpiry_Accepted verifies that positive SessionExpiry
// values on and above the 1s floor are accepted.
func TestNewConfig_SessionExpiry_Accepted(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		dur  time.Duration
	}{
		{"1s-boundary", testtime.D1s},
		{"5s-interior", testtime.D5s},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := newTestConfig(t, WithSessionExpiry(tc.dur))
			require.NoError(t, err)
		})
	}
}

// TestNewConfig_SessionExpiry_OverflowRejected verifies that a SessionExpiry
// above the uint32-seconds ceiling (maxSessionExpiryDuration) is rejected.
func TestNewConfig_SessionExpiry_OverflowRejected(t *testing.T) {
	t.Parallel()
	_, err := newTestConfig(t, WithSessionExpiry(configSessionExpiryOverflow))
	requireInvalidConfig(t, err)
}

// TestNewConfig_OptionsOverrideDefaults_WithAuth extends the existing
// TestNewConfig_OptionsOverrideDefaults to prove WithAuth round-trips Username
// and Password correctly through the sealed constructor.
func TestNewConfig_OptionsOverrideDefaults_WithAuth(t *testing.T) {
	t.Parallel()
	cfg, err := NewConfig(mustClientID(t), []string{validBroker},
		WithAuth(AuthConfig{Username: "u", Password: []byte("p")}),
	)
	require.NoError(t, err)
	assert.Equal(t, "u", cfg.auth.Username)
	assert.Equal(t, "p", string(cfg.auth.Password))
}
