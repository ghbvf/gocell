// Package errcode provides structured error codes for the GoCell framework.
// All errors exposed across package boundaries must use this package instead of
// bare errors.New.
package errcode

import (
	"encoding/json"
	"fmt"
	"log/slog"
)

// Code is a typed error code string.
type Code string

// Sentinel error codes used throughout the GoCell framework.
const (
	ErrMetadataInvalid    Code = "ERR_METADATA_INVALID"
	ErrMetadataNotFound   Code = "ERR_METADATA_NOT_FOUND"
	ErrCellNotFound       Code = "ERR_CELL_NOT_FOUND"
	ErrSliceNotFound      Code = "ERR_SLICE_NOT_FOUND"
	ErrContractNotFound   Code = "ERR_CONTRACT_NOT_FOUND"
	ErrAssemblyNotFound   Code = "ERR_ASSEMBLY_NOT_FOUND"
	ErrLifecycleInvalid   Code = "ERR_LIFECYCLE_INVALID"
	ErrDependencyCycle    Code = "ERR_DEPENDENCY_CYCLE"
	ErrValidationFailed   Code = "ERR_VALIDATION_FAILED"
	ErrReferenceBroken    Code = "ERR_REFERENCE_BROKEN"
	ErrInternal           Code = "ERR_INTERNAL"
	ErrServiceUnavailable Code = "ERR_SERVICE_UNAVAILABLE"
	ErrAuthUnauthorized   Code = "ERR_AUTH_UNAUTHORIZED"
	ErrAuthForbidden      Code = "ERR_AUTH_FORBIDDEN"
	ErrRateLimited        Code = "ERR_RATE_LIMITED"
	ErrCSRFOriginDenied   Code = "ERR_CSRF_ORIGIN_DENIED"
	ErrBodyTooLarge       Code = "ERR_BODY_TOO_LARGE"
	ErrJourneyNotFound    Code = "ERR_JOURNEY_NOT_FOUND"
	ErrTestExecution      Code = "ERR_TEST_EXECUTION"
	ErrCheckRefInvalid    Code = "ERR_CHECKREF_INVALID"
	ErrZeroTestMatch      Code = "ERR_ZERO_TEST_MATCH"
	ErrBusClosed          Code = "ERR_BUS_CLOSED"
	ErrCellMissingOutbox  Code = "ERR_CELL_MISSING_OUTBOX"
	ErrCellMissingCodec   Code = "ERR_CELL_MISSING_CODEC"
	// ErrCellMissingTokenIssuer signals that a Cell was started without a token
	// issuer dependency that it requires.
	ErrCellMissingTokenIssuer Code = "ERR_CELL_MISSING_TOKEN_ISSUER"
	ErrCellInvalidConfig      Code = "ERR_CELL_INVALID_CONFIG"
	// ErrCellPlatformUnsupported signals that a Cell option requested capability
	// that is not implemented on the current GOOS — distinct from
	// ErrCellInvalidConfig (configuration mistake) so operators can route
	// "this build was deployed to the wrong platform" failures separately from
	// "the deployment YAML is wrong". Surfaced fail-fast at cell.Init() time so
	// the failure is visible at process startup rather than during phase3b
	// LifecycleHook execution.
	ErrCellPlatformUnsupported Code = "ERR_CELL_PLATFORM_UNSUPPORTED"
	ErrSessionNotFound         Code = "ERR_SESSION_NOT_FOUND"
	ErrSessionConflict         Code = "ERR_SESSION_CONFLICT"
	ErrOrderNotFound           Code = "ERR_ORDER_NOT_FOUND"
	ErrDeviceNotFound          Code = "ERR_DEVICE_NOT_FOUND"
	ErrCommandNotFound         Code = "ERR_COMMAND_NOT_FOUND"
	ErrAdapterPGNoTx           Code = "ERR_ADAPTER_PG_NO_TX"
	ErrAuthKeyInvalid          Code = "ERR_AUTH_KEY_INVALID"
	// ErrAuthVerifierConfig signals a JWT verifier construction error — e.g.
	// required configuration (WithExpectedAudiences) was not provided.
	// Distinct from ErrAuthKeyInvalid (key material) so operators can route
	// verifier misconfiguration separately from cryptographic key failures.
	ErrAuthVerifierConfig Code = "ERR_AUTH_VERIFIER_CONFIG"
	// ErrAuthTokenInvalid signals that a JWT or service token failed
	// cryptographic or structural validation.
	ErrAuthTokenInvalid Code = "ERR_AUTH_TOKEN_INVALID"
	// ErrAuthTokenExpired signals that a JWT or service token has passed its
	// expiry time.
	ErrAuthTokenExpired Code = "ERR_AUTH_TOKEN_EXPIRED"
	// ErrAuthInvalidTokenIntent signals that a JWT's token_use claim (and/or
	// its JOSE typ header) does not match the expected intent for the current
	// request scope — e.g., a refresh token presented at a business endpoint,
	// or an access token presented at /auth/refresh. Middleware and slice
	// layers map this to a generic ERR_AUTH_UNAUTHORIZED / ERR_AUTH_REFRESH_FAILED
	// response to prevent token-type enumeration; the specific code is only
	// visible in logs.
	//
	// ref: RFC 8725 §2.8 / §3.11 (JWT token confusion threat model)
	// ref: AWS Cognito token_use claim, Keycloak typ header constants
	ErrAuthInvalidTokenIntent Code = "ERR_AUTH_INVALID_TOKEN_INTENT"

	// Access-core cell error codes.
	ErrAuthUserNotFound         Code = "ERR_AUTH_USER_NOT_FOUND"
	ErrAuthUserDuplicate        Code = "ERR_AUTH_USER_DUPLICATE"
	ErrAuthRoleNotFound         Code = "ERR_AUTH_ROLE_NOT_FOUND"
	ErrAuthRoleDuplicate        Code = "ERR_AUTH_ROLE_DUPLICATE"
	ErrAuthInvalidInput         Code = "ERR_AUTH_INVALID_INPUT"
	ErrAuthUserLocked           Code = "ERR_AUTH_USER_LOCKED"
	ErrAuthSessionInvalidInput  Code = "ERR_AUTH_SESSION_INVALID_INPUT"
	ErrAuthIdentityInvalidInput Code = "ERR_AUTH_IDENTITY_INVALID_INPUT"
	ErrAuthLoginInvalidInput    Code = "ERR_AUTH_LOGIN_INVALID_INPUT"
	ErrAuthLoginFailed          Code = "ERR_AUTH_LOGIN_FAILED"
	ErrAuthLogoutInvalidInput   Code = "ERR_AUTH_LOGOUT_INVALID_INPUT"
	ErrAuthRefreshInvalidInput  Code = "ERR_AUTH_REFRESH_INVALID_INPUT"
	ErrAuthRefreshFailed        Code = "ERR_AUTH_REFRESH_FAILED"
	ErrAuthRefreshUnavailable   Code = "ERR_AUTH_REFRESH_UNAVAILABLE"
	ErrAuthInvalidToken         Code = "ERR_AUTH_INVALID_TOKEN"
	ErrAuthRBACInvalidInput     Code = "ERR_AUTH_RBAC_INVALID_INPUT"
	ErrAuthKeyMissing           Code = "ERR_AUTH_KEY_MISSING"
	ErrAuthSelfDelete           Code = "ERR_AUTH_SELF_DELETE"
	// ErrAuthRoleFetchFailed signals that role-name resolution at the time of
	// session-token issuance failed due to an infrastructure fault (RoleRepository
	// unavailable, query error, etc.). Session minting is fail-closed: callers
	// must abort login / refresh / token re-issuance rather than sign a token
	// carrying empty roles, which would look like a successful authentication
	// but silently strip every RBAC capability. Maps to HTTP 500.
	ErrAuthRoleFetchFailed Code = "ERR_AUTH_ROLE_FETCH_FAILED"
	// ErrAuthPasswordResetRequired signals that the authenticated subject must
	// change their password before accessing business endpoints. The middleware
	// enforces this when the JWT claim password_reset_required is true.
	// Only the exempt endpoints (POST /api/v1/access/users/{id}/password and
	// DELETE /api/v1/access/sessions/{id}) bypass this check.
	ErrAuthPasswordResetRequired Code = "ERR_AUTH_PASSWORD_RESET_REQUIRED"
	// ErrSetupAlreadyInitialized signals that the interactive first-run admin
	// endpoint (POST /api/v1/access/setup/admin) was invoked after the system
	// already has at least one admin. The caller should authenticate via
	// /api/v1/access/sessions/login instead. Maps to HTTP 410 Gone: the endpoint
	// is permanently retired for the lifetime of the deployment (one-shot
	// lifecycle), not just temporarily conflicting.
	ErrSetupAlreadyInitialized Code = "ERR_SETUP_ALREADY_INITIALIZED"

	// Config-core cell error codes.
	ErrConfigNotFound            Code = "ERR_CONFIG_NOT_FOUND"
	ErrConfigDuplicate           Code = "ERR_CONFIG_DUPLICATE"
	ErrConfigInvalidInput        Code = "ERR_CONFIG_INVALID_INPUT"
	ErrConfigPublishInvalidInput Code = "ERR_CONFIG_PUBLISH_INVALID_INPUT"
	ErrConfigRepoNotFound        Code = "ERR_CONFIG_REPO_NOT_FOUND"
	ErrConfigRepoDuplicate       Code = "ERR_CONFIG_REPO_DUPLICATE"
	ErrConfigRepoQuery           Code = "ERR_CONFIG_REPO_QUERY"
	ErrFlagNotFound              Code = "ERR_FLAG_NOT_FOUND"
	ErrFlagDuplicate             Code = "ERR_FLAG_DUPLICATE"
	ErrFlagInvalidInput          Code = "ERR_FLAG_INVALID_INPUT"
	ErrFlagRepoQuery             Code = "ERR_FLAG_REPO_QUERY"

	// Audit-core cell error codes.
	ErrAuditRepoNotFound Code = "ERR_AUDIT_REPO_NOT_FOUND"
	ErrAuditRepoQuery    Code = "ERR_AUDIT_REPO_QUERY"
	ErrArchiveUpload     Code = "ERR_ARCHIVE_UPLOAD"
	ErrArchiveMarshal    Code = "ERR_ARCHIVE_MARSHAL"
	ErrNotImplemented    Code = "ERR_NOT_IMPLEMENTED"

	// Pagination / validation error codes.
	ErrCursorInvalid     Code = "ERR_CURSOR_INVALID"
	ErrPageSizeExceeded  Code = "ERR_PAGE_SIZE_EXCEEDED"
	ErrInvalidTimeFormat Code = "ERR_INVALID_TIME_FORMAT"
	// ErrValidationInvalidUUID signals that a UUID-typed input (currently used
	// for HTTP path parameters declared with `format: uuid` in contract.yaml)
	// failed to parse. Maps to HTTP 400 to distinguish "malformed identifier
	// shape" from "identifier valid but resource not found" (404). Issued at
	// handler edge by httputil.ParseUUIDPathParam.
	ErrValidationInvalidUUID Code = "ERR_VALIDATION_INVALID_UUID"

	// Resilience middleware error codes.
	ErrCircuitOpen Code = "ERR_CIRCUIT_OPEN"

	// Outbox relay health error codes.
	// ErrRelayBudgetExhausted signals that an outbox relay operation (poll /
	// reclaim / cleanup) has exceeded its consecutive-failure threshold, tripping
	// the failure budget and marking /readyz unhealthy.
	ErrRelayBudgetExhausted Code = "ERR_RELAY_BUDGET_EXHAUSTED"

	// Observability configuration error.
	// Raised by kernel / runtime observability constructors when a
	// required dependency (Provider, cellID) is missing or malformed.
	// Semantically an initialisation error — distinct from
	// ErrValidationFailed (user-input validation) so operators can route
	// the two through different dashboards.
	ErrObservabilityConfigInvalid Code = "ERR_OBSERVABILITY_CONFIG_INVALID"

	// WebSocket runtime error codes.
	ErrWSConnNotFound   Code = "ERR_WS_CONN_NOT_FOUND"
	ErrWSAlreadyStarted Code = "ERR_WS_ALREADY_STARTED"
	ErrWSAlreadyStopped Code = "ERR_WS_ALREADY_STOPPED"
	ErrWSHubStopping    Code = "ERR_WS_HUB_STOPPING"
	ErrWSHubNotRunning  Code = "ERR_WS_HUB_NOT_RUNNING"
	ErrWSMaxConns       Code = "ERR_WS_MAX_CONNS"
	// ErrWebsocketOriginsMissing signals that an UpgradeHandler was constructed
	// with an empty AllowedOrigins list. The handler rejects construction
	// fail-fast rather than silently accepting connections from any origin.
	// Operators must supply at least one explicit origin host pattern.
	//
	// Example:
	//
	//	handler, err := adapterws.UpgradeHandler(hub, adapterws.UpgradeConfig{
	//	    AllowedOrigins: []string{"https://example.com"},
	//	})
	//
	// ref: docs/plans/202604270020-1-2-ci-3-claude-ship-reactive-bachman.md PR-MODE-1
	ErrWebsocketOriginsMissing Code = "ERR_WEBSOCKET_ORIGINS_MISSING"
	// ErrWebsocketOriginsInvalid signals that AllowedOrigins contains a pattern
	// that would disable browser Origin protection, such as the full wildcard
	// "*".
	ErrWebsocketOriginsInvalid Code = "ERR_WEBSOCKET_ORIGINS_INVALID"
	// ErrWebsocketHubMissing signals that UpgradeHandler was constructed with
	// a nil *rtws.Hub. Composition-time fail-fast — letting nil through would
	// defer the failure until the first HTTP request, violating the
	// error-first construction contract (PR-MODE-6.1).
	//
	// Example:
	//
	//	handler, err := adapterws.UpgradeHandler(nil, adapterws.UpgradeConfig{
	//	    AllowedOrigins: []string{"https://example.com"},
	//	})
	//	var ec *errcode.Error
	//	if errors.As(err, &ec) && ec.Code == errcode.ErrWebsocketHubMissing { /* misconfig */ }
	ErrWebsocketHubMissing Code = "ERR_WEBSOCKET_HUB_MISSING"
	// ErrWebsocketAuthenticatorMissing signals that an UpgradeHandler was
	// constructed without an Authenticator. The error is returned by the
	// error-form constructor and panicked by the static-wiring twin.
	//
	// SEC-FAIL-CLOSED (PR-V1-SEC-WS-AUTH-ACL): nil Authenticator must surface
	// at composition root, not at the first HTTP request.
	ErrWebsocketAuthenticatorMissing Code = "ERR_WEBSOCKET_AUTHENTICATOR_MISSING"
	// ErrWebsocketBroadcastFilterMissing signals that Hub.BroadcastFilter was
	// called with a nil filter function. fail-closed: full-broadcast must be
	// expressed explicitly via `func(Conn) bool { return true }`.
	ErrWebsocketBroadcastFilterMissing Code = "ERR_WEBSOCKET_BROADCAST_FILTER_MISSING"
	// ErrWebsocketBroadcastSubjectMissing signals that Hub.BroadcastToSubject
	// was called with an empty subject string.
	ErrWebsocketBroadcastSubjectMissing Code = "ERR_WEBSOCKET_BROADCAST_SUBJECT_MISSING"
	// ErrWebsocketUpgradeUnauthenticated signals that the UpgradeHandler
	// rejected an incoming HTTP request because the Authenticator returned
	// either absent (no credential) or a present-but-invalid credential. The
	// HTTP response status is 401 and the body is plain text; this code is
	// only used in server-side slog records.
	ErrWebsocketUpgradeUnauthenticated Code = "ERR_WEBSOCKET_UPGRADE_UNAUTHENTICATED"
	// ErrWebsocketSlowClient signals that the Hub evicted a connection because
	// its send buffer filled (gorilla/websocket select-default-drop pattern).
	// Emitted from BroadcastFilter / BroadcastToSubject / Send.
	ErrWebsocketSlowClient Code = "ERR_WEBSOCKET_SLOW_CLIENT"

	// Outbox envelope error codes.
	// ErrEnvelopeSchema signals that an inbound wire message does not conform
	// to the expected envelope schema — unknown schemaVersion, missing required
	// fields, or corrupt JSON. Consumers must Reject (not retry) on this error.
	ErrEnvelopeSchema Code = "ERR_ENVELOPE_SCHEMA"

	// Bootstrap lifecycle error codes.
	// ErrBootstrapLifecycle signals that a lifecycle operation was called in an
	// invalid state — e.g. Append or Start called after the lifecycle has already
	// started. Distinct from ErrLifecycleInvalid (metadata validation) so
	// operators can route runtime lifecycle faults separately.
	ErrBootstrapLifecycle Code = "ERR_BOOTSTRAP_LIFECYCLE"

	// Refresh token store error code (runtime/auth/refresh).
	//
	// ErrRefreshTokenRejected is the single public sentinel emitted by
	// refresh.Store implementations for every unhappy path (malformed token,
	// unknown selector, verifier mismatch, expired, revoked, reused beyond
	// grace). Internal reasons are observed through the structured slog field
	// `reason` rather than the error shape, eliminating enumeration and timing
	// side-channels. CategoryAuth — OAuth2 RFC 6749 §10.4 attack signal when
	// reuse detected; handler maps to HTTP 401 regardless of internal reason.
	ErrRefreshTokenRejected Code = "ERR_REFRESH_TOKEN_REJECTED"

	// KeyProvider error codes.
	// ErrKeyProviderKeyNotFound signals that the requested key ID is not
	// present in the provider's keyring — e.g. a historical key that has been
	// purged. Callers must not fall back to plaintext; surface as a config error.
	// Permanent error — EventBus handlers should return DispositionReject.
	// Maps to Vault HTTP 404 (key or mount not found).
	ErrKeyProviderKeyNotFound Code = "ERR_KEY_PROVIDER_KEY_NOT_FOUND"
	// ErrKeyProviderAuthFailed signals that the Vault token has been revoked,
	// has insufficient permissions, or has expired (Vault HTTP 403 Forbidden).
	// Distinct from ErrKeyProviderKeyNotFound (404 — key absent) so operators
	// can route permission/token failures separately from missing-key failures.
	//
	// Use when:
	//   - Vault returns HTTP 403 on any transit read/encrypt/decrypt path.
	//   - Token revoked (revoke-accessor) or token lacks required capabilities.
	//   - Permission denied on transit/keys/{name} or transit/encrypt|decrypt.
	//
	// Permanent error — EventBus handlers should return DispositionReject.
	// Operators must rotate the Vault token (not retry the operation).
	ErrKeyProviderAuthFailed Code = "ERR_KEY_PROVIDER_AUTH_FAILED"
	// ErrKeyProviderEncryptFailed signals a KMS encrypt-side operation failure
	// (Vault Transit encrypt API error, malformed response, etc.). Distinct from
	// ErrKeyProviderDecryptFailed so callers and log aggregators can route
	// encrypt-side failures (usually transient / retriable) separately from
	// decrypt-side failures (usually permanent / data integrity signal).
	// Permanent error — EventBus handlers should return DispositionReject.
	ErrKeyProviderEncryptFailed Code = "ERR_KEY_PROVIDER_ENCRYPT_FAILED"
	// ErrKeyProviderDecryptFailed signals an AES-GCM authentication failure,
	// wrong key, or malformed ciphertext. Fail-closed: callers must surface
	// this as an error and never return raw ciphertext or empty string.
	// Permanent error — EventBus handlers should return DispositionReject.
	ErrKeyProviderDecryptFailed Code = "ERR_KEY_PROVIDER_DECRYPT_FAILED"
	// ErrKeyProviderRotateFailed signals a key-rotation operation failure
	// (Vault rotate API returned an error, new key version could not be read
	// back, malformed response). Distinct from ErrKeyProviderKeyNotFound so
	// rotation-path retries and alerting do not confuse "key absent" with
	// "rotation API unreachable".
	// Permanent error — EventBus handlers should return DispositionReject.
	ErrKeyProviderRotateFailed Code = "ERR_KEY_PROVIDER_ROTATE_FAILED"
	// ErrKeyProviderTransient signals a transient KeyProvider failure that is
	// safe to retry after back-off. Maps to Vault HTTP responses indicating
	// temporary unavailability:
	//
	//   - 503 Service Unavailable (sealed, standby, maintenance)
	//   - 429 Too Many Requests (rate-limited)
	//   - 408 Request Timeout / network timeout
	//
	// Contrast with ErrKeyProviderEncryptFailed / ErrKeyProviderDecryptFailed /
	// ErrKeyProviderKeyNotFound / ErrKeyProviderRotateFailed, which signal
	// permanent conditions (400 Bad Request, 403 Forbidden, 404 Not Found).
	//
	// EventBus Disposition routing:
	//   - ErrKeyProviderTransient → DispositionRequeue (back-off retry)
	//   - All other KeyProvider errors → DispositionReject (DLX)
	//
	// Use IsTransient to check the full error chain.
	//
	// ref: aws/aws-encryption-sdk-python src/aws_encryption_sdk/exceptions.py
	// (GenerateKeyError / DecryptKeyError transient vs permanent split).
	ErrKeyProviderTransient Code = "ERR_KEY_PROVIDER_TRANSIENT"
	// ErrConfigDecryptFailed signals that a sensitive config value could not be
	// decrypted at the repository boundary. Maps to HTTP 500 (internal error).
	// Symmetric with ErrConfigEncryptFailed for the encrypt boundary.
	ErrConfigDecryptFailed Code = "ERR_CONFIG_DECRYPT_FAILED"
	// ErrConfigEncryptFailed signals that a sensitive config value could not be
	// encrypted at the repository boundary (Encrypt/EncryptVersion). Maps to
	// HTTP 500 (internal error). Symmetric with ErrConfigDecryptFailed so
	// alerting systems can filter all crypto failures via a single code prefix
	// and distinguish them from generic ErrConfigRepoQuery DB failures.
	ErrConfigEncryptFailed Code = "ERR_CONFIG_ENCRYPT_FAILED"
	// ErrConfigKeyMissing signals that a required encryption key (e.g. GOCELL_CONFIGCORE_MASTER_KEY
	// or vault token) is absent at startup. Triggers fail-fast in postgres mode.
	ErrConfigKeyMissing Code = "ERR_CONFIG_KEY_MISSING"
	// ErrVaultAuthFailed signals a Vault auth method failure: missing or malformed
	// credentials, unknown VAULT_AUTH_METHOD, AppRole / Kubernetes Login returned
	// error, static token rejected by real-mode guard, or re-authentication loop
	// exhausted (ctx canceled).
	//
	// Distinct from ErrKeyProviderAuthFailed, which signals runtime Vault 403 on
	// transit encrypt/decrypt paths (an in-flight operation failure). This code
	// is used exclusively at bootstrap and background re-auth loop boundaries.
	//
	// Permanent at boot: operators must fix configuration before restart.
	// During re-auth loop: ctx cancellation is the only exit condition; this
	// code is returned when ctx is canceled while retrying.
	//
	// Category: default CategoryInfra (consistent with ErrVault* / ErrKeyProvider* siblings).
	ErrVaultAuthFailed Code = "ERR_VAULT_AUTH_FAILED"

	// Control-plane startup configuration errors (cmd/corebundle).
	//
	// ErrControlplaneServiceSecretMissing signals that GOCELL_SERVICE_SECRET is
	// unset in adapter mode "real", so the /internal/v1/* service-token guard
	// cannot be constructed. Produced by cmd/corebundle.internalGuardFromEnv
	// and cmd/corebundle.SharedDeps.validateControlPlane; fails the binary at
	// startup before any listener binds. Never reaches the HTTP layer in
	// practice.
	//
	// Distinct from ErrValidationFailed (user-input validation) so operators
	// can grep startup logs specifically for control-plane misconfigurations.
	ErrControlplaneServiceSecretMissing Code = "ERR_CONTROLPLANE_SERVICE_SECRET_MISSING"

	// ErrControlplaneNonceStoreMissing signals that the control-plane
	// service-token guard was constructed without a replay-safe NonceStore
	// (either nil or a NoopNonceStore sentinel) while adapter mode is "real".
	// Produced by cmd/corebundle.SharedDeps.validateControlPlane; fails the
	// binary at startup. Operators must inject an InMemoryNonceStore (single
	// pod) or a shared store (multi-pod) before restart.
	//
	// This is the closure of the P1 replay window identified in six-role
	// review (backlog S-nonce, 2026-04-24): a captured valid service token
	// must not be replayable within its 5-minute validity window.
	ErrControlplaneNonceStoreMissing Code = "ERR_CONTROLPLANE_NONCE_STORE_MISSING"

	// ErrControlplaneClaimerNotDistributed signals that a real multi-pod
	// corebundle deployment is using a process-local outbox idempotency
	// Claimer. Operators must configure Redis so consumers coordinate
	// idempotency across pods before restart.
	ErrControlplaneClaimerNotDistributed Code = "ERR_CONTROLPLANE_CLAIMER_NOT_DISTRIBUTED"

	// ErrAuthReplayDetected distinguishes a service-token replay signal from
	// generic authentication failures (invalid MAC, expired token, missing
	// header). Machine-side consumers (monitoring, alerting, SDKs) can match
	// this code exclusively to trigger replay-specific escalation without
	// parsing the human-readable message.
	//
	// Maps to HTTP 401 Unauthorized — the client must not retry with the same
	// token; a new token with a fresh nonce is required.
	ErrAuthReplayDetected Code = "ERR_AUTH_REPLAY_DETECTED"

	// ErrNonceStoreFull signals that the in-memory nonce store has reached its
	// maximum entry capacity and could not reclaim any expired entries. The
	// request is rejected to prevent unbounded memory growth; the condition is
	// transient (existing nonces will expire at the next TTL boundary).
	//
	// Maps to HTTP 503 Service Unavailable — callers should retry after a brief
	// back-off. Distinct from ErrAuthReplayDetected (security signal) so
	// operators can route capacity alerts separately from security alerts.
	ErrNonceStoreFull Code = "ERR_AUTH_NONCE_STORE_FULL"

	// ErrReadyzVerboseDenied signals that /readyz?verbose was requested but
	// the supplied X-Readyz-Token header did not match the configured verbose
	// token (or no token was configured while verbose is still being
	// requested). Maps to HTTP 401 Unauthorized.
	//
	// Introduced by PR-A35: prior behavior silently downgraded mismatched
	// requests to a plain 200 (without the verbose body), masking
	// misconfiguration. The strict 401 makes configuration errors observable
	// to operators without affecting the bare /readyz endpoint used by
	// Kubernetes readinessProbes (which must not pass ?verbose).
	ErrReadyzVerboseDenied Code = "ERR_READYZ_VERBOSE_DENIED"

	// ErrControlplaneVerboseTokenMissing signals that
	// GOCELL_READYZ_VERBOSE_TOKEN was not set at startup in a mode that
	// requires the token to be explicitly configured. Starting with PR-A35
	// this invariant holds in all adapter modes (not just "real"). Operators
	// who genuinely do not want the verbose endpoint exposed at all should
	// explicitly acknowledge that via the WithReadyzVerboseDisabled option
	// instead of relying on an absent token to silently disable gating.
	ErrControlplaneVerboseTokenMissing Code = "ERR_CONTROLPLANE_VERBOSE_TOKEN_MISSING"

	// ErrControlplaneVerboseTokenSample signals that
	// GOCELL_READYZ_VERBOSE_TOKEN is set to the literal placeholder shipped
	// in .env.example. A production deploy that copies the sample env and
	// rotates only the other secrets would otherwise pass startup with a
	// publicly-known token; rejecting the literal value at startup forces
	// operators to mint a real high-entropy secret. Distinct from
	// ErrControlplaneVerboseTokenMissing so dashboards and runbooks can
	// distinguish "forgot to configure" from "configured with the sample".
	ErrControlplaneVerboseTokenSample Code = "ERR_CONTROLPLANE_VERBOSE_TOKEN_SAMPLE"

	// ErrAdapterEndpointNotTLS signals that a remote adapter endpoint (Redis,
	// Vault, S3, etc.) was configured with a non-TLS scheme and is not a loopback
	// address. Adapters call secutil.ValidateTLSEndpoint at construction time and
	// fail-closed with this code so that plaintext connections to production
	// infrastructure are rejected at startup rather than at first use.
	//
	// Loopback addresses (127.0.0.1, ::1, localhost) are exempt to allow
	// testcontainer / dev-CI workflows without TLS termination.
	//
	// ref: docs/plans/202604270020-1-2-ci-3-claude-ship-reactive-bachman.md PR-MODE-1
	ErrAdapterEndpointNotTLS Code = "ERR_ADAPTER_ENDPOINT_NOT_TLS"

	// ErrDistlockTimeout is returned by Locker.Acquire when the requested key is
	// already held by another holder and the lock cannot be granted immediately.
	// Maps to HTTP 409 Conflict at the API boundary.
	// runtime/distlock aliases this as ErrLockTimeout for ergonomic local use.
	ErrDistlockTimeout Code = "ERR_DISTLOCK_TIMEOUT"

	// ErrClientCanceled signals that the request was canceled by the client
	// before the server finished processing — typically context.Canceled
	// surfaced from a downstream IO operation. Maps to HTTP 499 (nginx
	// "Client Closed Request"); operators should treat as a client-direction
	// signal, not a server fault, so it never pollutes 5xx error-rate SLOs.
	//
	// IO-boundary helpers should wrap context.Canceled errors with this
	// code so the HTTP layer routes the response to 499 + slog.Warn via the
	// 4xx response writer path.
	//
	// Distinct from ErrServerTimeout (504): this is "the client gave up",
	// the latter is "the server's own deadline fired". Splitting the two
	// codes lets dashboards / SDK retry policies / circuit breakers react
	// differently — 499 is benign noise, 504 is a real timeout to alert on.
	//
	// ref: nginx ngx_http_special_response.c — 499 emitted on client disconnect
	// ref: OTel semantic conventions http-spans.md — 4xx server spans Unset;
	//      intentional cancellation should not set error.type
	ErrClientCanceled Code = "ERR_CLIENT_CANCELED"

	// ErrServerTimeout signals that the request exceeded a server-side or
	// upstream-inherited deadline — typically context.DeadlineExceeded
	// surfaced from a downstream IO operation. Maps to HTTP 504 (Gateway
	// Timeout); operators should treat as a real server-direction failure
	// and route through the standard 5xx error path (slog.Error + 5xx error
	// rate / SLO bucket).
	//
	// IO-boundary helpers should wrap context.DeadlineExceeded errors with
	// this code (NOT ErrClientCanceled) so the HTTP layer surfaces the
	// timeout via 504 instead of conflating it with client disconnect.
	//
	// Distinct from ErrClientCanceled (499): this is "the server's deadline
	// fired", the latter is "the client gave up". Aligns with NGINX (499 vs
	// 504), Kratos transport/http/status (Canceled→499, DeadlineExceeded→504),
	// and standard ingress / load-balancer expectations for retry semantics.
	//
	// CategoryInfra so IsInfraError predicates (health bucket, retry
	// classifiers) treat timeouts as real infrastructure faults.
	//
	// ref: RFC 9110 §15.6.5 — 504 Gateway Timeout
	// ref: kratos transport/http/status — Canceled→499, DeadlineExceeded→504
	ErrServerTimeout Code = "ERR_SERVER_TIMEOUT"

	// ErrListenerAuthChainMissing signals that a bootstrap listener was declared
	// with a nil authChain. Bootstrap phase0 fail-fasts with this code so that
	// listeners without explicit authentication intent are rejected at startup
	// rather than silently accepting all requests. Operators must pass an
	// explicit authChain — use []cell.ListenerAuth{cell.AuthNone{}} for
	// listeners that genuinely require no authentication (e.g. HealthListener
	// behind a Kubernetes probe path).
	//
	// ref: docs/plans/202604270020-1-2-ci-3-claude-ship-reactive-bachman.md PR-MODE-1
	ErrListenerAuthChainMissing Code = "ERR_LISTENER_AUTH_CHAIN_MISSING"

	// ErrReadyzVerboseUnconfigured signals that /readyz?verbose was requested
	// but neither a verbose token nor the explicit disabled flag has been
	// configured. This fail-closed default forces operators to make an explicit
	// choice — configure a token (WithReadyzVerboseToken) or disable the verbose
	// endpoint (WithReadyzVerboseDisabled) — rather than leaking internal health
	// details to unauthenticated callers by default.
	//
	// Maps to HTTP 401 Unauthorized at the health handler layer.
	//
	// ref: docs/plans/202604270020-1-2-ci-3-claude-ship-reactive-bachman.md PR-MODE-1
	ErrReadyzVerboseUnconfigured Code = "ERR_READYZ_VERBOSE_UNCONFIGURED"

	// Idempotency error codes (kernel/idempotency).
	//
	// ErrIdempotencyLeaseExpired signals that the processing lease is no longer
	// held — either it expired naturally or another consumer claimed it.
	// Callers MUST stop business logic and proceed to Release. Maps to HTTP 409.
	ErrIdempotencyLeaseExpired Code = "ERR_IDEMPOTENCY_LEASE_EXPIRED"
	// ErrIdempotencyNoClaimLease signals that Receipt methods were called for a
	// Claim result that did not acquire a processing lease. Maps to HTTP 409.
	ErrIdempotencyNoClaimLease Code = "ERR_IDEMPOTENCY_NO_CLAIM_LEASE"

	// Metrics error codes (kernel/observability/metrics).
	//
	// ErrMetricsLabelMismatch signals that the supplied Labels do not exactly
	// cover the registered LabelNames. Maps to HTTP 400 (caller/programmer error).
	ErrMetricsLabelMismatch Code = "ERR_METRICS_LABEL_MISMATCH"
	// ErrMetricsLabelValueIllegal signals that a label value contains a separator
	// reserved by the OTel-provider cache key. Maps to HTTP 400.
	ErrMetricsLabelValueIllegal Code = "ERR_METRICS_LABEL_VALUE_ILLEGAL"

	// Outbox error codes (kernel/outbox).
	//
	// ErrOutboxDegraded signals that the fail-open drop ratio has exceeded the
	// configured threshold. The /readyz aggregator maps this to HTTP 200 +
	// status="degraded" rather than 503, but the code itself maps to 503 for
	// direct HTTP boundary use. Maps to HTTP 503 Service Unavailable.
	ErrOutboxDegraded Code = "ERR_OUTBOX_DEGRADED"

	// Worker error codes (kernel/worker).
	//
	// ErrWorkerExitedEarly signals that a Worker.Start returned nil while the
	// group context was still live — an abnormal silent exit modeled as an error
	// so WorkerGroup can propagate the failure. Maps to HTTP 500.
	ErrWorkerExitedEarly Code = "ERR_WORKER_EXITED_EARLY"

	// SecureCookie error codes (pkg/securecookie).
	//
	// ErrSecureCookieHashKeyTooShort signals that hashKey is shorter than the
	// minimum required length (32 bytes). Maps to HTTP 400.
	ErrSecureCookieHashKeyTooShort Code = "ERR_SECURECOOKIE_HASH_KEY_TOO_SHORT"
	// ErrSecureCookieInvalidBlockKey signals that blockKey is not nil and not
	// one of the valid AES key sizes (16, 24, or 32 bytes). Maps to HTTP 400.
	ErrSecureCookieInvalidBlockKey Code = "ERR_SECURECOOKIE_INVALID_BLOCK_KEY"
	// ErrSecureCookieEncodingTooShort signals that the encoded cookie value is
	// shorter than the minimum required length. Maps to HTTP 400.
	ErrSecureCookieEncodingTooShort Code = "ERR_SECURECOOKIE_ENCODING_TOO_SHORT"
	// ErrSecureCookieHMACInvalid signals that HMAC verification failed —
	// the cookie has been tampered with or forged. Maps to HTTP 400.
	ErrSecureCookieHMACInvalid Code = "ERR_SECURECOOKIE_HMAC_INVALID"
	// ErrSecureCookieExpired signals that the cookie has exceeded its configured
	// max-age. Maps to HTTP 400.
	ErrSecureCookieExpired Code = "ERR_SECURECOOKIE_EXPIRED"
	// ErrSecureCookieDecryptFailed signals that AES-GCM decryption failed —
	// wrong key or corrupt ciphertext. Maps to HTTP 400.
	ErrSecureCookieDecryptFailed Code = "ERR_SECURECOOKIE_DECRYPT_FAILED"

	// Auth nonce error codes (runtime/auth).
	//
	// ErrAuthNonceReused signals that a nonce has already been consumed within
	// its TTL window — a replay attack or duplicate request. Maps to HTTP 401.
	ErrAuthNonceReused Code = "ERR_AUTH_NONCE_REUSED"

	// Distlock context-cause sentinels (runtime/distlock).
	//
	// ErrDistlockLockLost signals that the manager failed to renew the lock or
	// the backend reports ownership has been taken by another holder. Set as the
	// context cancellation cause on the lock-derived context. Maps to HTTP 409.
	ErrDistlockLockLost Code = "ERR_DISTLOCK_LOCK_LOST"
	// ErrDistlockLockReleased signals that release() was called (normal
	// end-of-critical-section). Set as the context cancellation cause on the
	// lock-derived context. Maps to HTTP 409.
	ErrDistlockLockReleased Code = "ERR_DISTLOCK_LOCK_RELEASED"

	// MetricsSchema error codes (tools/metricschema).
	//
	// ErrMetricsSchemaUnresolved signals that a concrete metric registration
	// has an unresolved identity field (name, label, namespace, bucket).
	// Maps to HTTP 500.
	ErrMetricsSchemaUnresolved Code = "ERR_METRICS_SCHEMA_UNRESOLVED"
)

