package oidc

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

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
// It wraps coreos/go-oidc v3 for discovery, token verification, and user info.
type Provider struct {
	config Config
	client *http.Client

	mu            sync.RWMutex
	doc           *DiscoveryDocument
	docFetch      time.Time
	oidcProvider  *gooidc.Provider
	providerFetch time.Time
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

// oidcContext returns a context with the Provider's HTTP client attached,
// so that go-oidc uses this client for all HTTP requests.
func (p *Provider) oidcContext(ctx context.Context) context.Context {
	return gooidc.ClientContext(ctx, p.client)
}

// ensureProvider lazily initializes or refreshes the go-oidc provider.
// The provider is re-created when DiscoveryCacheTTL expires, which also
// refreshes the internal JWKS RemoteKeySet used for token verification.
func (p *Provider) ensureProvider(ctx context.Context) (*gooidc.Provider, error) {
	p.mu.RLock()
	if p.oidcProvider != nil && time.Since(p.providerFetch) < p.config.DiscoveryCacheTTL {
		provider := p.oidcProvider
		p.mu.RUnlock()
		return provider, nil
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check after acquiring write lock.
	if p.oidcProvider != nil && time.Since(p.providerFetch) < p.config.DiscoveryCacheTTL {
		return p.oidcProvider, nil
	}

	provider, err := gooidc.NewProvider(p.oidcContext(ctx), p.config.IssuerURL)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterOIDCDiscovery,
			"oidc: failed to create provider via discovery", err)
	}

	p.oidcProvider = provider
	p.providerFetch = time.Now()
	slog.Info("oidc: provider initialized via discovery",
		slog.String("issuer", p.config.IssuerURL),
	)

	return provider, nil
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

	provider, err := p.ensureProvider(ctx)
	if err != nil {
		return nil, err
	}

	// Extract endpoints from the go-oidc provider.
	endpoint := provider.Endpoint()

	// Use Claims to extract additional fields go-oidc exposes via raw claims.
	var rawClaims struct {
		UserinfoEndpoint   string   `json:"userinfo_endpoint"`
		JWKSURI            string   `json:"jwks_uri"`
		ScopesSupported    []string `json:"scopes_supported"`
		IDTokenSigningAlgs []string `json:"id_token_signing_alg_values_supported"`
	}
	if claimErr := provider.Claims(&rawClaims); claimErr != nil {
		return nil, errcode.Wrap(ErrAdapterOIDCDiscovery,
			"oidc: failed to extract raw claims from provider", claimErr)
	}

	doc := &DiscoveryDocument{
		Issuer:                p.config.IssuerURL,
		AuthorizationEndpoint: endpoint.AuthURL,
		TokenEndpoint:         endpoint.TokenURL,
		UserinfoEndpoint:      rawClaims.UserinfoEndpoint,
		JWKSURI:               rawClaims.JWKSURI,
		ScopesSupported:       rawClaims.ScopesSupported,
		IDTokenSigningAlgs:    rawClaims.IDTokenSigningAlgs,
	}

	p.mu.Lock()
	p.doc = doc
	p.docFetch = time.Now()
	p.mu.Unlock()

	slog.Info("oidc: discovery document fetched",
		slog.String("issuer", doc.Issuer),
		slog.String("jwks_uri", doc.JWKSURI),
	)

	return doc, nil
}

// oauth2Config returns an oauth2.Config for the provider.
func (p *Provider) oauth2Config(provider *gooidc.Provider) *oauth2.Config {
	scopes := p.config.Scopes
	if len(scopes) == 0 {
		scopes = []string{gooidc.ScopeOpenID, "profile", "email"}
	}

	return &oauth2.Config{
		ClientID:     p.config.ClientID,
		ClientSecret: p.config.ClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  p.config.RedirectURL,
		Scopes:       scopes,
	}
}

// ExchangeCode exchanges an authorization code for tokens.
func (p *Provider) ExchangeCode(ctx context.Context, code string) (*TokenResponse, error) {
	provider, err := p.ensureProvider(ctx)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterOIDCToken, "oidc exchange: discovery failed", err)
	}

	oauth2Cfg := p.oauth2Config(provider)
	oauthCtx := p.oidcContext(ctx)

	token, err := oauth2Cfg.Exchange(oauthCtx, code)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterOIDCToken,
			"oidc: token exchange failed", err)
	}

	var expiresIn int
	if !token.Expiry.IsZero() {
		expiresIn = int(time.Until(token.Expiry).Seconds())
	}

	resp := &TokenResponse{
		AccessToken: token.AccessToken,
		TokenType:   token.TokenType,
		ExpiresIn:   expiresIn,
	}

	if token.RefreshToken != "" {
		resp.RefreshToken = token.RefreshToken
	}

	// Extract scope from extra fields (oauth2.Token does not expose scope directly).
	if scope, ok := token.Extra("scope").(string); ok {
		resp.Scope = scope
	}

	// Extract ID token from the extra fields.
	if rawIDToken, ok := token.Extra("id_token").(string); ok {
		resp.IDToken = rawIDToken
	}

	return resp, nil
}

// GetUserInfo calls the UserInfo endpoint with the given access token.
func (p *Provider) GetUserInfo(ctx context.Context, accessToken string) (*UserInfo, error) {
	provider, err := p.ensureProvider(ctx)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterOIDCUserInfo, "oidc userinfo: discovery failed", err)
	}

	tokenSource := oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: accessToken,
		TokenType:   "Bearer",
	})

	oidcInfo, err := provider.UserInfo(p.oidcContext(ctx), tokenSource)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterOIDCUserInfo,
			"oidc: userinfo request failed", err)
	}

	// Extract all claims into our UserInfo struct.
	var info UserInfo
	info.Subject = oidcInfo.Subject
	info.Email = oidcInfo.Email
	info.EmailVerified = oidcInfo.EmailVerified

	// Extract additional claims (name, picture, locale) from raw claims.
	var extra struct {
		Name    string `json:"name"`
		Picture string `json:"picture"`
		Locale  string `json:"locale"`
	}
	if claimErr := oidcInfo.Claims(&extra); claimErr == nil {
		info.Name = extra.Name
		info.Picture = extra.Picture
		info.Locale = extra.Locale
	}

	return &info, nil
}
