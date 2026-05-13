package oidc

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/ghbvf/gocell/adapters/adapterutil"
	"github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/worker"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/secutil"
)

// Compile-time assertion: Adapter satisfies lifecycle.ManagedResource.
var _ lifecycle.ManagedResource = (*Adapter)(nil)

const (
	// defaultOIDCHTTPTimeout is the default HTTP client timeout for OIDC
	// provider discovery and token exchange requests.
	defaultOIDCHTTPTimeout = 10 * time.Second
)

// Config holds the OIDC provider configuration.
type Config struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string      // default: [openid, profile, email]
	HTTPTimeout  time.Duration // default: 10s
}

// Validate checks required fields.
func (c Config) Validate() error {
	if c.IssuerURL == "" {
		return errcode.New(errcode.KindInternal, ErrAdapterOIDCConfig, "oidc: issuer URL is required")
	}
	// SEC-FAIL-CLOSED: reject non-TLS issuer endpoints (loopback exempt).
	if err := secutil.ValidateTLSEndpoint(c.IssuerURL); err != nil {
		return err
	}
	if c.ClientID == "" {
		return errcode.New(errcode.KindInternal, ErrAdapterOIDCConfig, "oidc: client ID is required")
	}
	return nil
}

// Adapter is a thin wrapper over go-oidc and oauth2. It manages provider
// discovery and exposes the underlying go-oidc types directly.
type Adapter struct {
	config Config
	client *http.Client

	mu       sync.RWMutex
	provider *gooidc.Provider
}

// New creates an OIDC Adapter and synchronously performs OIDC discovery.
// An unreachable or misconfigured issuer causes construction to fail
// immediately (fail-fast at boot, not at first request).
//
// ref: coreos/go-oidc — Provider.NewProvider semantics (sync HTTP round-trip).
func New(ctx context.Context, cfg Config) (*Adapter, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	timeout := cfg.HTTPTimeout
	if timeout == 0 {
		timeout = defaultOIDCHTTPTimeout
	}
	a := &Adapter{
		config: cfg,
		client: &http.Client{Timeout: timeout},
	}
	if _, err := a.discover(ctx, true); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *Adapter) oidcCtx(ctx context.Context) context.Context {
	return gooidc.ClientContext(ctx, a.client)
}

// Provider returns the go-oidc Provider, performing discovery on first call.
// Subsequent calls return the cached provider without re-fetching.
//
// IMPORTANT: The cached provider never expires automatically. For long-lived
// processes, the caller (typically bootstrap/runtime) MUST call Refresh()
// periodically to pick up OIDC metadata and JWKS key rotation. A common
// pattern is a background goroutine with a 24h ticker.
func (a *Adapter) Provider(ctx context.Context) (*gooidc.Provider, error) {
	a.mu.RLock()
	if a.provider != nil {
		p := a.provider
		a.mu.RUnlock()
		return p, nil
	}
	a.mu.RUnlock()

	return a.discover(ctx, false)
}

// Refresh forces re-discovery of the OIDC provider metadata. Use this
// periodically in long-lived processes to pick up JWKS/metadata rotation.
func (a *Adapter) Refresh(ctx context.Context) (*gooidc.Provider, error) {
	return a.discover(ctx, true)
}

// discover performs OIDC discovery. When force is false, it double-checks
// whether another goroutine already completed initialization before making
// a network call (cold-start thundering-herd protection).
func (a *Adapter) discover(ctx context.Context, force bool) (*gooidc.Provider, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !force && a.provider != nil {
		return a.provider, nil
	}

	p, err := gooidc.NewProvider(a.oidcCtx(ctx), a.config.IssuerURL)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterOIDCDiscovery,
			"oidc: discovery failed", err,
			errcode.WithDetails(slog.String("issuer", a.config.IssuerURL)))
	}
	a.provider = p
	slog.Info("oidc: provider discovered", slog.String("issuer", a.config.IssuerURL))
	return p, nil
}

// Verifier returns a go-oidc IDTokenVerifier for the configured client ID.
func (a *Adapter) Verifier(ctx context.Context) (*gooidc.IDTokenVerifier, error) {
	p, err := a.Provider(ctx)
	if err != nil {
		return nil, err
	}
	return p.Verifier(&gooidc.Config{ClientID: a.config.ClientID}), nil
}

// Checkers returns a readyz probe for the OIDC provider. The probe verifies
// that the cached provider is populated without re-discovering.
//
// ref: kubernetes/kubernetes pkg/util/healthz — named health checkers.
func (a *Adapter) Checkers() map[string]func(context.Context) error {
	return adapterutil.HealthToCheckers("oidc_ready", a.healthProbe, adapterutil.DefaultProbeTimeout)
}

// Worker returns nil — no background goroutine is needed. The JWKS rotation
// worker is deferred to PR-11/A-02.
func (a *Adapter) Worker() worker.Worker { return nil }

// Close is idempotent and currently a no-op. The go-oidc provider has no
// managed connections to release; the HTTP client is ephemeral.
func (a *Adapter) Close(_ context.Context) error { return nil }

// healthProbe verifies the cached provider is populated. It does NOT
// re-discover — Refresh() / the future rotation worker (PR-11) handles that.
//
// Lock contention note: Provider(ctx) acquires a.mu.RLock(). When Refresh()
// or the future PR-11/A-02 JWKS rotation worker holds a.mu.Lock() during
// re-discover, healthProbe is briefly queued. The probe's inner 5s timeout
// (adapterutil.DefaultProbeTimeout) bounds the worst case; if rotation
// reliably exceeds that, PR-11 must switch oidc to an atomic.Bool state
// machine (like adapters/s3.Client) so /readyz reads do not block.
func (a *Adapter) healthProbe(ctx context.Context) error {
	_, err := a.Provider(ctx)
	return err
}

// OAuth2Config returns an oauth2.Config using the provider's endpoints.
func (a *Adapter) OAuth2Config(ctx context.Context) (*oauth2.Config, error) {
	p, err := a.Provider(ctx)
	if err != nil {
		return nil, err
	}
	scopes := a.config.Scopes
	if len(scopes) == 0 {
		scopes = []string{gooidc.ScopeOpenID, "profile", "email"}
	}
	return &oauth2.Config{
		ClientID:     a.config.ClientID,
		ClientSecret: a.config.ClientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  a.config.RedirectURL,
		Scopes:       scopes,
	}, nil
}
