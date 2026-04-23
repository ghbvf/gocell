// controlplane.go: 内部控制平面端点守卫（/internal/v1/* service token middleware）。
package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

// internalGuardFromEnv builds a ServiceTokenMiddleware guard for /internal/v1/*
// from GOCELL_SERVICE_SECRET (and optionally GOCELL_SERVICE_SECRET_PREVIOUS).
//
//   - In "real" adapter mode, the env var is required; missing value returns an error.
//   - In dev mode (any non-"real" mode), an empty secret returns (nil, nil), meaning
//     "no guard installed" — the caller then skips WithInternalEndpointGuard.
//
// The returned guard is nil only in dev mode with an empty secret. In all other
// cases a functioning guard (or an error) is returned.
//
// ref: Kubernetes kube-apiserver service-account verification — guard only when
// key material is present; no guard is better than a broken guard.
func internalGuardFromEnv(adapterMode string) (func(http.Handler) http.Handler, error) {
	secret := os.Getenv(auth.EnvServiceSecret)
	if secret == "" {
		if adapterMode == "real" {
			return nil, errcode.New(errcode.ErrValidationFailed,
				"GOCELL_SERVICE_SECRET must be set in adapter mode \"real\" to protect /internal/v1/*")
		}
		slog.Warn("controlplane guard disabled: GOCELL_SERVICE_SECRET is empty (dev mode only)")
		return nil, nil
	}
	if err := rejectDemoKey(adapterMode, auth.EnvServiceSecret, []byte(secret)); err != nil {
		return nil, err
	}
	prevSecret := os.Getenv(auth.EnvServiceSecretPrevious)
	var prevBytes []byte
	if prevSecret != "" {
		if err := rejectDemoKey(adapterMode, auth.EnvServiceSecretPrevious, []byte(prevSecret)); err != nil {
			return nil, err
		}
		prevBytes = []byte(prevSecret)
	}
	ring, err := auth.NewHMACKeyRing([]byte(secret), prevBytes)
	if err != nil {
		return nil, fmt.Errorf("build service HMAC key ring: %w", err)
	}
	return auth.ServiceTokenMiddleware(ring), nil
}
