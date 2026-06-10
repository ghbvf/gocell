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
// wrap when converted. NewConfig rejects them.
const maxKeepAliveDuration = time.Duration(65535) * time.Second

// maxSessionExpiryDuration is the upper bound for SessionExpiry —
// autopaho's ClientConfig.SessionExpiryInterval is uint32 (seconds), so
// values >math.MaxUint32 seconds would silently wrap when converted.
const maxSessionExpiryDuration = time.Duration(4294967295) * time.Second

// Construction defaults — applied by NewConfig before options run, so the only
// compile-required inputs are clientID + brokers (identity + transport); every
// timing/backoff knob has a working default that survives validate(). Values
// mirror the production examples (examples/iotdevice). A With* option overrides
// the corresponding default.
//
// ref: adapters/rabbitmq/connection.go (*Config).setDefaults — same
// "construct with sensible defaults, then validate" precedent.
const (
	defaultConnectTimeout  = 10 * time.Second
	defaultConnectDeadline = 30 * time.Second
	defaultKeepAlive       = 30 * time.Second
)

// defaultBackoff is the reconnect back-off applied when WithBackoff is not
// supplied. A struct value cannot be a const, hence a package var; it is never
// mutated (NewConfig copies it into the Config by value).
var defaultBackoff = BackoffConfig{BaseDelay: 500 * time.Millisecond, MaxDelay: 30 * time.Second}

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
	msgConfigConnectDeadline          = "mqtt: ConnectDeadline must be > 0 (bootstrap first-connection wait budget)"
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

// Config holds all configuration required to construct an MQTT connection. It is
// a SEALED struct: every field is unexported, so an outside-package
// `mqtt.Config{...}` literal is structurally inexpressible. The only way to
// obtain a non-zero Config is NewConfig, which applies defaults, then options,
// then validates in its body — so a Config a caller can hold has always passed
// validation ("unvalidated Config" is unrepresentable).
//
// Field set, types, and unexported-ness are frozen by archtest
// MQTT-CONFIG-SEALED-FIELD-FROZEN-01 (A1 reflect freeze + A2 NewConfig
// construction allowlist). Changing the shape requires an ADR amendment.
//
// ref: errcode.PublicDetail / adapters/mqtt.ClientID sealed-value pattern.
type Config struct {
	clientID      ClientID      // sealed type from clientid.go; zero value invalid
	brokers       []string      // e.g. "tcp://127.0.0.1:1883", "tls://broker.example.com:8883"
	tlsConfig     *tls.Config   // optional; required when any broker uses tls/ssl/mqtts/wss scheme
	sessionExpiry time.Duration // 0 = clean session
	auth          AuthConfig
	backoff       BackoffConfig
	// maximumPacketSize controls both the INBOUND limit advertised to the broker
	// (via CONNECT packet Properties) AND the OUTBOUND client-side guard in
	// Publisher.Publish (payloads exceeding this size return
	// ErrAdapterMQTTPayloadTooLarge immediately without network round-trip).
	//
	// 0 = no client-declared limit (broker default applies) AND no outbound
	// guard in Publisher.
	maximumPacketSize uint32
	connectTimeout    time.Duration // per-attempt; must be > 0
	// connectDeadline bounds the bootstrap first-connection wait (Open blocks
	// until the first connection comes up OR this deadline elapses). It is
	// orthogonal to the per-attempt connectTimeout: connectTimeout caps a single
	// dial, connectDeadline caps the whole "wait for first success" window across
	// retries. Open derives a context.WithTimeout(ctx, connectDeadline) child for
	// the wait while binding the ConnectionManager to the lifecycle ctx, so a
	// broker that never comes up fails Open fast instead of hanging on an
	// unbounded root/app ctx (#1388). Must be > 0 — a zero/negative value would
	// re-collapse the wait onto the lifecycle ctx.
	connectDeadline time.Duration
	keepAlive       time.Duration // > 0, <= 65535s (uint16 wire field)
	// publishTimeout caps the wall-clock time for a single Publish call (per-
	// publish; the publisher's WithTimeout child ctx fires after this duration).
	// 0 = no adapter-imposed timeout — Publisher uses the caller-provided ctx's
	// deadline as-is. < 0 rejected by validate.
	publishTimeout time.Duration
}

