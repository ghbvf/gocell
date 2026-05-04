package vault

// auth_classify_test.go — table-driven tests for classifyAuthLoginError.
//
// Verifies that each error category maps to the expected reason label.
// All test inputs are constructed in-process (no real Vault needed).

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	vaultapi "github.com/hashicorp/vault/api"

	"github.com/ghbvf/gocell/pkg/errcode"
)

func TestClassifyAuthLoginError_Table(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "nil returns empty",
			err:  nil,
			want: "",
		},
		{
			name: "context.DeadlineExceeded → timeout",
			err:  context.DeadlineExceeded,
			want: reasonTimeout,
		},
		{
			name: "wrapped DeadlineExceeded → timeout",
			err:  fmt.Errorf("wrap: %w", context.DeadlineExceeded),
			want: reasonTimeout,
		},
		{
			name: "net.OpError → network",
			err: &net.OpError{
				Op:  "dial",
				Net: "tcp",
				Err: errors.New("connection refused"),
			},
			want: reasonNetwork,
		},
		{
			name: "vault 400 → auth_invalid",
			err: &vaultapi.ResponseError{
				StatusCode: 400,
				Errors:     []string{"missing role_id"},
			},
			want: reasonAuthInvalid,
		},
		{
			name: "vault 403 → auth_invalid",
			err: &vaultapi.ResponseError{
				StatusCode: 403,
				Errors:     []string{"permission denied"},
			},
			want: reasonAuthInvalid,
		},
		{
			name: "vault 500 → server_error",
			err: &vaultapi.ResponseError{
				StatusCode: 500,
				Errors:     []string{"internal server error"},
			},
			want: reasonServerError,
		},
		{
			name: "vault 503 → server_error",
			err: &vaultapi.ResponseError{
				StatusCode: 503,
				Errors:     []string{"service unavailable"},
			},
			want: reasonServerError,
		},
		{
			name: "message contains 'wrapping token' → unwrap_failed",
			err:  errors.New("wrapping token is invalid or expired"),
			want: reasonUnwrapFailed,
		},
		{
			name: "message contains 'unwrap' → unwrap_failed",
			err:  errors.New("failed to unwrap secret_id"),
			want: reasonUnwrapFailed,
		},
		{
			name: "generic error → other",
			err:  errors.New("something unexpected happened"),
			want: reasonOther,
		},
		{
			name: "vault 404 (mount/path misrouted) → auth_invalid",
			err: &vaultapi.ResponseError{
				StatusCode: 404,
				Errors:     []string{"no handler for route"},
			},
			want: reasonAuthInvalid,
		},
		{
			name: "vault 400/403/404 via errcode.Wrap → auth_invalid (errors.As unwraps)",
			err: errcode.Wrap(errcode.KindUnavailable, errcode.ErrVaultAuthFailed, "login",
				&vaultapi.ResponseError{StatusCode: 400, Errors: []string{"invalid role_id"}}),
			want: reasonAuthInvalid,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyAuthLoginError(tc.err)
			if got != tc.want {
				t.Errorf("classifyAuthLoginError(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// TestClassifyAuthLoginError_WrappedNetOpError verifies that a net.OpError
// wrapped inside a higher-level error is still classified as "network".
func TestClassifyAuthLoginError_WrappedNetOpError(t *testing.T) {
	inner := &net.OpError{
		Op:  "read",
		Net: "tcp",
		Err: errors.New("connection reset"),
	}
	wrapped := fmt.Errorf("vault client: %w", inner)
	got := classifyAuthLoginError(wrapped)
	if got != reasonNetwork {
		t.Errorf("classifyAuthLoginError(wrapped net.OpError) = %q, want %q", got, reasonNetwork)
	}
}

// TestClassifyAuthLoginError_WrappedResponseError verifies that a
// *vaultapi.ResponseError wrapped inside fmt.Errorf is still classified.
func TestClassifyAuthLoginError_WrappedResponseError(t *testing.T) {
	inner := &vaultapi.ResponseError{StatusCode: 403, Errors: []string{"forbidden"}}
	wrapped := fmt.Errorf("approle login: %w", inner)
	got := classifyAuthLoginError(wrapped)
	if got != reasonAuthInvalid {
		t.Errorf("classifyAuthLoginError(wrapped 403) = %q, want %q", got, reasonAuthInvalid)
	}
}
