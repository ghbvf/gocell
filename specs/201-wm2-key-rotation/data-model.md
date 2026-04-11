# Data Model: JWT kid Rotation & HMAC Key Ring

**Feature**: 201-wm2-key-rotation  
**Date**: 2026-04-11

## Entities

### KeySet

The central key management entity for JWT operations. Holds the currently active signing key and zero or more verification-only keys retained for validating previously-issued tokens.

**Attributes**:

| Attribute | Description | Constraints |
| --- | --- | --- |
| SigningKey | The active key pair used to sign new tokens | Exactly one; MUST have both private and public key |
| SigningKeyID | kid of the signing key (RFC 7638 thumbprint) | Derived deterministically from public key material |
| VerificationKeys | List of demoted public keys still trusted for validation | Zero or more; each has an expiry. Env loader supports 0-1; programmatic API supports 0-N. |

**State transitions**:

```
[New Key Loaded] → Active (signs new tokens)
                      │
                      ▼  (new key replaces it)
                 Verification-only (validates existing tokens)
                      │
                      ▼  (expiry passed)
                    Pruned (removed from set)
```

**Invariants**:
- Exactly one signing key at any time
- kid is unique across all keys in the set (signing + verification)
- A verification-only key whose expiry has passed MUST be pruned on next access
- Adding a new verification-only key when one already exists replaces the oldest

**Relationships**:
- Used by JWTIssuer (reads SigningKey for token creation)
- Used by JWTVerifier (reads all keys for kid-based lookup)

---

### VerificationKey

A previously active signing key that has been demoted. Retains only the public key for token validation during the grace period.

**Attributes**:

| Attribute | Description | Constraints |
| --- | --- | --- |
| PublicKey | RSA public key material | MUST meet MinRSAKeyBits (2048) |
| KeyID | kid (RFC 7638 thumbprint) | Immutable once assigned |
| ExpiresAt | Time after which this key is pruned | Typically set to demotion time + token TTL |

**Invariants**:
- Read-only after creation (no mutation of public key or kid)
- ExpiresAt MUST be in the future at creation time

---

### HMACKeyRing

An ordered pair of symmetric secrets for HMAC-based service token operations. Enables graceful secret rotation without invalidating in-flight tokens.

**Attributes**:

| Attribute | Description | Constraints |
| --- | --- | --- |
| Current | The active secret used for signing new tokens | Position 0; MUST NOT be empty |
| Previous | The previous secret retained for verification | Position 1; MAY be nil (single-key mode) |

**Invariants**:
- Current (position 0) is always used for signing
- Verification tries Current first, then Previous
- Ring size is fixed at 2 (no growth beyond [current, previous])
- When Previous is nil, verification uses only Current

**Relationships**:
- Used by ServiceTokenMiddleware (verification)
- Used by GenerateServiceToken (signing)

---

## Validation Rules

| Entity | Rule | Source |
| --- | --- | --- |
| KeySet | MUST have exactly one signing key at startup | FR-015 |
| KeySet | MUST reject tokens with unknown kid | FR-004 |
| KeySet | MUST prune expired verification keys | FR-008 |
| VerificationKey | Public key MUST be ≥ 2048 bits RSA | Existing MinRSAKeyBits |
| VerificationKey | ExpiresAt MUST be set at creation | FR-008 |
| HMACKeyRing | Current secret MUST NOT be empty | FR-015 |
| HMACKeyRing | Previous MAY be nil | FR-013 |

## No Database Changes

This feature operates entirely in-memory. Key material is loaded from static configuration (environment variables or PEM files) at startup. No database tables, migrations, or schema changes are required.
