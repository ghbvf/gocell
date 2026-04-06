// Package oidc provides a thin adapter over coreos/go-oidc v3 and
// golang.org/x/oauth2 for OpenID Connect authentication.
//
// This package intentionally does NOT re-define SDK types. It exposes
// go-oidc's Provider, IDToken, and oauth2's Token directly. The adapter
// adds GoCell-specific concerns: Config, errcode wrapping, and HTTP
// client lifecycle.
//
// ref: github.com/coreos/go-oidc/v3/oidc
// ref: golang.org/x/oauth2
package oidc
