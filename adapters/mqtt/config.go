package mqtt

import (
	"crypto/tls"
	"log/slog"
	"net/url"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/redaction"
	"github.com/ghbvf/gocell/pkg/secutil"
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

// maxKeepAliveDuration is the upper bound for KeepAlive — autopaho's
// ClientConfig.KeepAlive is uint16 (seconds), so values >65535s would silently
// wrap when converted. Validate rejects them.
const maxKeepAliveDuration = time.Duration(65535) * time.Second

// maxSessionExpiryDuration is the upper bound for SessionExpiry —
// autopaho's ClientConfig.SessionExpiryInterval is uint32 (seconds), so
// values >math.MaxUint32 seconds would silently wrap when converted.
const maxSessionExpiryDuration = time.Duration(4294967295) * time.Second

// Error message constants (MESSAGE-CONST-LITERAL-01: all messages are const literals).
const (
	msgConfigClientIDRequired         = "mqtt: config ClientID required"
	msgConfigNoBrokers                = "mqtt: config requires at least one broker"
	msgConfigBadBrokerURL             = "mqtt: broker URL is invalid or uses an unsupported scheme"
	msgConfigTLSRequired              = "mqtt: TLS config required for tls broker"
	msgConfigTLSInsecureSkipVerify    = "mqtt: TLS InsecureSkipVerify is forbidden (certificate verification must not be disabled)"
	msgConfigTLSMinVersion            = "mqtt: TLS MinVersion below TLS 1.2 is forbidden (downgrade protection)"
	msgConfigPlaintextRemote          = "mqtt: plaintext broker scheme requires loopback host"
	msgConfigConnectTimeout           = "mqtt: ConnectTimeout must be > 0"
	msgConfigKeepAlive                = "mqtt: KeepAlive must be > 0"
	msgConfigKeepAliveBelowSecond     = "mqtt: KeepAlive must be >= 1s (uint16 seconds wire field truncates sub-second values to 0)"
	msgConfigKeepAliveOverflow        = "mqtt: KeepAlive exceeds uint16 seconds (65535s)"
	msgConfigSessionExpiryBelowSecond = "mqtt: SessionExpiry > 0 must be >= 1s (uint32 seconds wire field truncates sub-second values to 0)"
	msgConfigSessionExpiryRange       = "mqtt: SessionExpiry must be >= 0 and <= uint32 max seconds"
	msgConfigBackoffInvalid           = "mqtt: Backoff.BaseDelay must be > 0 and MaxDelay >= BaseDelay"
	msgConfigPublishTimeoutNegative   = "mqtt: PublishTimeout must be >= 0"

	// PublicDetail key constants — extracted per go-standards.md
	// "同义字符串重复 ≥ 3 次抽常量". keepAlive / sessionExpiry / broker
	// are the keys surfaced in 4xx error details for operator diagnosis.
	detailKeyBroker        = "broker"
	detailKeyKeepAlive     = "keepAlive"
	detailKeySessionExpiry = "sessionExpiry"
	detailKeyMax           = "max"
	detailKeyMin           = "min"
)

// AuthConfig holds optional MQTT broker authentication credentials.
type AuthConfig struct {
	Username string
	Password []byte // autopaho ConnectPassword is []byte
}

// LogValue implements slog.LogValuer so the broker password is never emitted in
// structured logs. Any slog call that resolves an AuthConfig (directly, or as a
// nested Attr) sees the username and a redacted password placeholder instead of
// the raw bytes. Defensive: AuthConfig is not logged on any current path, but
// this seals the credential against future logging wiring.
func (a AuthConfig) LogValue() slog.Value {
	pw := ""
	if len(a.Password) > 0 {
		pw = redaction.Mask
	}
	return slog.GroupValue(
		slog.String("username", a.Username),
		slog.String("password", pw),
	)
}

// BackoffConfig controls reconnect back-off behavior.
type BackoffConfig struct {
	BaseDelay time.Duration // first retry delay
	MaxDelay  time.Duration // cap; must be >= BaseDelay
}

// Config holds all configuration required to construct an MQTT connection.
// Populate via struct literal; Open will call Validate before constructing
// the underlying autopaho ClientConfig — callers do not need to call Validate
// explicitly (but may, e.g. in CLI flag binding paths).
//
// Zero values are invalid for most fields — Validate reports all failures with
// typed errcode details so callers can surface them in structured logs without
// PII leaking into messages (MESSAGE-CONST-LITERAL-01).
type Config struct {
	ClientID      ClientID      // sealed type from clientid.go; zero value invalid
	Brokers       []string      // e.g. "tcp://127.0.0.1:1883", "tls://broker.example.com:8883"
	TLS           *tls.Config   // optional; required when any broker uses tls/ssl/mqtts/wss scheme
	SessionExpiry time.Duration // 0 = clean session
	Auth          AuthConfig
	Backoff       BackoffConfig
	// MaximumPacketSize controls both the INBOUND limit advertised to the broker
	// (via CONNECT packet Properties) AND the OUTBOUND client-side guard in
	// Publisher.Publish (payloads exceeding this size return
	// ErrAdapterMQTTPayloadTooLarge immediately without network round-trip).
	//
	// 0 = no client-declared limit (broker default applies) AND no outbound
	// guard in Publisher.
	MaximumPacketSize uint32
	ConnectTimeout    time.Duration // per-attempt; must be > 0
	KeepAlive         time.Duration // > 0, <= 65535s (uint16 wire field)
	// PublishTimeout caps the wall-clock time for a single Publish call (per-
	// publish; the publisher's WithTimeout child ctx fires after this duration).
	// 0 = no adapter-imposed timeout — Publisher uses the caller-provided ctx's
	// deadline as-is. < 0 rejected by Validate.
	PublishTimeout time.Duration
}

// Validate checks all fields for internal consistency and returns the first
// validation error encountered. Each error carries ErrAdapterMQTTInvalidConfig
// with relevant public details so the caller can surface structured diagnostics.
//
// Open calls Validate automatically; callers do not need to invoke it directly.
// The MQTT-CONFIG-VALIDATE-FIRST-01 archtest locks the Open-side form so the
// gate cannot be silently removed.
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
	if err := c.validateBackoff(); err != nil {
		return err
	}
	return c.validatePublishTimeout()
}

