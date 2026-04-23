package vault

// auth.go — pluggable Vault authentication methods for GoCell.
//
// Design: AuthMethod is a first-class interface. Each implementation holds its
// own dependencies and calls Login to obtain a short-lived ClientToken. The
// provider stores the AuthMethod so the renewal worker can re-authenticate
// after a terminal LifetimeWatcher failure.
//
// Three production auth methods are supported:
//   - MethodToken      — static VAULT_TOKEN (dev/CI only; rejected in real mode)
//   - MethodAppRole    — VAULT_ROLE_ID + secret-ID (direct / wrapped / file)
//   - MethodKubernetes — projected service account JWT
//
// Factory selection is driven by VAULT_AUTH_METHOD (required, no default).
//
// ref: hashicorp/vault api/auth.go#AuthMethod — interface shape reference
// ref: hashicorp/vault api/auth/approle/approle.go — AppRole login API
// ref: hashicorp/vault api/auth/kubernetes/kubernetes.go — K8s login API
// ref: hashicorp/vault api/logical.go#Logical.Write — login endpoint calls

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	vaultapi "github.com/hashicorp/vault/api"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// ---------------------------------------------------------------------------
// Method constants
// ---------------------------------------------------------------------------

// Method identifies a Vault auth method, used in AssertForRealMode and metrics labels.
type Method string

const (
	// MethodToken is a static Vault token (VAULT_TOKEN). Only allowed in dev/CI mode.
	MethodToken Method = "token"
	// MethodAppRole uses Vault AppRole auth (role_id + secret_id).
	MethodAppRole Method = "approle"
	// MethodKubernetes uses Vault Kubernetes auth (projected service account JWT).
	MethodKubernetes Method = "kubernetes"
)

// ---------------------------------------------------------------------------
// AuthResult
// ---------------------------------------------------------------------------

// AuthResult is the unified output of AuthMethod.Login. It decouples callers
// from the Vault SDK's *vaultapi.Secret so fakes and tests need not import it.
//
// ref: hashicorp/vault api/auth/approle/approle.go — Login return type
type AuthResult struct {
	// ClientToken is the short-lived Vault client token to use for subsequent API calls.
	ClientToken string
	// LeaseSeconds is the token TTL in seconds. 0 means no explicit TTL (e.g. root token).
	LeaseSeconds int
	// Renewable indicates whether the token can be renewed via LifetimeWatcher.
	Renewable bool
}

// ---------------------------------------------------------------------------
// AuthMethod interface
// ---------------------------------------------------------------------------

// AuthMethod is the GoCell abstraction for a Vault authentication strategy.
// Implementations hold their own credentials and call the appropriate Vault
// auth endpoint on Login. The TransitKeyProvider stores the AuthMethod so the
// renewal worker can re-authenticate after a terminal LifetimeWatcher failure.
//
// Implementations must be safe for concurrent calls to Login (the renewal
// worker may call Login from its goroutine while the main goroutine is idle).
//
// ref: hashicorp/vault api/auth.go#Auth — original SDK interface shape
type AuthMethod interface {
	// Method returns the method identifier for logging and metrics labels.
	Method() Method
	// Login authenticates with Vault and returns the resulting token.
	// On success it also sets the token on the underlying *vaultapi.Client
	// so subsequent VaultClient calls use the fresh token without extra wiring.
	Login(ctx context.Context) (AuthResult, error)
}

// ---------------------------------------------------------------------------
// SecretIDProvider
// ---------------------------------------------------------------------------

// SecretIDProvider supplies a secret_id on demand. Different sources have
// different refresh semantics:
//   - direct  — returns the same value every call (env var / static config)
//   - wrapped — returns the unwrapped value from the wrapping token; the
//     wrapping token is single-use, so subsequent calls return the cached value
//     (a new wrapping token requires a restart)
//   - file    — re-reads the file on every call to pick up orchestrator rotations
//
// ref: external-secrets/external-secrets — resolves credential references at Login time
// ref: HashiCorp Vault Agent — re-reads AppRole file on each auto-auth cycle
type SecretIDProvider func(ctx context.Context) (string, error)

// ---------------------------------------------------------------------------
// staticTokenAuth
// ---------------------------------------------------------------------------

