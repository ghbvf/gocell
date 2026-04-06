package oidc

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/ghbvf/gocell/pkg/errcode"
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
		return errcode.New(ErrAdapterOIDCConfig, "oidc: issuer URL is required")
	}
	if c.ClientID == "" {
		return errcode.New(ErrAdapterOIDCConfig, "oidc: client ID is required")
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

// New creates an OIDC Adapter.
func New(cfg Config) (*Adapter, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	timeout := cfg.HTTPTimeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	return &Adapter{
		config: cfg,
		client: &http.Client{Timeout: timeout},
	}, nil
}

func (a *Adapter) oidcCtx(ctx context.Context) context.Context {
	return gooidc.ClientContext(ctx, a.client)
}

// Provider returns the go-oidc Provider, performing discovery on first call.
// Subsequent calls return the cached provider. Call Refresh() to force
// re-discovery (e.g., on a timer for long-lived processes).
func (a *Adapter) Provider(ctx context.Context) (*gooidc.Provider, error) {
	a.mu.RLock()
	if a.provider != nil {
		p := a.provider
		a.mu.RUnlock()
		return p, nil
	}
	a.mu.RUnlock()

	return a.refresh(ctx)
}

// Refresh forces re-discovery of the OIDC provider metadata. Use this
// periodically in long-lived processes to pick up JWKS/metadata rotation.
func (a *Adapter) Refresh(ctx context.Context) (*gooidc.Provider, error) {
	return a.refresh(ctx)
}

func (a *Adapter) refresh(ctx context.Context) (*gooidc.Provider, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	p, err := gooidc.NewProvider(a.oidcCtx(ctx), a.config.IssuerURL)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterOIDCDiscovery,
			fmt.Sprintf("oidc: discovery failed for %s", a.config.IssuerURL), err)
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
