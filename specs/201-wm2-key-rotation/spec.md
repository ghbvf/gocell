# Feature Specification: JWT kid Rotation & HMAC Key Ring

**Feature Branch**: `201-wm2-key-rotation`  
**Created**: 2026-04-11  
**Status**: Draft  
**Input**: User description: "JWT kid rotation + HMAC key rotation for runtime/auth, based on Dex/gorilla/go-zero models"

## User Scenarios & Testing *(mandatory)*

### User Story 1 - JWT Tokens Include Key Identifier (Priority: P1)

When the authentication module issues a JWT token, it must include a key identifier (kid) in the token header so that verifiers can deterministically select the correct public key for validation. Today, the system signs tokens without a kid, which means verification can only work with a single known key — making key rotation impossible without downtime.

**Why this priority**: This is the foundational building block. Without kid in tokens, no rotation scheme can work. Every downstream story depends on this capability.

**Independent Test**: Can be fully tested by issuing a token and inspecting the header to confirm the kid is present and matches the signing key's identity. Delivers the ability to identify which key signed any given token.

**Acceptance Scenarios**:

1. **Given** the system has a configured signing key, **When** a JWT token is issued, **Then** the token header contains a `kid` field that uniquely identifies the signing key.
2. **Given** a token with a `kid` header, **When** the verifier receives the token, **Then** it uses the kid to select the matching public key from the key set rather than using a single hardcoded key.
3. **Given** a token with a `kid` that does not match any known key, **When** the verifier attempts validation, **Then** the token is rejected with an appropriate error.

---

### User Story 2 - JWT Key Set Supports Multiple Verification Keys (Priority: P1)

The system must support holding multiple public keys simultaneously — one active signing key and one or more verification-only keys (recently rotated out). This enables zero-downtime key rotation: tokens signed with the previous key remain valid until they naturally expire, while all new tokens are signed with the current key.

**Why this priority**: Equal priority with Story 1 because together they form the minimum viable key rotation capability. A kid without multi-key support, or multi-key without kid, are both incomplete.

**Independent Test**: Can be tested by configuring two key pairs, signing a token with the older key, and verifying that the system accepts it via the verification key set while only using the newer key for new signatures.

**Acceptance Scenarios**:

1. **Given** a key set with one active signing key and one verification-only key, **When** a token signed by the verification-only key is presented, **Then** the verifier accepts the token (the key is still trusted).
2. **Given** a key set with one active signing key, **When** a new token is issued, **Then** it is always signed by the active key (never by a verification-only key).
3. **Given** a verification-only key has passed its expiry time, **When** the key set is refreshed, **Then** the expired key is removed (pruned) and tokens signed by it are no longer accepted.

---

### User Story 3 - HMAC Secrets Support Graceful Rotation (Priority: P2)

For service-to-service tokens (service tokens signed with HMAC), the system must support a key ring of two secrets: the current signing secret and the previous secret. This allows operators to rotate the HMAC secret without invalidating in-flight tokens signed with the old secret.

**Why this priority**: Service tokens are critical for internal communication, but the HMAC rotation pattern is simpler than JWT kid rotation and has fewer moving parts. It builds on the same mental model but is independently valuable.

**Independent Test**: Can be tested by signing a token with the old secret, rotating to a new secret, and verifying that the token signed with the old secret is still accepted while new tokens use the new secret.

**Acceptance Scenarios**:

1. **Given** a key ring with two secrets [current, previous], **When** a new service token is signed, **Then** the current secret (position 0) is always used.
2. **Given** a key ring with two secrets, **When** a token signed with the previous secret is verified, **Then** the system accepts it by trying both secrets in order.
3. **Given** a key ring with two secrets, **When** a token signed with an unknown secret (not in the ring) is verified, **Then** the system rejects it.
4. **Given** a key ring with only one secret (no previous), **When** a token is verified, **Then** the system uses only the single secret and does not error due to the absent previous secret.

---

### User Story 4 - Key Lifecycle Transitions (Priority: P2)

Operators must be able to understand the state of each key in the system. Each JWT key progresses through a clear lifecycle: Active (signs new tokens) to Verification-only (validates existing tokens for a grace period) to Pruned (removed). Each HMAC secret is either Current (signs) or Previous (verifies only). The system logs lifecycle transitions for operational visibility.

**Why this priority**: Without observable lifecycle state, operators cannot confidently rotate keys or diagnose authentication failures during rotation windows.

**Independent Test**: Can be tested by triggering a key rotation and observing that the system correctly transitions the old key to verification-only status and logs the event.

**Acceptance Scenarios**:

1. **Given** an active JWT signing key, **When** a new key is loaded as the active key, **Then** the previous key transitions to verification-only status with an expiry time.
2. **Given** a verification-only key whose expiry has passed, **When** the system checks key state, **Then** the expired key is pruned from the key set.
3. **Given** any key lifecycle transition occurs, **When** the transition completes, **Then** a structured log entry records the transition type, key identifier, and timestamp.