// staticTokenAuth implements AuthMethod for VAULT_TOKEN (dev/CI only).
// Login sets the token on the client (if non-nil) and returns a non-renewable
// AuthResult with LeaseSeconds=0 — the provider will not start a renewal worker.
//
// ref: hashicorp/vault api/client.go#SetToken
type staticTokenAuth struct {
	client *vaultapi.Client // may be nil in tests
	token  string
}

// NewStaticTokenAuth creates an AuthMethod that presents a pre-configured
// static token. Pass nil client in unit tests (no real Vault required).
func NewStaticTokenAuth(client *vaultapi.Client, token string) AuthMethod {
	return &staticTokenAuth{client: client, token: token}
}

func (a *staticTokenAuth) Method() Method { return MethodToken }

// Login sets the token on the client and returns a non-renewable AuthResult.
// Static tokens have no explicit lease and are not renewable via LifetimeWatcher.
func (a *staticTokenAuth) Login(_ context.Context) (AuthResult, error) {
	if a.token == "" {
		return AuthResult{}, errcode.New(errcode.ErrVaultAuthFailed,
			"vault-auth: static token (VAULT_TOKEN) is empty")
	}
	if a.client != nil {
		a.client.SetToken(a.token)
	}
	return AuthResult{
		ClientToken:  a.token,
		LeaseSeconds: 0,
		Renewable:    false,
	}, nil
}

// ---------------------------------------------------------------------------
// appRoleAuth
// ---------------------------------------------------------------------------

// appRoleAuth implements AuthMethod for Vault AppRole auth.
// It calls auth/{mountPath}/login with role_id + secret_id.
// secretIDSource is called on each Login to pick up rotated secret_id files
// (file mode) or return the cached value (direct / wrapped mode).
//
// ref: hashicorp/vault api/auth/approle/approle.go — NewAppRoleAuth + Login
// ref: hashicorp/vault builtin/credential/approle/path_login.go — login endpoint
// ref: external-secrets/external-secrets — resolves credentials at Login time
type appRoleAuth struct {
	client         *vaultapi.Client
	roleID         string
	secretIDSource SecretIDProvider
	mountPath      string // default: "approle"
}

// NewAppRoleAuth creates an AuthMethod that authenticates via Vault AppRole.
// mountPath defaults to "approle" if empty.
// secretIDSource is called on each Login to obtain the current secret_id.
//
// NOTE: client must be non-nil; unlike NewStaticTokenAuth, AppRole auth
// requires a real Vault client for the Login HTTP call. In unit tests, use
// vaultapi.NewClient(vaultapi.DefaultConfig()) with any address — Login is
// only called at auth time, not at construction time.
//
// ref: hashicorp/vault api/auth/approle/approle.go#NewAppRoleAuth
func NewAppRoleAuth(client *vaultapi.Client, roleID string, secretIDSource SecretIDProvider) (AuthMethod, error) {
	if client == nil {
		return nil, errcode.New(errcode.ErrVaultAuthFailed,
			"vault-auth: AppRole auth requires a non-nil Vault client")
	}
	if roleID == "" {
		return nil, errcode.New(errcode.ErrVaultAuthFailed,
			"vault-auth: AppRole auth requires VAULT_ROLE_ID")
	}
	if secretIDSource == nil {
		return nil, errcode.New(errcode.ErrVaultAuthFailed,
			"vault-auth: AppRole auth requires a non-nil SecretIDProvider")
	}
	return &appRoleAuth{
		client:         client,
		roleID:         roleID,
		secretIDSource: secretIDSource,
		mountPath:      "approle",
	}, nil
}

func (a *appRoleAuth) Method() Method { return MethodAppRole }