// ConfigOption sets an optional Config field inside NewConfig. Required identity
// and transport (clientID, brokers) are positional arguments of NewConfig;
// everything else is an option, defaulted where a working zero is not available.
// Nil options are silently skipped by NewConfig.
type ConfigOption func(*Config)

// WithTLS sets the TLS config. Required when any broker uses a tls/ssl/mqtts/wss
// scheme; rejected (via validate) if it disables certificate verification or
// floors below TLS 1.2. The config is defensively cloned via (*tls.Config).Clone()
// so post-construction mutation of the caller's *tls.Config cannot weaken TLS
// settings (e.g. setting InsecureSkipVerify after NewConfig returned). Clone is
// nil-safe: WithTLS(nil) results in tlsConfig=nil (no TLS).
func WithTLS(c *tls.Config) ConfigOption { return func(cfg *Config) { cfg.tlsConfig = c.Clone() } }

// WithAuth sets broker authentication credentials (optional). The Password field
// is defensively copied so post-construction mutation of the caller's slice cannot
// corrupt the stored credential.
func WithAuth(a AuthConfig) ConfigOption {
	return func(cfg *Config) {
		if a.Password != nil {
			a.Password = append([]byte(nil), a.Password...)
		}
		cfg.auth = a
	}
}

// WithSessionExpiry sets the MQTT session expiry. 0 (default) = clean session;
// > 0 must be >= 1s (uint32 seconds wire field).
func WithSessionExpiry(d time.Duration) ConfigOption {
	return func(cfg *Config) { cfg.sessionExpiry = d }
}

// WithMaximumPacketSize sets the maximum packet size (inbound advertised limit
// + outbound publisher guard). 0 (default) = no client-declared limit.
func WithMaximumPacketSize(n uint32) ConfigOption {
	return func(cfg *Config) { cfg.maximumPacketSize = n }
}

// WithPublishTimeout sets the per-publish wall-clock cap. 0 (default) = no
// adapter-imposed timeout (caller ctx deadline applies); < 0 rejected.
func WithPublishTimeout(d time.Duration) ConfigOption {
	return func(cfg *Config) { cfg.publishTimeout = d }
}

// WithConnectTimeout overrides the per-attempt dial timeout (default 10s; > 0).
func WithConnectTimeout(d time.Duration) ConfigOption {
	return func(cfg *Config) { cfg.connectTimeout = d }
}

// WithConnectDeadline overrides the bootstrap first-connection wait budget
// (default 30s; > 0).
func WithConnectDeadline(d time.Duration) ConfigOption {
	return func(cfg *Config) { cfg.connectDeadline = d }
}

// WithKeepAlive overrides the MQTT keep-alive (default 30s; >= 1s, <= 65535s).
func WithKeepAlive(d time.Duration) ConfigOption {
	return func(cfg *Config) { cfg.keepAlive = d }
}

// WithBackoff overrides the reconnect back-off (default {500ms, 30s};
// BaseDelay > 0 and MaxDelay >= BaseDelay).
func WithBackoff(b BackoffConfig) ConfigOption { return func(cfg *Config) { cfg.backoff = b } }

