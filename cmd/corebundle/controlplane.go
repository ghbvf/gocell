// controlplane.go: 内部控制平面端点 HMAC 密钥环构造（/internal/v1/* service token）。
package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/ghbvf/gocell/cellmodules/cellsecrets"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

// buildInternalHMACRing builds the /internal/v1/* service-token HMAC key ring
// from GOCELL_SERVICE_SECRET (and optionally GOCELL_SERVICE_SECRET_PREVIOUS).
//
// GOCELL_SERVICE_SECRET is required in all adapter modes (SEC-FAIL-CLOSED).
// A missing secret returns ErrControlplaneServiceSecretMissing regardless of
// the adapterMode parameter — there is no dev-mode silent bypass.
//
// The ring + the NonceStore (built by replaydeps.Resolve) are the two components
// of the internal-listener service-token guard. Both are placed on
// composition.SharedDeps (InternalHMACRing + NonceStore) so that the
// composition-contract control-plane validation can introspect NonceStore.Kind()
// at startup and reject a NoopNonceStore / single-process store in a multi-pod
// real deployment — see runtime/composition.SharedDeps.validateProductionControlPlane.
//
// ref: Kubernetes kube-apiserver service-account verification — require key
// material before installing an authentication guard.
// ref: gorilla/securecookie — replay protection defaults on, not opt-in.
func buildInternalHMACRing(adapterMode string) (*auth.HMACKeyRing, error) {
	secret := os.Getenv(auth.EnvServiceSecret)
	if secret == "" {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrControlplaneServiceSecretMissing,
			"GOCELL_SERVICE_SECRET must be set in all adapter modes to protect /internal/v1/*")
	}
	if err := cellsecrets.RejectDemoKey(adapterMode, auth.EnvServiceSecret, []byte(secret)); err != nil {
		return nil, err
	}
	prevSecret := os.Getenv(auth.EnvServiceSecretPrevious)
	var prevBytes []byte
	if prevSecret != "" {
		if err := cellsecrets.RejectDemoKey(adapterMode, auth.EnvServiceSecretPrevious, []byte(prevSecret)); err != nil {
			return nil, err
		}
		prevBytes = []byte(prevSecret)
	}
	ring, err := auth.NewHMACKeyRing([]byte(secret), prevBytes)
	if err != nil {
		return nil, fmt.Errorf("build service HMAC key ring: %w", err)
	}
	slog.Info("controlplane: service-token HMAC ring built for /internal/v1/*")
	return ring, nil
}