// Login resolves the current secret_id via secretIDSource (which may re-read
// a file for rotation support), then calls auth/approle/login with role_id and
// secret_id, sets the resulting token on the client, and returns an AuthResult.
//
// ref: hashicorp/vault api/auth/approle/approle.go#Login
// ref: external-secrets/external-secrets — resolves credential references at Login time
func (a *appRoleAuth) Login(ctx context.Context) (AuthResult, error) {
	secretID, err := a.secretIDSource(ctx)
	if err != nil {
		return AuthResult{}, err
	}

	path := "auth/" + a.mountPath + "/login"
	secret, err := a.client.Logical().WriteWithContext(ctx, path, map[string]any{
		"role_id":   a.roleID,
		"secret_id": secretID,
	})
	if err != nil {
		return AuthResult{}, errcode.Wrap(errcode.ErrVaultAuthFailed,
			"vault-auth: AppRole login failed", err)
	}
	return extractAuthResult(a.client, secret)
}

// ---------------------------------------------------------------------------
// kubernetesAuth
// ---------------------------------------------------------------------------

// kubernetesAuth implements AuthMethod for Vault Kubernetes auth.
// It reads a projected service account JWT from disk and calls auth/{mount}/login.
//
// ref: hashicorp/vault api/auth/kubernetes/kubernetes.go — NewKubernetesAuth + Login
// ref: hashicorp/vault builtin/credential/kubernetes/path_login.go — login endpoint
type kubernetesAuth struct {
	client    *vaultapi.Client
	role      string
	jwtPath   string // default: /var/run/secrets/kubernetes.io/serviceaccount/token
	mountPath string // default: "kubernetes"
}

// NewKubernetesAuth creates an AuthMethod for Vault Kubernetes auth.
// jwtPath defaults to the standard K8s projected volume path if empty.
// mountPath defaults to "kubernetes" if empty.
//
// NOTE: client must be non-nil; unlike NewStaticTokenAuth, Kubernetes auth
// requires a real Vault client for the Login HTTP call. In unit tests, use
// vaultapi.NewClient(vaultapi.DefaultConfig()) with any address — Login is
// only called at auth time, not at construction time.
//
// ref: hashicorp/vault api/auth/kubernetes/kubernetes.go#NewKubernetesAuth
func NewKubernetesAuth(client *vaultapi.Client, role, jwtPath, mountPath string) (AuthMethod, error) {
	if client == nil {
		return nil, errcode.New(errcode.ErrVaultAuthFailed,
			"vault-auth: Kubernetes auth requires a non-nil Vault client")
	}
	if role == "" {
		return nil, errcode.New(errcode.ErrVaultAuthFailed,
			"vault-auth: Kubernetes auth requires VAULT_K8S_ROLE")
	}
	if jwtPath == "" {
		jwtPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	}
	if mountPath == "" {
		mountPath = "kubernetes"
	}
	return &kubernetesAuth{
		client:    client,
		role:      role,
		jwtPath:   jwtPath,
		mountPath: mountPath,
	}, nil
}

func (a *kubernetesAuth) Method() Method { return MethodKubernetes }

// Login reads the projected JWT from disk and calls auth/kubernetes/login.
//
// ref: hashicorp/vault api/auth/kubernetes/kubernetes.go#Login
func (a *kubernetesAuth) Login(ctx context.Context) (AuthResult, error) {
	jwtBytes, err := os.ReadFile(a.jwtPath)
	if err != nil {
		return AuthResult{}, errcode.Wrap(errcode.ErrVaultAuthFailed,
			fmt.Sprintf("vault-auth: Kubernetes auth: read JWT from %s", a.jwtPath), err)
	}
	if len(jwtBytes) == 0 {
		return AuthResult{}, errcode.New(errcode.ErrVaultAuthFailed,
			fmt.Sprintf("vault-auth: Kubernetes auth: JWT file is empty: %s", a.jwtPath))
	}

	path := "auth/" + a.mountPath + "/login"
	secret, err := a.client.Logical().WriteWithContext(ctx, path, map[string]any{
		"role": a.role,
		"jwt":  string(jwtBytes),
	})
	if err != nil {
		return AuthResult{}, errcode.Wrap(errcode.ErrVaultAuthFailed,
			"vault-auth: Kubernetes login failed", err)
	}
	return extractAuthResult(a.client, secret)
}

// ---------------------------------------------------------------------------
// extractAuthResult — shared helper
// ---------------------------------------------------------------------------

