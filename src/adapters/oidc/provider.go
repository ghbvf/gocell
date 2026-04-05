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

// discoveryMetadata represents the OpenID Provider Configuration response from
// the /.well-known/openid-configuration endpoint.
type discoveryMetadata struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	UserInfoEndpoint      string   `json:"userinfo_endpoint"`
	JWKSURI               string   `json:"jwks_uri"`
	SupportedAlgorithms   []string `json:"id_token_signing_alg_values_supported"`
}

// Provider manages OIDC Discovery metadata and provides access to the various
// OIDC operations (token exchange, verification, userinfo).
type Provider struct {
	cfg    Config
	client *http.Client

	mu       sync.RWMutex
	metadata *discoveryMetadata
	fetchedAt time.Time
}

// discoveryMaxAge controls how long cached metadata is considered fresh.
const discoveryMaxAge = 1 * time.Hour

// maxResponseBytes limits the size of responses read from the OIDC provider
// to prevent excessive memory usage from malicious or misconfigured servers.
const maxResponseBytes = 1 << 20 // 1 MiB

// NewProvider creates a Provider and eagerly fetches the OIDC Discovery document.
func NewProvider(ctx context.Context, cfg Config, opts ...ProviderOption) (*Provider, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	p := &Provider{
		cfg:    cfg,
		client: http.DefaultClient,
	}
	for _, opt := range opts {
		opt(p)
	}

	if err := p.discover(ctx); err != nil {
		return nil, err
	}

	slog.Info("oidc: provider initialized",
		slog.String("issuer", cfg.IssuerURL),
	)
	return p, nil
}

// ProviderOption configures optional Provider behaviour.
type ProviderOption func(*Provider)

// WithHTTPClient overrides the default HTTP client used for all provider requests.
func WithHTTPClient(c *http.Client) ProviderOption {
	return func(p *Provider) {
		p.client = c
	}
}

// discover fetches (or refreshes) the OpenID Provider Configuration.
func (p *Provider) discover(ctx context.Context) error {
	url := p.cfg.IssuerURL + "/.well-known/openid-configuration"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return errcode.Wrap(ErrDiscovery, "oidc: failed to build discovery request", err)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return errcode.Wrap(ErrDiscovery, "oidc: discovery request failed", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return errcode.New(ErrDiscovery,
			fmt.Sprintf("oidc: discovery endpoint returned HTTP %d", resp.StatusCode))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return errcode.Wrap(ErrDiscovery, "oidc: failed to read discovery response", err)
	}

	var md discoveryMetadata
	if err := json.Unmarshal(body, &md); err != nil {
		return errcode.Wrap(ErrDiscovery, "oidc: failed to decode discovery document", err)
	}

	if md.Issuer == "" || md.JWKSURI == "" || md.TokenEndpoint == "" {
		return errcode.New(ErrDiscovery, "oidc: discovery document missing required fields")
	}

	p.mu.Lock()
	p.metadata = &md
	p.fetchedAt = time.Now()
	p.mu.Unlock()

	return nil
}

// Metadata returns the cached discovery metadata, refreshing if stale.
func (p *Provider) Metadata(ctx context.Context) (*discoveryMetadata, error) {
	p.mu.RLock()
	md := p.metadata
	age := time.Since(p.fetchedAt)
	p.mu.RUnlock()

	if md != nil && age < discoveryMaxAge {
		return md, nil
	}

	if err := p.discover(ctx); err != nil {
		return nil, err
	}

	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.metadata, nil
}

// Health checks that the discovery endpoint is reachable.
func (p *Provider) Health(ctx context.Context) error {
	url := p.cfg.IssuerURL + "/.well-known/openid-configuration"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return errcode.Wrap(ErrDiscovery, "oidc: health check request build failed", err)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return errcode.Wrap(ErrDiscovery, "oidc: health check failed", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return errcode.New(ErrDiscovery,
			fmt.Sprintf("oidc: health check returned HTTP %d", resp.StatusCode))
	}
	return nil
}
