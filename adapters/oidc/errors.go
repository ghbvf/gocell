package oidc

import "github.com/ghbvf/gocell/pkg/errcode"

// OIDC adapter error codes.
const (
	ErrAdapterOIDCConfig    errcode.Code = "ERR_ADAPTER_OIDC_CONFIG"
	ErrAdapterOIDCDiscovery errcode.Code = "ERR_ADAPTER_OIDC_DISCOVERY"
	ErrAdapterOIDCVerify    errcode.Code = "ERR_ADAPTER_OIDC_VERIFY"
	//nolint:gosec // G101 false positive: error code constant, not a credential
	ErrAdapterOIDCToken    errcode.Code = "ERR_ADAPTER_OIDC_TOKEN"
	ErrAdapterOIDCUserInfo errcode.Code = "ERR_ADAPTER_OIDC_USERINFO"
)