// extractAuthResult converts a Vault *Secret with auth data into an AuthResult
// and sets the token on the client. Used by AppRole and Kubernetes Login.
func extractAuthResult(client *vaultapi.Client, secret *vaultapi.Secret) (AuthResult, error) {
	if secret == nil || secret.Auth == nil {
		return AuthResult{}, errcode.New(errcode.ErrVaultAuthFailed,
			"vault-auth: login response missing auth data")
	}
	token := secret.Auth.ClientToken
	if token == "" {
		return AuthResult{}, errcode.New(errcode.ErrVaultAuthFailed,
			"vault-auth: login response has empty client_token")
	}
	client.SetToken(token)
	return AuthResult{
		ClientToken:  token,
		LeaseSeconds: secret.Auth.LeaseDuration,
		Renewable:    secret.Auth.Renewable,
	}, nil
}

// ---------------------------------------------------------------------------
// NewAuthMethodFromEnv — environment-driven factory
// ---------------------------------------------------------------------------

// NewAuthMethodFromEnv constructs an AuthMethod based on the VAULT_AUTH_METHOD
// environment variable. The client parameter is used by AppRole and Kubernetes
// auth to issue the login call and set the resulting token.
//
// ctx is used to bound the wrapping token unwrap call when
// VAULT_SECRET_ID_TYPE=wrapped (F-3a: unwrap must respect the startup deadline).
//
// Required:
//   - VAULT_AUTH_METHOD = token | approle | kubernetes  (no default — fail-fast)
//
// Per-method required env:
//   - token:      VAULT_TOKEN
//   - approle:    VAULT_ROLE_ID + secret from VAULT_SECRET_ID_TYPE dispatch
//   - kubernetes: VAULT_K8S_ROLE (optional: VAULT_K8S_JWT_PATH, VAULT_K8S_MOUNT)
//
// AppRole secret ID types (VAULT_SECRET_ID_TYPE, default: direct):
//   - direct:  read VAULT_SECRET_ID
//   - wrapped: read VAULT_SECRET_ID_WRAPPING_TOKEN, unwrap via Vault sys/unwrap
//   - file:    read VAULT_SECRET_ID_FILE path, re-read on every Login call
//
// ref: hashicorp/vault api/auth/approle/approle.go — SecretID variants
// ref: hashicorp/vault api/logical.go#Logical.UnwrapWithContext — wrapping token unwrap
func NewAuthMethodFromEnv(ctx context.Context, client *vaultapi.Client) (AuthMethod, error) {
	method := os.Getenv("VAULT_AUTH_METHOD")
	switch method {
	case string(MethodToken):
		token := os.Getenv("VAULT_TOKEN")
		if token == "" {
			return nil, errcode.New(errcode.ErrVaultAuthFailed,
				"vault-auth: VAULT_AUTH_METHOD=token requires VAULT_TOKEN to be set")
		}
		return NewStaticTokenAuth(client, token), nil

	case string(MethodAppRole):
		roleID := os.Getenv("VAULT_ROLE_ID")
		if roleID == "" {
			return nil, errcode.New(errcode.ErrVaultAuthFailed,
				"vault-auth: VAULT_AUTH_METHOD=approle requires VAULT_ROLE_ID to be set")
		}
		secretIDSource, err := secretIDFromEnv(ctx, client)
		if err != nil {
			return nil, err
		}
		return NewAppRoleAuth(client, roleID, secretIDSource)

	case string(MethodKubernetes):
		role := os.Getenv("VAULT_K8S_ROLE")
		jwtPath := os.Getenv("VAULT_K8S_JWT_PATH")
		mountPath := os.Getenv("VAULT_K8S_MOUNT")
		return NewKubernetesAuth(client, role, jwtPath, mountPath)

	case "":
		return nil, errcode.New(errcode.ErrVaultAuthFailed,
			"vault-auth: VAULT_AUTH_METHOD is required (known values: token, approle, kubernetes)")

	default:
		return nil, errcode.New(errcode.ErrVaultAuthFailed,
			fmt.Sprintf("vault-auth: unknown VAULT_AUTH_METHOD %q (known values: token, approle, kubernetes)", method))
	}
}

// ---------------------------------------------------------------------------
// secretIDFromEnv — AppRole secret ID loading (direct / wrapped / file)
// ---------------------------------------------------------------------------

