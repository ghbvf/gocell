// controlplane.go: 内部控制平面端点守卫（/internal/v1/* service token middleware）。
package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

// nonceStoreBuffer extends the nonce retention window past the token validity
// bound so a nonce cannot be replayed as the token approaches expiry.
const nonceStoreBuffer = 30 * time.Second

// internalGuard is the resolved /internal/v1/* service-token guard plus the
// dependencies the guard was built from. Holding the NonceStore and ring
// alongside the middleware closure lets SharedDeps.Validate introspect the
// guard at startup — a plain middleware func would be an opaque black box,
// forcing validation to a shallow "is it nil?" check that cannot detect a
// guard that was installed without replay protection.
//
// Fields are package-private; external consumers use Middleware / NonceStore
// to project out exactly what they need.
type internalGuard struct {
	ring       *auth.HMACKeyRing
	nonceStore auth.NonceStore
	mw         func(http.Handler) http.Handler
}

// Middleware returns the assembled service-token middleware ready for the
// internal listener's router chain.
func (g *internalGuard) Middleware() func(http.Handler) http.Handler { return g.mw }

// NonceStore exposes the backing replay-defense store. Startup validation
// inspects Kind() to reject NonceStoreKindNoop in adapter mode "real".
func (g *internalGuard) NonceStore() auth.NonceStore { return g.nonceStore }

// internalGuardFromEnv builds an internalGuard for /internal/v1/* from
// GOCELL_SERVICE_SECRET (and optionally GOCELL_SERVICE_SECRET_PREVIOUS).
//
//   - In "real" adapter mode, the env var is required; a missing value is
//     returned as an ErrControlplaneServiceSecretMissing so operators can
//     grep startup logs for the specific misconfiguration class.
//   - In dev mode (any non-"real" mode), an empty secret returns (nil, nil) —
//     the caller skips WithInternalMiddleware.
//
// The guard always wires a replay-defense NonceStore when installed. A
// single-process InMemoryNonceStore is used by default; multi-pod
// deployments must replace it with a shared implementation (e.g. Redis)
// before horizontally scaling — SharedDeps.Validate checks Kind() at
// startup but cannot know the topology, so the operator is responsible for
// matching store class to pod count.
//
// ref: Kubernetes kube-apiserver service-account verification — guard only when
// key material is present; no guard is better than a broken guard.
// ref: gorilla/securecookie — replay protection defaults on, not opt-in.
func internalGuardFromEnv(adapterMode string) (*internalGuard, error) {
	secret := os.Getenv(auth.EnvServiceSecret)
	if secret == "" {
		if isRealMode(adapterMode) {
			return nil, errcode.New(errcode.ErrControlplaneServiceSecretMissing,
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
	store, err := auth.NewInMemoryNonceStore(auth.ServiceTokenMaxAge + nonceStoreBuffer)
	if err != nil {
		return nil, fmt.Errorf("build service token nonce store: %w", err)
	}
	mw := auth.ServiceTokenMiddleware(ring, auth.WithServiceTokenNonceStore(store))
	return &internalGuard{ring: ring, nonceStore: store, mw: mw}, nil
}
