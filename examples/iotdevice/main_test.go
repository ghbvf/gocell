package main

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	kauth "github.com/ghbvf/gocell/framework/kernel/auth"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	devicecell "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/auth/keystest"
)

const testServiceKey = "test-service-secret-at-least-32-bytes!!"

func TestInternalAuthChainMissingServiceSecretFailsFast(t *testing.T) {
	t.Setenv(iotdeviceServiceSecretEnv, "")

	_, err := newInternalAuthChainFromEnv(clock.Real())

	require.Error(t, err)
	require.Contains(t, err.Error(), iotdeviceServiceSecretEnv)
}

func TestInternalAuthChainContainsServiceToken(t *testing.T) {
	t.Setenv(iotdeviceServiceSecretEnv, testServiceKey)

	chain, err := newInternalAuthChainFromEnv(clock.Real())

	require.NoError(t, err)
	require.NotEmpty(t, chain)
	require.True(t, authChainContainsServiceToken(chain))
}

func TestJWTVerifierFromEnvRequiresIssuerAndAudience(t *testing.T) {
	t.Setenv(jwtIssuerEnv, "")
	t.Setenv(jwtAudienceEnv, "gocell")
	_, err := newJWTVerifierFromEnv(clock.Real())
	require.Error(t, err)
	assert.Contains(t, err.Error(), jwtIssuerEnv)

	t.Setenv(jwtIssuerEnv, "iotdevice-local")
	t.Setenv(jwtAudienceEnv, "")
	_, err = newJWTVerifierFromEnv(clock.Real())
	require.Error(t, err)
	assert.Contains(t, err.Error(), jwtAudienceEnv)
}

func TestJWTVerifierFromEnvAcceptsRS256AndRejectsDemoOrHS256Tokens(t *testing.T) {
	setJWTKeyEnv(t)
	t.Setenv(jwtIssuerEnv, "iotdevice-local")
	t.Setenv(jwtAudienceEnv, "gocell")

	verifier, err := newJWTVerifierFromEnv(clock.Real())
	require.NoError(t, err)

	keySet, err := auth.LoadKeySetFromEnv(clock.Real())
	require.NoError(t, err)
	issuer, err := auth.NewJWTIssuer(keySet, "iotdevice-local", time.Minute, clock.Real(),
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	require.NoError(t, err)
	token, err := issuer.Issue(kauth.TokenIntentAccess, "iot-admin", auth.IssueOptions{
		Roles: []string{
			devicecell.RoleAdmin,
			devicecell.RoleOperator,
			devicecell.RoleDevice,
		},
		Audience: []string{"gocell"},
	})
	require.NoError(t, err)

	claims, err := verifier.VerifyIntent(context.Background(), token, kauth.TokenIntentAccess)
	require.NoError(t, err)
	assert.Equal(t, "iot-admin", claims.Subject)
	assert.ElementsMatch(t, []string{
		devicecell.RoleAdmin,
		devicecell.RoleOperator,
		devicecell.RoleDevice,
	}, claims.Roles)

	_, err = verifier.VerifyIntent(context.Background(), "iotdevice-admin-demo-token", kauth.TokenIntentAccess)
	require.Error(t, err)

	_, err = verifier.VerifyIntent(context.Background(), signedHS256Token(t, "iotdevice-local", "gocell"), kauth.TokenIntentAccess)
	require.Error(t, err)
}

func authChainContainsServiceToken(chain []kauth.ListenerAuth) bool {
	for _, plan := range chain {
		if _, ok := plan.(kauth.AuthServiceToken); ok {
			return true
		}
	}
	return false
}

func setJWTKeyEnv(t *testing.T) {
	t.Helper()
	priv, pub := keystest.MustGenerateKeyPair()
	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	require.NoError(t, err)

	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(priv),
	})
	pubPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubDER,
	})
	t.Setenv(auth.EnvJWTPrivateKey, string(privPEM))
	t.Setenv(auth.EnvJWTPublicKey, string(pubPEM))
}

func signedHS256Token(t *testing.T, issuer, audience string) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":       "legacy-demo",
		"iss":       issuer,
		"aud":       audience,
		"exp":       time.Now().Add(time.Hour).Unix(),
		"iat":       time.Now().Unix(),
		"token_use": string(kauth.TokenIntentAccess),
	})
	token.Header["typ"] = auth.TypHeaderForIntent(kauth.TokenIntentAccess)
	signed, err := token.SignedString([]byte("demo-secret"))
	require.NoError(t, err)
	return signed
}