// secretIDFromEnv returns a SecretIDProvider based on VAULT_SECRET_ID_TYPE.
// Default is "direct" for backwards-compatibility in dev/CI.
//
// The caller supplies ctx for the wrapped case: unwrapping consumes the
// single-use wrapping token immediately so it respects the startup deadline (F-3a).
//
// ref: hashicorp/vault api/auth/approle/approle.go — SecretID.FromString / FromFile
// ref: hashicorp/vault api/logical.go#Logical.UnwrapWithContext — wrapping token
// ref: external-secrets/external-secrets — resolves credentials at Login time
// ref: HashiCorp Vault Agent — re-reads AppRole file on each auto-auth cycle
func secretIDFromEnv(ctx context.Context, client *vaultapi.Client) (SecretIDProvider, error) {
	secretIDType := os.Getenv("VAULT_SECRET_ID_TYPE")
	if secretIDType == "" {
		secretIDType = "direct"
	}
	switch secretIDType {
	case "direct":
		s := os.Getenv("VAULT_SECRET_ID")
		if s == "" {
			return nil, errcode.New(errcode.ErrVaultAuthFailed,
				"vault-auth: VAULT_SECRET_ID_TYPE=direct requires VAULT_SECRET_ID to be set")
		}
		return func(_ context.Context) (string, error) { return s, nil }, nil

	case "wrapped":
		// Unwrap NOW using the caller's ctx (e.g. the 30s startup deadline).
		// Wrapping tokens are single-use — cache the result for subsequent Login calls.
		// A new wrapping token requires a process restart.
		unwrapped, err := unwrapSecretID(ctx, client)
		if err != nil {
			return nil, err
		}
		return func(_ context.Context) (string, error) { return unwrapped, nil }, nil

	case "file":
		filePath := os.Getenv("VAULT_SECRET_ID_FILE")
		if filePath == "" {
			return nil, errcode.New(errcode.ErrVaultAuthFailed,
				"vault-auth: VAULT_SECRET_ID_TYPE=file requires VAULT_SECRET_ID_FILE to be set")
		}
		// Re-read on every Login call so orchestrator-rotated projected volumes
		// are picked up without a process restart (F-3c).
		return func(_ context.Context) (string, error) {
			data, err := os.ReadFile(filePath)
			if err != nil {
				return "", errcode.Wrap(errcode.ErrVaultAuthFailed,
					fmt.Sprintf("vault-auth: read secret_id from file %s", filePath), err)
			}
			s := strings.TrimSpace(string(data))
			if s == "" {
				return "", errcode.New(errcode.ErrVaultAuthFailed,
					"vault-auth: secret_id file is empty: "+filePath)
			}
			return s, nil
		}, nil

	default:
		return nil, errcode.New(errcode.ErrVaultAuthFailed,
			fmt.Sprintf("vault-auth: unknown VAULT_SECRET_ID_TYPE %q (known values: direct, wrapped, file)", secretIDType))
	}
}

