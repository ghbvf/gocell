package main

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	devicecell "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/auth"
)

const testServiceKey = "test-service-secret-at-least-32-bytes!!"

func TestInternalAuthChainMissingServiceSecretFailsFast(t *testing.T) {
	t.Setenv(iotdeviceServiceSecretEnv, "")

	_, err := newInternalAuthChainFromEnv()

	require.Error(t, err)
	require.Contains(t, err.Error(), iotdeviceServiceSecretEnv)
}

func TestInternalAuthChainContainsServiceToken(t *testing.T) {
	t.Setenv(iotdeviceServiceSecretEnv, testServiceKey)

	chain, err := newInternalAuthChainFromEnv()

	require.NoError(t, err)
	require.NotEmpty(t, chain)
	require.True(t, authChainContainsServiceToken(chain))
}

func TestJWTVerifierFromEnvRequiresIssuerAndAudience(t *testing.T) {
	t.Setenv(jwtIssuerEnv, "")
	t.Setenv(jwtAudienceEnv, "gocell")
	_, err := newJWTVerifierFromEnv()
	require.Error(t, err)
	assert.Contains(t, err.Error(), jwtIssuerEnv)

	t.Setenv(jwtIssuerEnv, "iotdevice-local")
	t.Setenv(jwtAudienceEnv, "")
	_, err = newJWTVerifierFromEnv()
	require.Error(t, err)
	assert.Contains(t, err.Error(), jwtAudienceEnv)
}

func TestJWTVerifierFromEnvAcceptsRS256AndRejectsDemoOrHS256Tokens(t *testing.T) {
	setJWTKeyEnv(t)
	t.Setenv(jwtIssuerEnv, "iotdevice-local")
	t.Setenv(jwtAudienceEnv, "gocell")

	verifier, err := newJWTVerifierFromEnv()
	require.NoError(t, err)

	keySet, err := auth.LoadKeySetFromEnv()
	require.NoError(t, err)
	issuer, err := auth.NewJWTIssuer(keySet, "iotdevice-local", time.Minute,
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	require.NoError(t, err)
	token, err := issuer.Issue(auth.TokenIntentAccess, "iot-admin", auth.IssueOptions{
		Roles: []string{
			devicecell.RoleAdmin,
			devicecell.RoleOperator,
			devicecell.RoleDevice,
		},
		Audience: []string{"gocell"},
	})
	require.NoError(t, err)

	claims, err := verifier.VerifyIntent(context.Background(), token, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.Equal(t, "iot-admin", claims.Subject)
	assert.ElementsMatch(t, []string{
		devicecell.RoleAdmin,
		devicecell.RoleOperator,
		devicecell.RoleDevice,
	}, claims.Roles)

	_, err = verifier.VerifyIntent(context.Background(), "iotdevice-admin-demo-token", auth.TokenIntentAccess)
	require.Error(t, err)

	_, err = verifier.VerifyIntent(context.Background(), signedHS256Token(t, "iotdevice-local", "gocell"), auth.TokenIntentAccess)
	require.Error(t, err)
}

func authChainContainsServiceToken(chain []cell.ListenerAuth) bool {
	for _, plan := range chain {
		if _, ok := plan.(cell.AuthServiceToken); ok {
			return true
		}
	}
	return false
}

func setJWTKeyEnv(t *testing.T) {
	t.Helper()
	priv, pub := auth.MustGenerateTestKeyPair()
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
		"token_use": string(auth.TokenIntentAccess),
	})
	token.Header["typ"] = auth.TypHeaderForIntent(auth.TokenIntentAccess)
	signed, err := token.SignedString([]byte("demo-secret"))
	require.NoError(t, err)
	return signed
}
