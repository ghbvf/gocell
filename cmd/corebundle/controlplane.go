// controlplane.go: 内部控制平面端点 HMAC 密钥环构造（/internal/v1/* service token）。
package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/ghbvf/gocell/cellmodules/cellsecrets"
	kauth "github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/auth"
)

// buildInternalServiceKeyring builds the /internal/v1/* service-token keyring in
// exactly one of two MUTUALLY EXCLUSIVE modes (#2153), selected by env:
//
//   - master mode (monolith): GOCELL_SERVICE_SECRET [+ _PREVIOUS] → an
//     HMACKeyRing that derives per-cell subkeys in-process. Single trust domain;
//     a process compromise yields the master, so this is NOT per-cell-Hard.
//   - provisioned mode (split, per-cell): GOCELL_SERVICE_SIGNING_KEY +
//     GOCELL_SERVICE_VERIFY_KEYS [+ _PREVIOUS] (+ GOCELL_SERVICE_CELL) → a
//     master-absent ProvisionedKeyring holding only this cell's signing subkey
//     and its declared callers' verify subkeys. Cross-cell forgery is
//     cryptographically fail-closed. Subkeys come from `gocell derive-service-keys`.
//
// Fail-closed env-layer guard: setting BOTH master and provisioned envs is
// ambiguous (a split cell must never also hold the master) → error; setting
// NEITHER → error. There is no dev-mode silent bypass.
//
// The keyring + the NonceStore (built by replaydeps.Resolve) are the two
// components of the internal-listener service-token guard. Both are placed on
// composition.SharedDeps (InternalServiceKeyring + NonceStore) so the
// composition-contract control-plane validation can introspect them at startup.
//
// ref: Kubernetes kube-apiserver service-account verification — require key
// material before installing an authentication guard.
// ref: gorilla/securecookie — replay protection defaults on, not opt-in.
func buildInternalServiceKeyring(adapterMode string) (kauth.ServiceKeyring, error) {
	master := os.Getenv(auth.EnvServiceSecret)
	signing := os.Getenv(auth.EnvServiceSigningKey)
	masterMode := master != ""
	provisionedMode := signing != ""

	switch {
	case masterMode && provisionedMode:
		return nil, errcode.New(errcode.KindInternal, errcode.ErrControlplaneServiceSecretMissing,
			"service-token keyring config is ambiguous: both master ("+auth.EnvServiceSecret+") and "+
				"provisioned ("+auth.EnvServiceSigningKey+") modes are set; a split cell must hold only its "+
				"per-cell subkeys, never the master")
	case provisionedMode:
		ring, err := auth.LoadProvisionedKeyringFromEnv()
		if err != nil {
			return nil, fmt.Errorf("build provisioned service keyring: %w", err)
		}
		// LoadProvisionedKeyringFromEnv succeeded, so EnvServiceOwnCell already
		// passed metadata.MatchCellID (^[a-z][a-z0-9]{1,31}$ — no newlines/control
		// chars); the value is safe to log.
		//nolint:gosec // G706: env value is MatchCellID-validated above, not raw taint.
		slog.Info("controlplane: per-cell provisioned service keyring built for /internal/v1/*",
			slog.String("cell", os.Getenv(auth.EnvServiceOwnCell)))
		return ring, nil
	case masterMode:
		if err := cellsecrets.RejectDemoKey(adapterMode, auth.EnvServiceSecret, []byte(master)); err != nil {
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
		ring, err := auth.NewHMACKeyRing([]byte(master), prevBytes)
		if err != nil {
			return nil, fmt.Errorf("build service HMAC master keyring: %w", err)
		}
		slog.Info("controlplane: master-derived service keyring built for /internal/v1/*")
		return ring, nil
	default:
		return nil, errcode.New(errcode.KindInternal, errcode.ErrControlplaneServiceSecretMissing,
			"service-token keyring not configured: set "+auth.EnvServiceSecret+" (monolith, master) or "+
				auth.EnvServiceSigningKey+" + "+auth.EnvServiceVerifyKeys+" (split, per-cell subkeys from "+
				"`gocell derive-service-keys`)")
	}
}