// unwrapSecretID reads VAULT_SECRET_ID_WRAPPING_TOKEN and unwraps it via
// Vault sys/wrapping/unwrap to obtain the real secret_id.
//
// ctx is used to bound the unwrap call — the caller passes the startup
// deadline so an unreachable Vault does not block indefinitely (F-3a).
//
// The wrapping token is consumed on unwrap — it must not be used again.
//
// Thread-safety: Clone() creates an isolated copy of the client so that
// setting the wrapping token on the clone does not affect the shared client
// used by other goroutines. This eliminates the race window that existed when
// the original client's token was temporarily replaced.
//
// ref: hashicorp/vault api/logical.go#Logical.UnwrapWithContext
// ref: hashicorp/vault builtin/logical/transit/wrapping.go — wrapping token pattern
// ref: hashicorp/vault api/client.go#Clone — thread-safe copy for isolated operations
func unwrapSecretID(ctx context.Context, client *vaultapi.Client) (string, error) {
	wrapToken := os.Getenv("VAULT_SECRET_ID_WRAPPING_TOKEN")
	if wrapToken == "" {
		return "", errcode.New(errcode.ErrVaultAuthFailed,
			"vault-auth: VAULT_SECRET_ID_TYPE=wrapped requires VAULT_SECRET_ID_WRAPPING_TOKEN to be set")
	}

	// Clone the client so the wrapping token is set only on the isolated copy.
	// This prevents a race window where other goroutines sharing the original
	// client would briefly see the wrapping token instead of the app token.
	clone, err := client.Clone()
	if err != nil {
		return "", errcode.Wrap(errcode.ErrVaultAuthFailed,
			"vault-auth: clone client for unwrap", err)
	}
	// Vault SDK's Logical().UnwrapWithContext reads the token from the client's
	// header (X-Vault-Token), NOT from the wrapToken body argument. SetToken is
	// required here; passing wrapToken to UnwrapWithContext is ignored when a
	// non-empty header is already set by the SDK.
	// ref: hashicorp/vault api/sys_wrapping.go#Unwrap
	clone.SetToken(wrapToken)

	secret, err := clone.Logical().UnwrapWithContext(ctx, wrapToken)
	if err != nil {
		return "", errcode.Wrap(errcode.ErrVaultAuthFailed,
			"vault-auth: unwrap AppRole secret_id (wrapping token may be expired or already consumed)", err)
	}
	if secret == nil || secret.Data == nil {
		return "", errcode.New(errcode.ErrVaultAuthFailed,
			"vault-auth: unwrap returned nil or empty data")
	}
	secretID, ok := secret.Data["secret_id"].(string)
	if !ok || secretID == "" {
		return "", errcode.New(errcode.ErrVaultAuthFailed,
			"vault-auth: unwrapped data missing string 'secret_id' field")
	}
	return secretID, nil
}

// secretIDFromFile reads the AppRole secret_id once from a file at
// VAULT_SECRET_ID_FILE. This is the legacy single-read helper kept for
// compatibility with tests that call it directly.
// New callers should use secretIDFromEnv (file case) which returns a provider
// that re-reads the file on each Login call.
func secretIDFromFile() (string, error) {
	filePath := os.Getenv("VAULT_SECRET_ID_FILE")
	if filePath == "" {
		return "", errcode.New(errcode.ErrVaultAuthFailed,
			"vault-auth: VAULT_SECRET_ID_TYPE=file requires VAULT_SECRET_ID_FILE to be set")
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", errcode.Wrap(errcode.ErrVaultAuthFailed,
			fmt.Sprintf("vault-auth: read secret_id from file %s", filePath), err)
	}
	secretID := strings.TrimSpace(string(data))
	if secretID == "" {
		return "", errcode.New(errcode.ErrVaultAuthFailed,
			fmt.Sprintf("vault-auth: secret_id file is empty: %s", filePath))
	}
	return secretID, nil
}

// ---------------------------------------------------------------------------
// AssertForRealMode — real-mode guard
// ---------------------------------------------------------------------------

// AssertForRealMode returns ErrVaultAuthFailed if auth uses MethodToken.
// Static tokens are rejected in production (GOCELL_ADAPTER_MODE=real) because
// they cannot be rotated automatically and carry permanent broad permissions.
// AppRole and Kubernetes tokens are accepted — they are short-lived and
// scoped to the application's role.
//
// Call this during construction when realMode is true:
//
//	if realMode {
//	    if err := AssertForRealMode(auth); err != nil {
//	        return nil, err
//	    }
//	}
//
// ref: hashicorp/vault security best practices — avoid long-lived static tokens in prod
func AssertForRealMode(auth AuthMethod) error {
	if auth == nil {
		return errcode.New(errcode.ErrVaultAuthFailed,
			"vault-auth: AssertForRealMode called with nil AuthMethod")
	}
	if auth.Method() == MethodToken {
		return errcode.New(errcode.ErrVaultAuthFailed,
			"vault-transit: static VAULT_TOKEN is not allowed in real mode; "+
				"use VAULT_AUTH_METHOD=approle or VAULT_AUTH_METHOD=kubernetes")
	}
	return nil
}

// IsErrVaultAuthFailed reports whether err (or any error in its chain) carries
// the ErrVaultAuthFailed code.
func IsErrVaultAuthFailed(err error) bool {
	if err == nil {
		return false
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		return false
	}
	return ec.Code == errcode.ErrVaultAuthFailed
}
