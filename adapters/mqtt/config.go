package mqtt

import (
	"crypto/tls"
	"net/url"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// validBrokerSchemes is the closed set of URL schemes accepted by autopaho.
// ref: eclipse/paho.golang autopaho/auto.go — NewConnection scheme check.
var validBrokerSchemes = map[string]bool{
	"tcp":   true,
	"mqtt":  true,
	"tls":   true,
	"ssl":   true,
	"mqtts": true,
	"ws":    true,
	"wss":   true,
}

// tlsRequiredSchemes lists schemes that mandate a non-nil TLS config.
var tlsRequiredSchemes = map[string]bool{
	"tls":   true,
	"ssl":   true,
	"mqtts": true,
	"wss":   true,
}

// Error message constants (MESSAGE-CONST-LITERAL-01: all messages are const literals).
const (
	msgConfigClientIDRequired = "mqtt: config ClientID required"
	msgConfigNoBrokers        = "mqtt: config requires at least one broker"
	msgConfigBadBrokerURL     = "mqtt: broker URL is invalid or uses an unsupported scheme"
	msgConfigTLSRequired      = "mqtt: TLS config required for tls broker"
	msgConfigConnectTimeout   = "mqtt: ConnectTimeout must be > 0"
	msgConfigKeepAlive        = "mqtt: KeepAlive must be > 0"
	msgConfigBackoffInvalid   = "mqtt: Backoff.BaseDelay must be > 0 and MaxDelay >= BaseDelay"
)

// AuthConfig holds optional MQTT broker authentication credentials.
type AuthConfig struct {
	Username string
	Password []byte // autopaho ConnectPassword is []byte
}

// BackoffConfig controls reconnect back-off behavior.
type BackoffConfig struct {
	BaseDelay time.Duration // first retry delay
	MaxDelay  time.Duration // cap; must be >= BaseDelay
}

// Config holds all configuration required to construct an MQTT connection.
// Populate via struct literal; call Validate before passing to the connection
// constructor (Batch B3).
//
// Zero values are invalid for most fields — Validate reports all failures with
// typed errcode details so callers can surface them in structured logs without
// PII leaking into messages (MESSAGE-CONST-LITERAL-01).
type Config struct {
	ClientID          ClientID      // sealed type from clientid.go (B1); zero value invalid
	Brokers           []string      // e.g. "tcp://host:1883", "tls://host:8883"
	TLS               *tls.Config   // optional; required when any broker uses tls/ssl/mqtts/wss scheme
	SessionExpiry     time.Duration // 0 = clean session
	Auth              AuthConfig
	Backoff           BackoffConfig
	MaximumPacketSize uint32        // 0 = broker default
	ConnectTimeout    time.Duration // per-attempt; must be > 0
	KeepAlive         time.Duration // must be > 0
}

// Validate checks all fields for internal consistency and returns the first
// validation error encountered. Each error carries ErrAdapterMQTTInvalidConfig
// with relevant public details so the caller can surface structured diagnostics.
//
// Cognitive-complexity budget: split into validateBrokers + validateTimings +
// validateBackoff helpers to stay ≤ 15 per function.
func (c Config) Validate() error {
	if c.ClientID.String() == "" {
		return errcode.New(errcode.KindInvalid, ErrAdapterMQTTInvalidConfig, msgConfigClientIDRequired)
	}
	if err := c.validateBrokers(); err != nil {
		return err
	}
	if err := c.validateTimings(); err != nil {
		return err
	}
	return c.validateBackoff()
}

// validateBrokers checks the Brokers slice and each individual broker URL.
func (c Config) validateBrokers() error {
	if len(c.Brokers) == 0 {
		return errcode.New(errcode.KindInvalid, ErrAdapterMQTTInvalidConfig, msgConfigNoBrokers)
	}
	needsTLS := false
	for _, raw := range c.Brokers {
		u, err := parseBrokerURL(raw)
		if err != nil {
			return err
		}
		if tlsRequiredSchemes[u.Scheme] {
			needsTLS = true
		}
	}
	if needsTLS && c.TLS == nil {
		return errcode.New(errcode.KindInvalid, ErrAdapterMQTTInvalidConfig, msgConfigTLSRequired)
	}
	return nil
}

// validateTimings checks ConnectTimeout and KeepAlive.
func (c Config) validateTimings() error {
	if c.ConnectTimeout <= 0 {
		return errcode.New(errcode.KindInvalid, ErrAdapterMQTTInvalidConfig, msgConfigConnectTimeout,
			errcode.WithDetails(errcode.PublicDuration("connectTimeout", c.ConnectTimeout)))
	}
	if c.KeepAlive <= 0 {
		return errcode.New(errcode.KindInvalid, ErrAdapterMQTTInvalidConfig, msgConfigKeepAlive,
			errcode.WithDetails(errcode.PublicDuration("keepAlive", c.KeepAlive)))
	}
	return nil
}

// validateBackoff checks the Backoff sub-config.
func (c Config) validateBackoff() error {
	if c.Backoff.BaseDelay <= 0 || c.Backoff.MaxDelay < c.Backoff.BaseDelay {
		return errcode.New(errcode.KindInvalid, ErrAdapterMQTTInvalidConfig, msgConfigBackoffInvalid,
			errcode.WithDetails(
				errcode.PublicDuration("baseDelay", c.Backoff.BaseDelay),
				errcode.PublicDuration("maxDelay", c.Backoff.MaxDelay),
			))
	}
	return nil
}

// brokerURLs parses each entry in Brokers and returns the resulting []*url.URL.
// Validation logic is shared with Validate via parseBrokerURL.
func (c Config) brokerURLs() ([]*url.URL, error) {
	out := make([]*url.URL, 0, len(c.Brokers))
	for _, raw := range c.Brokers {
		u, err := parseBrokerURL(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}

// parseBrokerURL parses raw into a *url.URL and validates the scheme and host.
// Returns ErrAdapterMQTTInvalidConfig on any failure.
func parseBrokerURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || !validBrokerSchemes[u.Scheme] || u.Host == "" {
		return nil, errcode.New(
			errcode.KindInvalid, ErrAdapterMQTTInvalidConfig,
			msgConfigBadBrokerURL,
			errcode.WithDetails(errcode.PublicString("broker", raw)),
		)
	}
	return u, nil
}