// validateBrokers checks the Brokers slice and each individual broker URL.
// Plaintext schemes (tcp/mqtt/ws) require a loopback host — enforced via
// pkg/secutil.ValidateTLSEndpoint which is the single source of truth for
// "is this remote endpoint TLS-secured?" across all adapters (redis / s3 /
// oidc / vault / mqtt). TLS schemes (tls/ssl/mqtts/wss) are accepted for any
// host but require c.TLS != nil.
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
			continue
		}
		// Plaintext scheme — require loopback host via secutil shared validator.
		// secutil accepts bare "host:port" (validateBareHostPort path) and
		// rejects non-loopback hosts.
		if secErr := secutil.ValidateTLSEndpoint(u.Host); secErr != nil {
			return errcode.Wrap(errcode.KindInvalid, ErrAdapterMQTTInvalidConfig,
				msgConfigPlaintextRemote, secErr,
				errcode.WithDetails(errcode.PublicString(detailKeyBroker, redactConnectURL(raw))))
		}
	}
	if needsTLS && c.TLS == nil {
		return errcode.New(errcode.KindInvalid, ErrAdapterMQTTInvalidConfig, msgConfigTLSRequired)
	}
	// Fail-closed: a TLS config that disables certificate verification is never
	// acceptable, regardless of scheme. InsecureSkipVerify would let a MITM
	// present any certificate; reject it at construction rather than shipping a
	// silently-insecure broker connection.
	if c.TLS != nil && c.TLS.InsecureSkipVerify {
		return errcode.New(errcode.KindInvalid, ErrAdapterMQTTInvalidConfig, msgConfigTLSInsecureSkipVerify)
	}
	// Fail-closed downgrade protection: MinVersion==0 means "unset" → Go's tls
	// default floor (TLS 1.2) applies, which is acceptable; reject only an
	// explicit weaker floor (TLS 1.0 / 1.1) that a caller mis-set.
	if c.TLS != nil && c.TLS.MinVersion != 0 && c.TLS.MinVersion < tls.VersionTLS12 {
		return errcode.New(errcode.KindInvalid, ErrAdapterMQTTInvalidConfig, msgConfigTLSMinVersion)
	}
	return nil
}

