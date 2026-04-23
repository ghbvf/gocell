package vault

// auth_classify.go — error classification for Vault auth Login failures.
//
// classifyAuthLoginError maps a Login error to a reason label used in the
// gocell_vault_auth_login_total{result="failure",reason=...} counter.
// Style mirrors classifyVaultEncryptError in transit_provider.go.
//
// ref: hashicorp/vault api/logical.go#ResponseError — Vault HTTP error type
// ref: transit_provider.go#classifyVaultError — existing classification pattern

import (
	"context"
	"errors"
	"net"
	"strings"

	vaultapi "github.com/hashicorp/vault/api"
)

// Reason labels returned by classifyAuthLoginError.
const (
	reasonTimeout      = "timeout"
	reasonNetwork      = "network"
	reasonAuthInvalid  = "auth_invalid"
	reasonUnwrapFailed = "unwrap_failed"
	reasonServerError  = "server_error"
	reasonOther        = "other"
)

// classifyAuthLoginError classifies a Vault auth Login error into a reason
// string suitable for use as a metric label. Classification is best-effort:
//
//   - timeout        — context deadline exceeded or net.Error.Timeout()
//   - network        — net.OpError, connection refused/reset (no HTTP response)
//   - auth_invalid   — Vault 400 or 403 (bad credentials, role not found, …)
//   - unwrap_failed  — error message mentions wrapping token / unwrap
//   - server_error   — Vault 5xx
//   - other          — anything else
//
// ref: transit_provider.go#classifyVaultEncryptError — matching style
// ref: hashicorp/vault api/logical.go#ResponseError — *vaultapi.ResponseError
func classifyAuthLoginError(err error) string {
	if err == nil {
		return ""
	}

	// 1. Context deadline / timeout.
	if errors.Is(err, context.DeadlineExceeded) {
		return reasonTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return reasonTimeout
	}

	// 2. Network error (OpError covers connection refused, reset, etc.).
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return reasonNetwork
	}

	// 3. Vault HTTP response error.
	var respErr *vaultapi.ResponseError
	if errors.As(err, &respErr) {
		switch {
		case respErr.StatusCode == 400 || respErr.StatusCode == 403:
			return reasonAuthInvalid
		case respErr.StatusCode >= 500:
			return reasonServerError
		}
	}

	// 4. Wrapping token errors — check message heuristic after SDK classification.
	msg := err.Error()
	if strings.Contains(msg, "wrapping token") || strings.Contains(msg, "unwrap") {
		return reasonUnwrapFailed
	}

	return reasonOther
}
