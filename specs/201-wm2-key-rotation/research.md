# Research: JWT kid Rotation & HMAC Key Ring

**Feature**: 201-wm2-key-rotation  
**Date**: 2026-04-11  
**Source**: `docs/reviews/20260411-wm2-key-rotation-research.md` (4-role parallel analysis of 12+ open source projects)

## R-001: JWT kid Generation Strategy

**Decision**: RFC 7638 SHA-256 thumbprint of the public key.

**Rationale**: Deterministic — same key always yields the same kid, no extra storage needed. Used by Kubernetes apiserver. The research found two prevalent patterns (thumbprint vs. random UUID); thumbprint is preferred because it eliminates the need for a separate kid→key mapping during initial key loading.

**Alternatives considered**:
- Random 20-byte hex (Dex) — simpler but non-deterministic; requires storing kid alongside key material.
- UUID (Ory Hydra) — same problem as random hex.
- cert.Name (Casdoor) — too coupled to certificate lifecycle.

ref: dexidp/dex `server/rotation.go`, Kubernetes apiserver thumbprint pattern

## R-002: JWT Key Set Model

**Decision**: Adopt Dex's 3-state model: Active → Verification-only (with expiry) → Pruned. KeySet holds 1 signing key + N verification-only keys.

**Rationale**: Dex's model is the simplest that correctly handles zero-downtime rotation. One signing key issues all new tokens; demoted keys remain trusted until their expiry (set to `now + tokenTTL` at demotion time). Pruning happens lazily on next key set access. This matches GoCell's "simplicity first" constitution principle.

**Alternatives considered**:
- Vault Transit sliding window (MinDecryptionVersion / MinEncryptionVersion) — more powerful but requires versioned ciphertext prefixes; overkill for JWT where kid is in the header.
- Teleport 5-phase rotation — designed for CA rotation with rollback, far too complex for single-service JWT.
- Authelia static kid — no rotation capability.

ref: dexidp/dex `storage/storage.go` Keys struct

## R-003: JWT Verification Strategy

**Decision**: `KeyFunc(token) → key` pattern — select key by kid from the key set, not try-all-keys.

**Rationale**: Industry standard (Kratos, golang-jwt/v5 native pattern). O(1) key lookup vs. O(N) trial. The existing GoCell `JWTVerifier` already uses `jwt.Parse` with a `KeyFunc` callback; extending it to look up by kid is a minimal change.

**Alternatives considered**:
- Try-all-keys (gorilla/securecookie style) — only appropriate for HMAC where kid cannot be embedded; wrong model for JWT.

ref: go-kratos/kratos `middleware/auth/jwt/jwt.go` — KeyFunc pattern with context injection

## R-004: HMAC Key Ring Model

**Decision**: Position-based ring of size 2 (`[current, previous]`). Sign with `secrets[0]`, verify by trying `secrets[0]` then `secrets[1]`.

**Rationale**: Industry consensus across gorilla/securecookie, Django, Rails, go-zero. HMAC signatures cannot embed a kid, so position-based try-all is the correct model. Ring size 2 is sufficient because rotation replaces the previous key; go-zero's `PrevSecret` validates this as industrial practice.

**Alternatives considered**:
- Unlimited ring (gorilla) — unnecessary; 2 covers the rotation window.
- Tagged/kid-based (golang-jwt) — not applicable for HMAC service tokens where the signature format doesn't carry metadata.
- Frequency-based priority (go-zero) — optimization not needed at GoCell's scale; simple ordered try is clearer.

ref: zeromicro/go-zero `rest/token/tokenparser.go`, gorilla/securecookie `DecodeMulti`

## R-005: Key Lifecycle Observability

**Decision**: Structured slog entries for all lifecycle transitions (activation, demotion, pruning). No metrics in WM-2 scope.

**Rationale**: Constitution Principle VIII requires audit logging for key operations. Structured log fields (`kid`, `transition`, `timestamp`) are sufficient for WM-2; Prometheus metrics can be layered on later without API changes.

**Alternatives considered**:
- Rails `on_rotation` callback — useful pattern, but GoCell's static config model doesn't trigger runtime rotation events. When WM-34 adds hot-reload, callbacks can be added.
- Metrics-first — premature; slog is the universal observability primitive in GoCell.

## R-006: Configuration Loading Strategy

**Decision**: Static configuration at startup (env vars or PEM files). No auto-rotation scheduler in WM-2.

**Rationale**: The current `LoadKeysFromEnv()` pattern works. WM-2 extends it to load multiple keys (active + verification-only for JWT, current + previous for HMAC). Auto-rotation requires worker infrastructure (WM-34 dependency). The research found that Dex uses a timer goroutine, Vault uses `AutoRotatePeriod`, and cert-manager uses condition-based triggers — all require background scheduling that GoCell doesn't have yet.

**Alternatives considered**:
- File watcher (fsnotify) — adds external dependency to runtime/; deferred to WM-34.
- Config-hot-reload — WM-34 scope.

## R-007: Backward Compatibility with Existing Tokens

**Decision**: Tokens without a `kid` header are rejected.

**Rationale**: GoCell is pre-production; no external consumers hold tokens issued by the current system. The research doc confirms the system currently has no kid in tokens. A clean break (reject kidless tokens) avoids dual-path verification complexity. Constitution Principle IX (YAGNI) supports this.

**Alternatives considered**:
- Fallback to single-key verification for kidless tokens — adds permanent legacy code path for a pre-production system.
