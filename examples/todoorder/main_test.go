package main

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	ordercell "github.com/ghbvf/gocell/examples/todoorder/cells/ordercell"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testServiceKey = "test-service-secret-at-least-32-bytes!!"

func TestInternalAuthChainMissingServiceSecretFailsFast(t *testing.T) {
	t.Setenv(todoorderServiceSecretEnv, "")

	_, err := newInternalAuthChainFromEnv()

	require.Error(t, err)
	require.Contains(t, err.Error(), todoorderServiceSecretEnv)
}

func TestInternalAuthChainContainsServiceToken(t *testing.T) {
	t.Setenv(todoorderServiceSecretEnv, testServiceKey)

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

	t.Setenv(jwtIssuerEnv, "todoorder-local")
	t.Setenv(jwtAudienceEnv, "")
	_, err = newJWTVerifierFromEnv()
	require.Error(t, err)
	assert.Contains(t, err.Error(), jwtAudienceEnv)
}

func TestJWTVerifierFromEnvAcceptsRS256AndRejectsDemoOrHS256Tokens(t *testing.T) {
	setJWTKeyEnv(t)
	t.Setenv(jwtIssuerEnv, "todoorder-local")
	t.Setenv(jwtAudienceEnv, "gocell")

	verifier, err := newJWTVerifierFromEnv()
	require.NoError(t, err)

	keySet, err := auth.LoadKeySetFromEnv()
	require.NoError(t, err)
	issuer, err := auth.NewJWTIssuer(keySet, "todoorder-local", time.Minute,
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	require.NoError(t, err)
	token, err := issuer.Issue(auth.TokenIntentAccess, "todo-customer", auth.IssueOptions{
		Roles:    []string{ordercell.RoleCustomer},
		Audience: []string{"gocell"},
	})
	require.NoError(t, err)

	claims, err := verifier.VerifyIntent(context.Background(), token, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.Equal(t, "todo-customer", claims.Subject)
	assert.Equal(t, []string{ordercell.RoleCustomer}, claims.Roles)

	_, err = verifier.VerifyIntent(context.Background(), "todoorder-customer-demo-token", auth.TokenIntentAccess)
	require.Error(t, err)

	_, err = verifier.VerifyIntent(context.Background(), signedHS256Token(t, "todoorder-local", "gocell"), auth.TokenIntentAccess)
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
