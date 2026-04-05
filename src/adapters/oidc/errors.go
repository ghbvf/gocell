package oidc

import "github.com/ghbvf/gocell/pkg/errcode"

// OIDC adapter error codes.
const (
	// ErrAdapterOIDCDiscovery indicates a failure fetching or parsing the OIDC discovery document.
	ErrAdapterOIDCDiscovery errcode.Code = "ERR_ADAPTER_OIDC_DISCOVERY"
	// ErrAdapterOIDCToken indicates a failure during token exchange.
	ErrAdapterOIDCToken errcode.Code = "ERR_ADAPTER_OIDC_TOKEN"
	// ErrAdapterOIDCJWKS indicates a failure fetching or parsing the JWKS.
	ErrAdapterOIDCJWKS errcode.Code = "ERR_ADAPTER_OIDC_JWKS"
	// ErrAdapterOIDCVerify indicates a token verification failure.
	ErrAdapterOIDCVerify errcode.Code = "ERR_ADAPTER_OIDC_VERIFY"
	// ErrAdapterOIDCUserInfo indicates a failure calling the UserInfo endpoint.
	ErrAdapterOIDCUserInfo errcode.Code = "ERR_ADAPTER_OIDC_USERINFO"
	// ErrAdapterOIDCConfig indicates an invalid OIDC configuration.
	ErrAdapterOIDCConfig errcode.Code = "ERR_ADAPTER_OIDC_CONFIG"
)