---

### Edge Cases

- What happens when the system starts with no keys configured? The system must fail to start with a clear error message indicating missing key configuration.
- What happens when a JWT token has no `kid` header (legacy token)? The system must reject it, since all valid tokens in the new scheme carry a kid.
- What happens when the HMAC key ring is reconfigured with the same secret in both positions? The system should accept this gracefully (degenerate case, functionally equivalent to a single key).
- What happens when the verification-only key expiry is set to zero or negative? The key should be pruned immediately, effectively disabling the grace period.
- What happens when multiple key rotations occur in quick succession before the first grace period expires? The system should support at most one verification-only key; the oldest is pruned when a new one is added.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: System MUST include a `kid` (key identifier) in the header of every JWT token it issues.
- **FR-002**: System MUST derive the `kid` deterministically from the public key material so that the same key always produces the same kid.
- **FR-003**: System MUST verify JWT tokens by selecting the matching key from the key set using the token's `kid` header, not by trying all keys.
- **FR-004**: System MUST reject JWT tokens whose `kid` does not match any key in the current key set.
- **FR-005**: System MUST support a key set containing one active signing key and zero or more verification-only keys.
- **FR-006**: System MUST only use the active signing key for issuing new JWT tokens.
- **FR-007**: System MUST accept JWT tokens signed by any non-expired verification-only key in the key set.
- **FR-008**: System MUST prune verification-only keys whose expiry time has passed.
- **FR-009**: System MUST support an HMAC key ring of exactly two positions: current (index 0) and previous (index 1).
- **FR-010**: System MUST sign all new HMAC-based tokens using the current secret (index 0).
- **FR-011**: System MUST verify HMAC-based tokens by trying secrets in order: current first, then previous.
- **FR-012**: System MUST reject HMAC-based tokens that do not match any secret in the ring.
- **FR-013**: System MUST operate correctly when the HMAC key ring contains only one secret (no previous secret configured).
- **FR-014**: System MUST log all key lifecycle transitions (activation, demotion to verification-only, pruning) with structured fields including key identifier and timestamp.
- **FR-015**: System MUST fail to start if no signing key is configured, with a clear error message.
- **FR-016**: System MUST reject JWT tokens that do not contain a `kid` header.
- **FR-017**: System MUST load key configuration at startup from static configuration (environment or file); automatic rotation scheduling is out of scope.

### Key Entities

- **KeySet**: A collection of cryptographic keys for JWT operations. Contains exactly one active signing key and zero or more verification-only keys. Responsible for key lookup by identifier and lifecycle management (promotion, demotion, pruning).
- **Signing Key**: The currently active asymmetric key pair used to sign new JWT tokens. Has an associated key identifier. Only one signing key is active at any time.
- **Verification Key**: A previously active signing key that has been demoted. Retains the public key and key identifier. Has an expiry time after which it is pruned. Used only for validating existing tokens.
- **HMAC Key Ring**: An ordered pair of symmetric secrets for service token operations. Position 0 is the current signing secret; position 1 is the previous secret retained for verification during rotation.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: All newly issued JWT tokens contain a valid key identifier that matches the signing key, verified by inspecting 100% of tokens in test scenarios.
- **SC-002**: Token verification using kid-based key lookup succeeds for valid tokens within 1 millisecond per verification (no performance regression from multi-key support).
- **SC-003**: Key rotation completes with zero rejected valid tokens — tokens signed by the previous key continue to be accepted during the grace period.
- **SC-004**: HMAC secret rotation causes zero authentication failures for in-flight service tokens signed with the previous secret.
- **SC-005**: All key lifecycle transitions produce observable structured log entries that operators can query.
- **SC-006**: System startup fails deterministically when no signing key is configured, preventing silent misconfiguration.

## Assumptions

- The system currently uses a single RSA key pair and a single HMAC secret; this feature extends (not replaces) the existing authentication flow.
- RS256 is the only JWT signing algorithm in scope; multi-algorithm support is not planned and not needed.
- Key material is loaded from static configuration (environment variables or files) at startup. Dynamic/hot-reload configuration will be addressed in a future task (WM-34).
- Automatic rotation scheduling (timer-based key generation) is out of scope; operators trigger rotation by updating configuration and restarting.
- The HMAC key ring is fixed at size 2 (current + previous); larger rings are not required.
- A JWKS (JSON Web Key Set) public endpoint for cross-service key discovery is out of scope and will be addressed as an independent task.
- The existing middleware layer (`middleware.go`) and interfaces (`TokenVerifier`, `Authorizer`) do not need structural changes; the new key management integrates behind these interfaces.
- The verification-only key grace period defaults to the JWT token TTL (tokens signed before rotation remain valid until they naturally expire).
