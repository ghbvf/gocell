package postgres

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// pgTimeoutErr is a net.Error stub whose Timeout()=true; used to exercise the
// net.Error.Timeout() branch in isRetryablePGError and classifyPGConnectError
// (pgxpool surfaces *net.OpError for dial-level timeouts when ConnectTimeout
// fires).
type pgTimeoutErr struct{}

func (pgTimeoutErr) Error() string   { return "i/o timeout" }
func (pgTimeoutErr) Timeout() bool   { return true }
func (pgTimeoutErr) Temporary() bool { return true }

// compile-time assertion: pgTimeoutErr implements net.Error.
var _ net.Error = pgTimeoutErr{}

// safeToRetryErr mimics pgx's connect/exec wrapper: pgconn.SafeToRetry
// recognizes any error whose chain implements SafeToRetry() bool == true.
// It also unwraps to its cause so errors.Is(err, context.Canceled) holds.
type safeToRetryErr struct{ cause error }

func (e safeToRetryErr) Error() string     { return "pg: " + e.cause.Error() }
func (e safeToRetryErr) Unwrap() error     { return e.cause }
func (e safeToRetryErr) SafeToRetry() bool { return true }

// TestIsRetryablePGError covers all SQLSTATE branches for isRetryablePGError.
func TestIsRetryablePGError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "serialization_failure 40001",
			err:  &pgconn.PgError{Code: "40001"},
			want: true,
		},
		{
			name: "deadlock_detected 40P01",
			err:  &pgconn.PgError{Code: "40P01"},
			want: true,
		},
		{
			name: "connection_failure 08006",
			err:  &pgconn.PgError{Code: "08006"},
			want: true,
		},
		{
			name: "connection_does_not_exist 08003",
			err:  &pgconn.PgError{Code: "08003"},
			want: true,
		},
		{
			name: "connection_exception 08000",
			err:  &pgconn.PgError{Code: "08000"},
			want: true,
		},
		{
			name: "protocol_violation 08P01",
			err:  &pgconn.PgError{Code: "08P01"},
			want: true,
		},
		{
			name: "unique_violation 23505 — permanent",
			err:  &pgconn.PgError{Code: "23505"},
			want: false,
		},
		{
			name: "invalid_password 28P01 — permanent",
			err:  &pgconn.PgError{Code: "28P01"},
			want: false,
		},
		{
			name: "invalid_catalog_name 3D000 — permanent",
			err:  &pgconn.PgError{Code: "3D000"},
			want: false,
		},
		{
			name: "context.DeadlineExceeded",
			err:  context.DeadlineExceeded,
			want: true,
		},
		{
			name: "net.Error Timeout()=true",
			err:  pgTimeoutErr{},
			want: true,
		},
		{
			name: "context.Canceled — NOT transient",
			err:  context.Canceled,
			want: false,
		},
		{
			// pgx wraps a canceled-context acquire/exec in an error whose
			// SafeToRetry()==true (failure before any bytes sent). The
			// Canceled check must win — caller gave up, not transient.
			name: "SafeToRetry error wrapping context.Canceled — NOT transient",
			err:  safeToRetryErr{cause: context.Canceled},
			want: false,
		},
		{
			name: "plain error",
			err:  errors.New("unknown"),
			want: false,
		},
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRetryablePGError(tt.err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestClassifyPGError verifies the non-connect classification funnel: every
// branch (transient via WrapInfra, permanent via Wrap) keeps the caller's
// permanentCode and never substitutes ErrAdapterPGConnectTimeout — the
// ConnectTimeout substitution lives in [classifyPGConnectError] (see
// TestClassifyPGConnectError).
//
// opCode is parameterized per case so timeout-class causes can be exercised
// under a non-connect code (ErrAdapterPGQuery), proving that picking
// classifyPGError on a savepoint/query caller keeps the query-domain code
// instead of relabeling it ConnectTimeout.
func TestClassifyPGError(t *testing.T) {
	const opMsg = "op"

	tests := []struct {
		name          string
		err           error
		opCode        errcode.Code
		wantTransient bool
	}{
		{
			name:          "serialization_failure 40001 → transient",
			err:           &pgconn.PgError{Code: "40001"},
			opCode:        ErrAdapterPGConnect,
			wantTransient: true,
		},
		{
			name:          "deadlock_detected 40P01 → transient",
			err:           &pgconn.PgError{Code: "40P01"},
			opCode:        ErrAdapterPGConnect,
			wantTransient: true,
		},
		{
			name:          "connection_failure 08006 → transient",
			err:           &pgconn.PgError{Code: "08006"},
			opCode:        ErrAdapterPGConnect,
			wantTransient: true,
		},
		{
			name:          "connection_does_not_exist 08003 → transient",
			err:           &pgconn.PgError{Code: "08003"},
			opCode:        ErrAdapterPGConnect,
			wantTransient: true,
		},
		{
			name:          "unique_violation 23505 → permanent",
			err:           &pgconn.PgError{Code: "23505"},
			opCode:        ErrAdapterPGConnect,
			wantTransient: false,
		},
		{
			name:          "invalid_password 28P01 → permanent",
			err:           &pgconn.PgError{Code: "28P01"},
			opCode:        ErrAdapterPGConnect,
			wantTransient: false,
		},
		{
			name:          "invalid_catalog_name 3D000 → permanent",
			err:           &pgconn.PgError{Code: "3D000"},
			opCode:        ErrAdapterPGConnect,
			wantTransient: false,
		},
		{
			// Query-scope timeout MUST stay under the caller's code; picking
			// classifyPGError on a savepoint/query caller must not relabel
			// timeouts as ConnectTimeout.
			name:          "context.DeadlineExceeded under query scope → transient, query code preserved",
			err:           context.DeadlineExceeded,
			opCode:        ErrAdapterPGQuery,
			wantTransient: true,
		},
		{
			// Same guarantee for net.Error.Timeout()=true.
			name:          "net.Error Timeout()=true under query scope → transient, query code preserved",
			err:           pgTimeoutErr{},
			opCode:        ErrAdapterPGQuery,
			wantTransient: true,
		},
		{
			name:          "context.Canceled → permanent",
			err:           context.Canceled,
			opCode:        ErrAdapterPGConnect,
			wantTransient: false,
		},
		{
			name:          "plain error → permanent",
			err:           errors.New("unknown error"),
			opCode:        ErrAdapterPGConnect,
			wantTransient: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyPGError(tt.err, tt.opCode, opMsg)
			assert.Equal(t, tt.wantTransient, errcode.IsTransient(got),
				"IsTransient mismatch for %q", tt.name)
			var ec *errcode.Error
			require := assert.New(t)
			require.ErrorAs(got, &ec, "classify result must be *errcode.Error")
			assert.Equal(t, tt.opCode, ec.Code,
				"classifyPGError must preserve caller's opCode for %q (no ConnectTimeout substitution outside the connect funnel)", tt.name)
		})
	}
}