// Option customizes an Error at construction time.
type Option func(*Error)

// WithCategory sets the origin category used by classifiers. Constructors
// default to CategoryUnspecified, which IsInfraError treats as infra.
func WithCategory(category Category) Option {
	return func(e *Error) {
		e.Category = category
	}
}

// WithInternal sets diagnostic detail that must never be exposed to clients.
func WithInternal(message string) Option {
	return func(e *Error) {
		e.InternalMessage = message
	}
}

// WithDetails attaches structured, client-visible details as typed slog.Attr
// values. The framework's HTTP response writer renders 4xx errors with the
// attribute list as a JSON array of {"key","value"} objects, and strips the
// list from 5xx errors so server-side runtime context never leaks to clients.
//
// Use slog.String / slog.Int / slog.Bool / slog.Group / slog.Any to construct
// attributes — the typed signature replaces the legacy map[string]any form so
// the compiler enforces structured logging conventions across all callers.
//
// Multiple WithDetails calls accumulate; attributes are appended in call order.
//
// Example:
//
//	errcode.New(KindNotFound, ErrCellNotFound, "cell not found",
//	    errcode.WithDetails(slog.String("cellId", id)))
func WithDetails(attrs ...slog.Attr) Option {
	return func(e *Error) {
		if len(attrs) == 0 {
			return
		}
		e.Details = append(e.Details, attrs...)
	}
}