// validateTimings checks ConnectTimeout, KeepAlive, and SessionExpiry against
// both the upper bound (uint16/uint32 wire field overflow) AND the lower
// bound (sub-second values that would floor-truncate to 0 at the
// uint16(d.Seconds()) / uint32(d.Seconds()) conversion site in Open). The
// "ms passes Validate but wires 0" failure mode is silent — a 500ms KeepAlive
// configured for a chatty client would actually disable KeepAlive (broker
// never times out idle), so Validate fails-closed at < 1s.
func (c Config) validateTimings() error {
	if c.ConnectTimeout <= 0 {
		return errcode.New(errcode.KindInvalid, ErrAdapterMQTTInvalidConfig, msgConfigConnectTimeout,
			errcode.WithDetails(errcode.PublicDuration("connectTimeout", c.ConnectTimeout)))
	}
	if c.KeepAlive <= 0 {
		return errcode.New(errcode.KindInvalid, ErrAdapterMQTTInvalidConfig, msgConfigKeepAlive,
			errcode.WithDetails(errcode.PublicDuration(detailKeyKeepAlive, c.KeepAlive)))
	}
	if c.KeepAlive < time.Second {
		return errcode.New(errcode.KindInvalid, ErrAdapterMQTTInvalidConfig, msgConfigKeepAliveBelowSecond,
			errcode.WithDetails(
				errcode.PublicDuration(detailKeyKeepAlive, c.KeepAlive),
				errcode.PublicDuration(detailKeyMin, time.Second),
			))
	}
	if c.KeepAlive > maxKeepAliveDuration {
		return errcode.New(errcode.KindInvalid, ErrAdapterMQTTInvalidConfig, msgConfigKeepAliveOverflow,
			errcode.WithDetails(
				errcode.PublicDuration(detailKeyKeepAlive, c.KeepAlive),
				errcode.PublicDuration(detailKeyMax, maxKeepAliveDuration),
			))
	}
	if c.SessionExpiry < 0 || c.SessionExpiry > maxSessionExpiryDuration {
		return errcode.New(errcode.KindInvalid, ErrAdapterMQTTInvalidConfig, msgConfigSessionExpiryRange,
			errcode.WithDetails(
				errcode.PublicDuration(detailKeySessionExpiry, c.SessionExpiry),
				errcode.PublicDuration(detailKeyMax, maxSessionExpiryDuration),
			))
	}
	// SessionExpiry > 0 must also be >= 1s to survive uint32 floor truncation —
	// 500ms would wire 0 and disable persistent session, defeating the caller's
	// intent. SessionExpiry == 0 (clean session) is the explicit non-persistent
	// path and stays accepted.
	if c.SessionExpiry > 0 && c.SessionExpiry < time.Second {
		return errcode.New(errcode.KindInvalid, ErrAdapterMQTTInvalidConfig, msgConfigSessionExpiryBelowSecond,
			errcode.WithDetails(
				errcode.PublicDuration(detailKeySessionExpiry, c.SessionExpiry),
				errcode.PublicDuration(detailKeyMin, time.Second),
			))
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

// validatePublishTimeout checks that PublishTimeout is non-negative.
// 0 is accepted — it means the Publisher uses the caller-provided ctx deadline
// as-is with no additional adapter-imposed timeout. Negative values are always
// rejected because they would immediately cancel any publish ctx.
func (c Config) validatePublishTimeout() error {
	if c.PublishTimeout < 0 {
		return errcode.New(errcode.KindInvalid, ErrAdapterMQTTInvalidConfig,
			msgConfigPublishTimeoutNegative,
			errcode.WithDetails(errcode.PublicDuration("publishTimeout", c.PublishTimeout)))
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
//
// The "broker" detail is redacted via redactConnectURL before being placed in
// the Public Details channel so that URLs with userinfo (tcp://user:pass@host)
// do not leak credentials into 4xx responses or slog (security: PII/credential
// redaction, MESSAGE-CONST-LITERAL-01 / observability.md).
func parseBrokerURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || !validBrokerSchemes[u.Scheme] || u.Host == "" {
		return nil, errcode.New(
			errcode.KindInvalid, ErrAdapterMQTTInvalidConfig,
			msgConfigBadBrokerURL,
			errcode.WithDetails(errcode.PublicString(detailKeyBroker, redactConnectURL(raw))),
		)
	}
	return u, nil
}
