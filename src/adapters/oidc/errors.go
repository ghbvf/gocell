package oidc

import "github.com/ghbvf/gocell/pkg/errcode"

// OIDC adapter error codes.
const (
	ErrAdapterOIDCConfig    errcode.Code = "ERR_ADAPTER_OIDC_CONFIG"
	ErrAdapterOIDCDiscovery errcode.Code = "ERR_ADAPTER_OIDC_DISCOVERY"
	ErrAdapterOIDCVerify    errcode.Code = "ERR_ADAPTER_OIDC_VERIFY"
	ErrAdapterOIDCToken     errcode.Code = "ERR_ADAPTER_OIDC_TOKEN"
	ErrAdapterOIDCUserInfo  errcode.Code = "ERR_ADAPTER_OIDC_USERINFO"
)