// Error is a structured error that carries a machine-readable Code, a
// Kind-derived HTTP status, a human-readable Message, optional Details, and an
// optional wrapped Cause.
//
// InternalMessage holds diagnostic detail that must never be exposed to
// API consumers. When present, Error() uses it (for logs/traces); HTTP
// response writers use Message (safe for clients).
//
// Category classifies the error origin for infra/domain triage. The zero value
// CategoryUnspecified is treated as infra (fail-closed).
type Error struct {
	Kind            Kind
	Code            Code
	Message         string
	InternalMessage string
	Details         []slog.Attr
	Cause           error
	Category        Category
}

// FindAttr returns the first detail attribute whose Key matches key, or the
// zero slog.Attr and false when no such attribute exists. It is intended for
// callers that need to read back a single typed detail (for example,
// ctxcancel.ReasonFromDetails) without exposing the raw attr slice across
// package boundaries.
func (e *Error) FindAttr(key string) (slog.Attr, bool) {
	if e == nil {
		return slog.Attr{}, false
	}
	for _, attr := range e.Details {
		if attr.Key == key {
			return attr, true
		}
	}
	return slog.Attr{}, false
}

// MarshalJSON renders e in the wire form expected by
// contracts/shared/errors/error-response-v1.schema.json:
//
//	{"code":"ERR_X","message":"...","details":[{"key":"k","value":v}, ...]}
//
// Server-side errors (Kind.IsClient() == false, i.e. HTTP 5xx) emit an empty
// details array regardless of attached attributes — this is the single
// source-of-truth strip rule for runtime context that must never reach a
// client. InternalMessage and Cause are never marshaled because they may
// contain sensitive runtime data.
func (e *Error) MarshalJSON() ([]byte, error) {
	type kv struct {
		Key   string `json:"key"`
		Value any    `json:"value"`
	}
	wire := struct {
		Code    Code   `json:"code"`
		Message string `json:"message"`
		Details []kv   `json:"details"`
	}{
		Code:    e.Code,
		Message: e.Message,
		Details: []kv{},
	}
	if e.Kind.IsClient() {
		wire.Details = make([]kv, 0, len(e.Details))
		for _, attr := range e.Details {
			wire.Details = append(wire.Details, kv{
				Key:   attr.Key,
				Value: attr.Value.Any(),
			})
		}
	}
	return json.Marshal(wire)
}

