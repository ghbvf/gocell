package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// TokenResponse represents the response from the token endpoint.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// ExchangeCode exchanges an authorization code for tokens.
func (p *Provider) ExchangeCode(ctx context.Context, code string) (*TokenResponse, error) {
	doc, err := p.Discover(ctx)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterOIDCToken, "oidc exchange: discovery failed", err)
	}

	if doc.TokenEndpoint == "" {
		return nil, errcode.New(ErrAdapterOIDCToken,
			"oidc: token endpoint not found in discovery")
	}

	data := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {p.config.RedirectURL},
		"client_id":     {p.config.ClientID},
		"client_secret": {p.config.ClientSecret},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		doc.TokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterOIDCToken,
			"oidc: failed to create token request", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterOIDCToken,
			"oidc: token request failed", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			slog.Warn("oidc: failed to close token response body",
				slog.Any("error", closeErr))
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterOIDCToken,
			"oidc: failed to read token response", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, errcode.New(ErrAdapterOIDCToken,
			fmt.Sprintf("oidc: token endpoint returned status %d", resp.StatusCode))
	}

	var tokenResp TokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, errcode.Wrap(ErrAdapterOIDCToken,
			"oidc: failed to parse token response", err)
	}

	return &tokenResp, nil
}
