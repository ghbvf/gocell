// jwt_deps.go: JWT 签名/验证依赖构建（Registry + issuer + verifier）。
package main

import (
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/runtime/auth"
	authconfig "github.com/ghbvf/gocell/runtime/auth/config"
)

// jwtDeps groups JWT signing and verification components built at startup.
// registry is the single source of truth for all JWT configuration; issuer
// and verifier are constructed from registry so they share the same settings.
type jwtDeps struct {
	issuer   *auth.JWTIssuer
	verifier *auth.JWTVerifier
	registry *authconfig.Registry
}

// buildJWTDeps loads the key set and constructs a Registry + issuer + verifier.
// GOCELL_JWT_ISSUER and GOCELL_JWT_AUDIENCE are required in all modes;
// missing values cause fail-fast before any assembly init.
//
// ref: kube-apiserver --service-account-issuer — required at startup.
// ref: Hydra internal/driver/config.DefaultProvider — single Registry pattern
// plan: docs/plans/202604191515-auth-federated-whistle.md §F1
func buildJWTDeps(adapterMode string) (jwtDeps, error) {
	keySet, err := loadKeySet(adapterMode)
	if err != nil {
		return jwtDeps{}, fmt.Errorf("load JWT key set: %w", err)
	}

	// Registry reads GOCELL_JWT_ISSUER and GOCELL_JWT_AUDIENCE, then validates
	// them in real mode. Config errors use ErrAuthVerifierConfig so operators
	// can distinguish startup misconfigurations from runtime key errors.
	reg, err := authconfig.FromEnv(
		authconfig.WithKeys(keySet),
		authconfig.WithRealMode(true),
	)
	if err != nil {
		return jwtDeps{}, fmt.Errorf("build JWT registry: %w", err)
	}

	issuer, err := authconfig.NewJWTIssuerFromRegistry(reg, auth.DefaultAccessTokenTTL)
	if err != nil {
		return jwtDeps{}, fmt.Errorf("create JWT issuer: %w", err)
	}

	verifier, err := authconfig.NewJWTVerifierFromRegistry(reg)
	if err != nil {
		return jwtDeps{}, fmt.Errorf("create JWT verifier: %w", err)
	}

	slog.Info("corebundle: JWT deps built",
		slog.String("issuer", reg.Issuer()),
		slog.Any("audiences", reg.Audiences()),
		slog.String("adapter_mode", adapterMode))

	return jwtDeps{issuer: issuer, verifier: verifier, registry: reg}, nil
}