// Error returns a formatted string representation for logging/diagnostics.
// When InternalMessage is set it is preferred over Message, because Error()
// is consumed by logs and traces — not by API clients.
// Format: "[CODE] msg" or "[CODE] msg: cause" when a Cause is present.
func (e *Error) Error() string {
	msg := e.Message
	if e.InternalMessage != "" {
		msg = e.InternalMessage
	}
	if e.Cause != nil {
		return fmt.Sprintf("[%s] %s: %s", e.Code, msg, e.Cause.Error())
	}
	return fmt.Sprintf("[%s] %s", e.Code, msg)
}

// Unwrap returns the underlying Cause, enabling errors.Is / errors.As chains.
func (e *Error) Unwrap() error {
	return e.Cause
}

// Status returns the HTTP status derived from e.Kind.
func (e *Error) Status() int {
	if e == nil {
		return KindInternal.Status()
	}
	return e.Kind.Status()
}

// PublicCode returns the wire-safe code for e.
func (e *Error) PublicCode() Code {
	if e == nil {
		return ErrInternal
	}
	if e.Kind.IsClient() {
		return e.Code
	}
	return e.Kind.PublicCode()
}

// Assertion constructs an *Error tagged as a programmer-error / impossible
// path. It is the canonical replacement for `panic(fmt.Sprintf("...", err))`
// patterns across production code: callers panic with the returned *Error so
// the kernel recovery middleware can surface a 500 with category=infra and a
// stable ErrInternal code, while the formatted text is preserved for logs and
// traces via Error.Error().
//
// Behaviour:
//   - Kind = KindInternal (HTTP 500)
//   - Code = ErrInternal (single sentinel for unrecoverable assertions)
//   - Category = CategoryInfra (treated as infrastructure for IsInfraError)
//   - Message = fmt.Sprintf(format, args...) — the formatted assertion text
//
// The formatted Message intentionally carries runtime data because Assertion
// indicates an impossible state that has already been reached: there is no
// safe way to render the failure without it. The kernel recovery layer maps
// this to ErrInternal before the response leaves the process, so end users
// receive only the public ErrInternal code.
func Assertion(format string, args ...any) *Error {
	return New(KindInternal, ErrInternal, fmt.Sprintf(format, args...), WithCategory(CategoryInfra))
}

// New creates an *Error with an explicit transport kind.
//
// The message argument must be a compile-time const literal — runtime
// information belongs in WithDetails (typed, client-visible) or
// WithInternal (server-side only). archtest MESSAGE-CONST-LITERAL-01
// statically enforces this rule outside the errcode package.
func New(kind Kind, code Code, message string, opts ...Option) *Error {
	e := &Error{
		Kind:    kind,
		Code:    code,
		Message: message,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}
	return e
}

// Wrap creates an *Error with an explicit transport kind and wrapped cause.
//
// The same const-literal restriction documented on New applies to message.
func Wrap(kind Kind, code Code, message string, cause error, opts ...Option) *Error {
	e := New(kind, code, message, opts...)
	e.Cause = cause
	return e
}
