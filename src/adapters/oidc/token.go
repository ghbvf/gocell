package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// TokenResponse holds the tokens returned from the OIDC token endpoint.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// ExchangeCode exchanges an authorization code for tokens using the token endpoint.
func (p *Provider) ExchangeCode(ctx context.Context, code, redirectURI string) (*TokenResponse, error) {
	md, err := p.Metadata(ctx)
	if err != nil {
		return nil, err
	}

	data := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {redirectURI},
		"client_id":    {p.cfg.ClientID},
		"client_secret": {p.cfg.ClientSecret},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, md.TokenEndpoint,
		strings.NewReader(data.Encode()))
	if err != nil {
		return nil, errcode.Wrap(ErrTokenExchange, "oidc: failed to build token request", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, errcode.Wrap(ErrTokenExchange, "oidc: token request failed", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, errcode.Wrap(ErrTokenExchange, "oidc: failed to read token response", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, errcode.New(ErrTokenExchange,
			fmt.Sprintf("oidc: token endpoint returned HTTP %d: %s", resp.StatusCode, truncate(body, 256)))
	}

	var tr TokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, errcode.Wrap(ErrTokenExchange, "oidc: failed to decode token response", err)
	}

	if tr.AccessToken == "" {
		return nil, errcode.New(ErrTokenExchange, "oidc: token response missing access_token")
	}

	return &tr, nil
}

// truncate returns the first n bytes of b as a string.
func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
