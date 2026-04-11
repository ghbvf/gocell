# Quickstart: JWT kid Rotation & HMAC Key Ring

**Feature**: 201-wm2-key-rotation

## JWT Key Rotation

### Before (current — single key, no kid)

```go
priv, pub, err := auth.LoadKeysFromEnv()
issuer, _ := auth.NewJWTIssuer(priv, "gocell", 1*time.Hour)
verifier, _ := auth.NewJWTVerifier(pub)

token, _ := issuer.Issue("user-123", []string{"admin"}, nil)
// Token header: {"alg": "RS256", "typ": "JWT"} — no kid
```

### After (key set with kid)

```go
// Load key set with active signing key + optional verification-only keys
keySet, err := auth.LoadKeySetFromEnv()
// keySet contains: 1 signing key (with kid) + 0-N verification keys

issuer, _ := auth.NewJWTIssuer(keySet, "gocell", 1*time.Hour)
verifier, _ := auth.NewJWTVerifier(keySet)

token, _ := issuer.Issue("user-123", []string{"admin"}, nil)
// Token header: {"alg": "RS256", "typ": "JWT", "kid": "<sha256-thumbprint>"}

// Verification: KeyFunc looks up key by kid from the key set
claims, err := verifier.Verify(ctx, token)
```

### Key Rotation (operator workflow)

1. Generate a new RSA key pair
2. Update environment: set new key as active, move old key to verification-only with expiry
3. Restart the service — the new key set is loaded, old tokens remain valid until expiry

## HMAC Service Token Rotation

### Before (current — single secret)

```go
secret := []byte(os.Getenv("GOCELL_SERVICE_SECRET"))
mw := auth.ServiceTokenMiddleware(secret)
token := auth.GenerateServiceToken(secret, "GET", "/internal/v1/sessions", time.Now())
```

### After (key ring with rotation)

```go
// Load key ring: current + optional previous secret
ring, err := auth.LoadHMACKeyRingFromEnv()

mw := auth.ServiceTokenMiddleware(ring)
// Verification: tries current secret first, then previous

token := auth.GenerateServiceToken(ring, "GET", "/internal/v1/sessions", time.Now())
// Always signs with current secret (position 0)
```

### Secret Rotation (operator workflow)

1. Set the new secret as `GOCELL_SERVICE_SECRET`
2. Set the old secret as `GOCELL_SERVICE_SECRET_PREVIOUS`
3. Restart the service — in-flight tokens signed with the old secret are still accepted

## Environment Variables

| Variable | Required | Description |
| --- | --- | --- |
| `GOCELL_JWT_PRIVATE_KEY` | Yes | PEM-encoded RSA private key (active signing key) |
| `GOCELL_JWT_PUBLIC_KEY` | Yes | PEM-encoded RSA public key (active signing key) |
| `GOCELL_JWT_PREV_PUBLIC_KEY` | No | PEM-encoded RSA public key (verification-only, recently rotated) |
| `GOCELL_JWT_PREV_KEY_EXPIRES` | No | Expiry for the verification-only key (RFC 3339, e.g. `2026-04-12T00:00:00Z`) |
| `GOCELL_SERVICE_SECRET` | Yes | Current HMAC secret for service tokens |
| `GOCELL_SERVICE_SECRET_PREVIOUS` | No | Previous HMAC secret (retained during rotation) |

## Middleware Integration (unchanged)

The existing `AuthMiddleware` and `ServiceTokenMiddleware` signatures remain compatible. The key set and key ring integrate behind the existing `TokenVerifier` interface — no changes to middleware.go or route registration are needed.