// TestClassifyPGConnectError verifies the connect-class classification funnel:
// timeout-class causes get the dedicated ErrAdapterPGConnectTimeout code while
// every other disposition delegates to classifyPGError under ErrAdapterPGConnect.
// The split is the upstream "typed function choice" Hard guarantee — connect
// callers select this funnel by name to opt into the ConnectTimeout substitution.
func TestClassifyPGConnectError(t *testing.T) {
	const opMsg = "op"

	tests := []struct {
		name          string
		err           error
		wantTransient bool
		wantCode      errcode.Code
	}{
		{
			name:          "context.DeadlineExceeded → transient ConnectTimeout code",
			err:           context.DeadlineExceeded,
			wantTransient: true,
			wantCode:      ErrAdapterPGConnectTimeout,
		},
		{
			name:          "net.Error Timeout()=true → transient ConnectTimeout code",
			err:           pgTimeoutErr{},
			wantTransient: true,
			wantCode:      ErrAdapterPGConnectTimeout,
		},
		{
			name:          "connection_failure 08006 → transient (delegates to classifyPGError, Connect code)",
			err:           &pgconn.PgError{Code: "08006"},
			wantTransient: true,
			wantCode:      ErrAdapterPGConnect,
		},
		{
			name:          "unique_violation 23505 → permanent (delegates to classifyPGError, Connect code)",
			err:           &pgconn.PgError{Code: "23505"},
			wantTransient: false,
			wantCode:      ErrAdapterPGConnect,
		},
		{
			name:          "context.Canceled → permanent (caller abandoned, no ConnectTimeout substitution)",
			err:           context.Canceled,
			wantTransient: false,
			wantCode:      ErrAdapterPGConnect,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyPGConnectError(tt.err, opMsg)
			assert.Equal(t, tt.wantTransient, errcode.IsTransient(got),
				"IsTransient mismatch for %q", tt.name)
			var ec *errcode.Error
			require := assert.New(t)
			require.ErrorAs(got, &ec, "classify result must be *errcode.Error")
			assert.Equal(t, tt.wantCode, ec.Code,
				"code mismatch for %q", tt.name)
		})
	}
}
