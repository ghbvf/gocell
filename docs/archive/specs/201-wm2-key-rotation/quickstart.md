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

## PEM Encoding in Environment Variables

PEM keys contain newlines. Depending on your deployment platform:

| Platform | How to set multi-line env var |
| --- | --- |
| Shell (bash/zsh) | `export GOCELL_JWT_PRIVATE_KEY="$(cat private.pem)"` |
| Docker Compose | Use YAML literal block: <code>GOCELL_JWT_PRIVATE_KEY: \|</code> followed by indented PEM content |
| Kubernetes | Use a Secret with `stringData:` and literal PEM, or mount as a file |
| `.env` file | Replace newlines with `\n`: `GOCELL_JWT_PRIVATE_KEY=-----BEGIN RSA PRIVATE KEY-----\nMIIE...` |

If you see `"no PEM block found"` errors at startup, the newlines are likely stripped or escaped incorrectly.

## PREV_KEY_EXPIRES Recommended Value

Set `GOCELL_JWT_PREV_KEY_EXPIRES` to the latest possible expiry of any token signed with the old key:

```
PREV_KEY_EXPIRES = rotation_time + token_TTL
```

For example, if your JWT TTL is 15 minutes, set the expiry to 15 minutes after the rotation timestamp. This ensures all in-flight tokens naturally expire before the old key is pruned.

## Initial Deployment (Migration from Legacy Tokens)

This feature requires all tokens to carry a `kid` header. Tokens issued before this deployment (without kid) will be rejected.

**Recommended deployment strategy:**

1. Deploy the new code — all *new* tokens will include `kid`
2. Wait one full token TTL (e.g. 15 minutes) for all old tokens to expire
3. No further action needed — after the TTL window, all active tokens carry `kid`

Since GoCell is pre-production, there are no external consumers holding legacy tokens. If you have long-lived tokens in an external system, coordinate the deployment with those consumers.

## JWT Key Rotation Runbook

### Rotation steps

1. **Generate** a new RSA key pair (>= 2048 bits)
2. **Set environment variables:**
   - `GOCELL_JWT_PRIVATE_KEY` = new private key PEM
   - `GOCELL_JWT_PUBLIC_KEY` = new public key PEM
   - `GOCELL_JWT_PREV_PUBLIC_KEY` = old public key PEM
   - `GOCELL_JWT_PREV_KEY_EXPIRES` = `now + token_TTL` (RFC 3339, e.g. `2026-04-12T00:15:00Z`)
3. **Rolling restart** the service — new tokens use the new key; old tokens remain valid
4. **After PREV_KEY_EXPIRES passes**, remove the `PREV` env vars (optional cleanup)

### Rollback

If the new key is compromised or causes issues:
1. Revert `GOCELL_JWT_PRIVATE_KEY` / `GOCELL_JWT_PUBLIC_KEY` to the old key
2. Remove `GOCELL_JWT_PREV_*` vars
3. Restart — service reverts to the old key, all new tokens use the old key again

### Verification

After rotation, confirm:
- New tokens contain the new kid: decode a token header and check `kid` field
- Old tokens still verify: test with a token issued before the restart
- Logs show "key activated" for the new kid and "key demoted to verification-only" for the old kid

## Middleware Integration (unchanged)

The existing `AuthMiddleware` and `ServiceTokenMiddleware` signatures remain compatible. The key set and key ring integrate behind the existing `TokenVerifier` interface — no changes to middleware.go or route registration are needed.
