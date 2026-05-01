package oidc

import "github.com/ghbvf/gocell/pkg/errcode"

// OIDC adapter error codes.
//
// ErrAdapterOIDCExchange is named "Exchange" but its wire literal stays
// "ERR_ADAPTER_OIDC_TOKEN" — the constant was renamed to dodge a gosec
// G101 false positive on "Token" while keeping the existing wire value
// stable for downstream consumers (logs, error fingerprints, dashboards).
const (
	ErrAdapterOIDCConfig    errcode.Code = "ERR_ADAPTER_OIDC_CONFIG"
	ErrAdapterOIDCDiscovery errcode.Code = "ERR_ADAPTER_OIDC_DISCOVERY"
	ErrAdapterOIDCVerify    errcode.Code = "ERR_ADAPTER_OIDC_VERIFY"

	ErrAdapterOIDCExchange errcode.Code = "ERR_ADAPTER_OIDC_TOKEN"
	ErrAdapterOIDCUserInfo errcode.Code = "ERR_ADAPTER_OIDC_USERINFO"
)
