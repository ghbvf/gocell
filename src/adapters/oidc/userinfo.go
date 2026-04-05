package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// UserInfo represents the response from the OIDC UserInfo endpoint.
type UserInfo struct {
	Subject       string `json:"sub"`
	Name          string `json:"name,omitempty"`
	Email         string `json:"email,omitempty"`
	EmailVerified bool   `json:"email_verified,omitempty"`
	Picture       string `json:"picture,omitempty"`
	Locale        string `json:"locale,omitempty"`
}

// GetUserInfo calls the UserInfo endpoint with the given access token.
func (p *Provider) GetUserInfo(ctx context.Context, accessToken string) (*UserInfo, error) {
	doc, err := p.Discover(ctx)
	if err != nil {
		return nil, fmt.Errorf("oidc userinfo: %w", err)
	}

	if doc.UserinfoEndpoint == "" {
		return nil, errcode.New(ErrAdapterOIDCUserInfo,
			"oidc: userinfo endpoint not found in discovery")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, doc.UserinfoEndpoint, nil)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterOIDCUserInfo,
			"oidc: failed to create userinfo request", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterOIDCUserInfo,
			"oidc: userinfo request failed", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			slog.Warn("oidc: failed to close userinfo response body",
				slog.Any("error", closeErr))
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, errcode.New(ErrAdapterOIDCUserInfo,
			fmt.Sprintf("oidc: userinfo endpoint returned status %d", resp.StatusCode))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterOIDCUserInfo,
			"oidc: failed to read userinfo response", err)
	}

	var info UserInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, errcode.Wrap(ErrAdapterOIDCUserInfo,
			"oidc: failed to parse userinfo response", err)
	}

	return &info, nil
}
