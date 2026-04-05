package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// UserInfoResponse holds the claims returned from the OIDC UserInfo endpoint.
type UserInfoResponse struct {
	Subject       string `json:"sub"`
	Name          string `json:"name"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Picture       string `json:"picture"`
	Locale        string `json:"locale"`
	// Extra holds any additional claims not mapped to explicit fields.
	Extra map[string]any `json:"-"`
}

// UserInfo fetches user information from the OIDC UserInfo endpoint using the
// provided access token.
func (p *Provider) UserInfo(ctx context.Context, accessToken string) (*UserInfoResponse, error) {
	md, err := p.Metadata(ctx)
	if err != nil {
		return nil, err
	}

	if md.UserInfoEndpoint == "" {
		return nil, errcode.New(ErrUserInfo, "oidc: provider does not expose a userinfo endpoint")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, md.UserInfoEndpoint, nil)
	if err != nil {
		return nil, errcode.Wrap(ErrUserInfo, "oidc: failed to build userinfo request", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, errcode.Wrap(ErrUserInfo, "oidc: userinfo request failed", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, errcode.Wrap(ErrUserInfo, "oidc: failed to read userinfo response", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, errcode.New(ErrUserInfo,
			fmt.Sprintf("oidc: userinfo endpoint returned HTTP %d: %s", resp.StatusCode, truncate(body, 256)))
	}

	var uir UserInfoResponse
	if err := json.Unmarshal(body, &uir); err != nil {
		return nil, errcode.Wrap(ErrUserInfo, "oidc: failed to decode userinfo response", err)
	}

	// Parse extra claims.
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err == nil {
		delete(raw, "sub")
		delete(raw, "name")
		delete(raw, "email")
		delete(raw, "email_verified")
		delete(raw, "picture")
		delete(raw, "locale")
		if len(raw) > 0 {
			uir.Extra = raw
		}
	}

	if uir.Subject == "" {
		return nil, errcode.New(ErrUserInfo, "oidc: userinfo response missing sub claim")
	}

	return &uir, nil
}
