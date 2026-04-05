package oidc

import "github.com/ghbvf/gocell/pkg/errcode"

// Error codes for the OIDC adapter.
const (
	// ErrDiscovery indicates a failure fetching or parsing the OIDC discovery document.
	ErrDiscovery errcode.Code = "ERR_ADAPTER_OIDC_DISCOVERY"
	// ErrTokenExchange indicates a failure exchanging an authorization code for tokens.
	ErrTokenExchange errcode.Code = "ERR_ADAPTER_OIDC_TOKEN_EXCHANGE"
	// ErrTokenVerify indicates a failure verifying an ID token (signature, claims, etc.).
	ErrTokenVerify errcode.Code = "ERR_ADAPTER_OIDC_TOKEN_VERIFY"
	// ErrUserInfo indicates a failure fetching user information from the UserInfo endpoint.
	ErrUserInfo errcode.Code = "ERR_ADAPTER_OIDC_USERINFO"
)