// NewConfig is the sole constructor for a Config. It seeds the timing/backoff
// defaults, applies opts, then validates — returning a sealed, validated Config
// or an error (and a zero Config). clientID and brokers are the irreducible
// identity + transport inputs and are compile-required; everything else is an
// optional With* knob with a working default. Nil and empty brokers are both
// rejected (at-least-one-broker required). Nil options in opts are silently skipped.
//
// The brokers slice is defensively copied so a caller cannot mutate the
// validated transport list after construction (which would bypass the
// scheme/host checks validate ran).
//
// All option fields and their default / zero-semantics:
//
//   - connectTimeout  = 10s (per-attempt dial timeout; > 0 required)
//   - connectDeadline = 30s (bootstrap first-connection wait budget; > 0 required)
//   - keepAlive       = 30s (MQTT keep-alive; >= 1s, <= 65535s)
//   - backoff         = {BaseDelay: 500ms, MaxDelay: 30s} (reconnect back-off)
//   - tlsConfig       = nil (no TLS; required when any broker uses tls/ssl/mqtts/wss scheme)
//   - auth            = zero (no credentials)
//   - sessionExpiry   = 0 (clean session; > 0 must be >= 1s)
//   - maximumPacketSize = 0 (no client-declared limit; no outbound guard in Publisher)
//   - publishTimeout  = 0 (no adapter-imposed timeout; caller ctx deadline applies)
//
// This is the single sanctioned non-zero Config composite-literal site
// (MQTT-CONFIG-SEALED-FIELD-FROZEN-01/A2).
func NewConfig(clientID ClientID, brokers []string, opts ...ConfigOption) (Config, error) {
	c := Config{
		clientID:        clientID,
		brokers:         append([]string(nil), brokers...),
		connectTimeout:  defaultConnectTimeout,
		connectDeadline: defaultConnectDeadline,
		keepAlive:       defaultKeepAlive,
		backoff:         defaultBackoff,
	}
	for _, o := range opts {
		if o != nil {
			o(&c)
		}
	}
	if err := c.validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// validate checks all fields for internal consistency and returns the first
// validation error encountered. Each error carries ErrAdapterMQTTInvalidConfig
// with relevant public details so the caller can surface structured diagnostics.
//
// validate is unexported: NewConfig calls it on construction and Open calls it
// as a zero-value defense-in-depth guard (an ignored NewConfig error must not
// let a zero Config reach the autopaho wiring). External callers receive
// validation results as the NewConfig error.
//
// Short-circuit order: clientID → brokers → timings → backoff → publishTimeout.
// A caller fixing multiple errors will encounter them in this sequence.
//
// Cognitive-complexity budget: split into validateBrokers + validateTimings +
// validateBackoff helpers to stay ≤ 15 per function.
func (c Config) validate() error {
	if c.clientID.String() == "" {
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

// validateBrokers checks the brokers slice and each individual broker URL.
// Plaintext schemes (tcp/mqtt/ws) require a loopback host — enforced via
// pkg/secutil.ValidateTLSEndpoint which is the single source of truth for
// "is this remote endpoint TLS-secured?" across all adapters (redis / s3 /
// oidc / vault / mqtt). TLS schemes (tls/ssl/mqtts/wss) are accepted for any
// host but require c.tlsConfig != nil.
func (c Config) validateBrokers() error {
	if len(c.brokers) == 0 {
		return errcode.New(errcode.KindInvalid, ErrAdapterMQTTInvalidConfig, msgConfigNoBrokers)
	}
	needsTLS := false
	for _, raw := range c.brokers {
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
	if needsTLS && c.tlsConfig == nil {
		return errcode.New(errcode.KindInvalid, ErrAdapterMQTTInvalidConfig, msgConfigTLSRequired)
	}
	// Fail-closed: a TLS config that disables certificate verification is never
	// acceptable, regardless of scheme. InsecureSkipVerify would let a MITM
	// present any certificate; reject it at construction rather than shipping a
	// silently-insecure broker connection.
	if c.tlsConfig != nil && c.tlsConfig.InsecureSkipVerify {
		return errcode.New(errcode.KindInvalid, ErrAdapterMQTTInvalidConfig, msgConfigTLSInsecureSkipVerify)
	}
	// Fail-closed downgrade protection: MinVersion==0 means "unset" → Go's tls
	// default floor (TLS 1.2) applies, which is acceptable; reject only an
	// explicit weaker floor (TLS 1.0 / 1.1) that a caller mis-set.
	if c.tlsConfig != nil && c.tlsConfig.MinVersion != 0 && c.tlsConfig.MinVersion < tls.VersionTLS12 {
		return errcode.New(errcode.KindInvalid, ErrAdapterMQTTInvalidConfig, msgConfigTLSMinVersion)
	}
	return nil
}

// validateTimings checks ConnectTimeout, KeepAlive, and SessionExpiry against
// both the upper bound (uint16/uint32 wire field overflow) AND the lower
// bound (sub-second values that would floor-truncate to 0 at the
// uint16(d.Seconds()) / uint32(d.Seconds()) conversion site in Open). The
// "ms passes validate but wires 0" failure mode is silent — a 500ms KeepAlive
// configured for a chatty client would actually disable KeepAlive (broker
// never times out idle), so validate fails-closed at < 1s.
func (c Config) validateTimings() error {
	if c.connectTimeout <= 0 {
		return errcode.New(errcode.KindInvalid, ErrAdapterMQTTInvalidConfig, msgConfigConnectTimeout,
			errcode.WithDetails(errcode.PublicDuration("connectTimeout", c.connectTimeout)))
	}
	if c.connectDeadline <= 0 {
		return errcode.New(errcode.KindInvalid, ErrAdapterMQTTInvalidConfig, msgConfigConnectDeadline,
			errcode.WithDetails(errcode.PublicDuration("connectDeadline", c.connectDeadline)))
	}
	if c.keepAlive <= 0 {
		return errcode.New(errcode.KindInvalid, ErrAdapterMQTTInvalidConfig, msgConfigKeepAlive,
			errcode.WithDetails(errcode.PublicDuration(detailKeyKeepAlive, c.keepAlive)))
	}
	if c.keepAlive < time.Second {
		return errcode.New(errcode.KindInvalid, ErrAdapterMQTTInvalidConfig, msgConfigKeepAliveBelowSecond,
			errcode.WithDetails(
				errcode.PublicDuration(detailKeyKeepAlive, c.keepAlive),
				errcode.PublicDuration(detailKeyMin, time.Second),
			))
	}
	if c.keepAlive > maxKeepAliveDuration {
		return errcode.New(errcode.KindInvalid, ErrAdapterMQTTInvalidConfig, msgConfigKeepAliveOverflow,
			errcode.WithDetails(
				errcode.PublicDuration(detailKeyKeepAlive, c.keepAlive),
				errcode.PublicDuration(detailKeyMax, maxKeepAliveDuration),
			))
	}
	if c.sessionExpiry < 0 || c.sessionExpiry > maxSessionExpiryDuration {
		return errcode.New(errcode.KindInvalid, ErrAdapterMQTTInvalidConfig, msgConfigSessionExpiryRange,
			errcode.WithDetails(
				errcode.PublicDuration(detailKeySessionExpiry, c.sessionExpiry),
				errcode.PublicDuration(detailKeyMax, maxSessionExpiryDuration),
			))
	}
	// SessionExpiry > 0 must also be >= 1s to survive uint32 floor truncation —
	// 500ms would wire 0 and disable persistent session, defeating the caller's
	// intent. SessionExpiry == 0 (clean session) is the explicit non-persistent
	// path and stays accepted.
	if c.sessionExpiry > 0 && c.sessionExpiry < time.Second {
		return errcode.New(errcode.KindInvalid, ErrAdapterMQTTInvalidConfig, msgConfigSessionExpiryBelowSecond,
			errcode.WithDetails(
				errcode.PublicDuration(detailKeySessionExpiry, c.sessionExpiry),
				errcode.PublicDuration(detailKeyMin, time.Second),
			))
	}
	return nil
}

// validateBackoff checks the backoff sub-config.
func (c Config) validateBackoff() error {
	if c.backoff.BaseDelay <= 0 || c.backoff.MaxDelay < c.backoff.BaseDelay {
		return errcode.New(errcode.KindInvalid, ErrAdapterMQTTInvalidConfig, msgConfigBackoffInvalid,
			errcode.WithDetails(
				errcode.PublicDuration("baseDelay", c.backoff.BaseDelay),
				errcode.PublicDuration("maxDelay", c.backoff.MaxDelay),
			))
	}
	return nil
}

// validatePublishTimeout checks that publishTimeout is non-negative.
// 0 is accepted — it means the Publisher uses the caller-provided ctx deadline
// as-is with no additional adapter-imposed timeout. Negative values are always
// rejected because they would immediately cancel any publish ctx.
func (c Config) validatePublishTimeout() error {
	if c.publishTimeout < 0 {
		return errcode.New(errcode.KindInvalid, ErrAdapterMQTTInvalidConfig,
			msgConfigPublishTimeoutNegative,
			errcode.WithDetails(errcode.PublicDuration("publishTimeout", c.publishTimeout)))
	}
	return nil
}

// brokerURLs parses each entry in brokers and returns the resulting []*url.URL.
// Validation logic is shared with validate via parseBrokerURL.
func (c Config) brokerURLs() ([]*url.URL, error) {
	out := make([]*url.URL, 0, len(c.brokers))
	for _, raw := range c.brokers {
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
