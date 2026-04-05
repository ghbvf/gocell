package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// DiscoveryDocument represents the OIDC provider metadata from
// .well-known/openid-configuration.
type DiscoveryDocument struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	UserinfoEndpoint      string   `json:"userinfo_endpoint"`
	JWKSURI               string   `json:"jwks_uri"`
	ScopesSupported       []string `json:"scopes_supported"`
	IDTokenSigningAlgs    []string `json:"id_token_signing_alg_values_supported"`
}

// Provider handles OIDC discovery and metadata caching.
type Provider struct {
	config Config
	client *http.Client

	mu       sync.RWMutex
	doc      *DiscoveryDocument
	docFetch time.Time
}

// NewProvider creates a Provider with the given configuration.
func NewProvider(cfg Config) (*Provider, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	timeout := cfg.HTTPTimeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}

	return &Provider{
		config: cfg,
		client: &http.Client{Timeout: timeout},
	}, nil
}

// Discover fetches and caches the OIDC discovery document. If the cached
// document is still valid, it is returned without a network call.
func (p *Provider) Discover(ctx context.Context) (*DiscoveryDocument, error) {
	p.mu.RLock()
	if p.doc != nil && time.Since(p.docFetch) < p.config.DiscoveryCacheTTL {
		doc := p.doc
		p.mu.RUnlock()
		return doc, nil
	}
	p.mu.RUnlock()

	return p.fetchDiscovery(ctx)
}

// fetchDiscovery retrieves the discovery document from the well-known endpoint.
func (p *Provider) fetchDiscovery(ctx context.Context) (*DiscoveryDocument, error) {
	url := p.config.IssuerURL + "/.well-known/openid-configuration"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterOIDCDiscovery,
			fmt.Sprintf("oidc: failed to create discovery request for %s", url), err)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterOIDCDiscovery,
			"oidc: discovery request failed", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			slog.Warn("oidc: failed to close discovery response body",
				slog.Any("error", closeErr))
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, errcode.New(ErrAdapterOIDCDiscovery,
			fmt.Sprintf("oidc: discovery returned status %d", resp.StatusCode))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterOIDCDiscovery,
			"oidc: failed to read discovery response", err)
	}

	var doc DiscoveryDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, errcode.Wrap(ErrAdapterOIDCDiscovery,
			"oidc: failed to parse discovery document", err)
	}

	if doc.Issuer == "" {
		return nil, errcode.New(ErrAdapterOIDCDiscovery,
			"oidc: discovery document missing issuer")
	}

	p.mu.Lock()
	p.doc = &doc
	p.docFetch = time.Now()
	p.mu.Unlock()

	slog.Info("oidc: discovery document fetched",
		slog.String("issuer", doc.Issuer),
		slog.String("jwks_uri", doc.JWKSURI),
	)

	return &doc, nil
}
