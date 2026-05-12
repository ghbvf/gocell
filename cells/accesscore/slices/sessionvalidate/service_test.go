package sessionvalidate

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/sloghelper"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

var (
	testKeySet, testPrivKey, _ = auth.MustNewTestKeySet(clock.Real())
	testVerifier               *auth.JWTVerifier
)

func init() {
	var err error
	testVerifier, err = auth.NewJWTVerifier(testKeySet, clock.Real(), auth.WithExpectedAudiences("gocell"))
	if err != nil {
		panic("test setup: " + err.Error())
	}
}

// dNeg2h is the offset for seeding an expired session whose CreatedAt is 2h ago.
const dNeg2h = -2 * time.Hour

// testProtocol returns a Protocol suitable for in-memory session tests.
// FingerprintJTIRef requires a non-empty JTI on every seeded Session.
func testProtocol(t testing.TB) *session.Protocol {
	t.Helper()
	p, err := session.NewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOnAll(),
	)
	require.NoError(t, err)
	return p
}

// newTestStore constructs a MemStore for tests.
func newTestStore(t testing.TB) *session.MemStore {
	t.Helper()
	store, err := session.NewMemStore(testProtocol(t), clock.Real())
	require.NoError(t, err)
	return store
}

func TestService_VerifyIntent(t *testing.T) {
	store := newTestStore(t)

	// Seed an active session for revocation tests.
	require.NoError(t, store.Create(context.Background(), &session.Session{
		ID:        "sess-active",
		SubjectID: "usr-1",
		JTI:       "jti-active",
		ExpiresAt: time.Now().Add(time.Hour),
		CreatedAt: time.Now(),
	}))

	// Seed a revoked session.
	revokedAt := time.Now()
	require.NoError(t, store.Create(context.Background(), &session.Session{
		ID:        "sess-revoked",
		SubjectID: "usr-2",
		JTI:       "jti-revoked",
		ExpiresAt: time.Now().Add(time.Hour),
		CreatedAt: time.Now(),
		RevokedAt: &revokedAt,
	}))

	// Seed an expired session.
	require.NoError(t, store.Create(context.Background(), &session.Session{
		ID:        "sess-expired",
		SubjectID: "usr-3",
		JTI:       "jti-expired",
		ExpiresAt: time.Now().Add(-time.Hour), // already expired
		CreatedAt: time.Now().Add(dNeg2h),
	}))

	tests := []struct {
		name    string
		token   func() string
		wantSub string
		wantErr bool
	}{
		{
			name: "valid token without sid",
			token: func() string {
				tok, _ := IssueTestToken(testPrivKey, "usr-1", []string{"admin"}, time.Hour)
				return tok
			},
			wantSub: "usr-1",
			wantErr: true,
		},
		{
			name: "valid token with active session",
			token: func() string {
				tok, _ := IssueTestToken(testPrivKey, "usr-1", []string{"admin"}, time.Hour, "sess-active")
				return tok
			},
			wantSub: "usr-1",
			wantErr: false,
		},
		{
			name: "token with revoked session",
			token: func() string {
				tok, _ := IssueTestToken(testPrivKey, "usr-2", nil, time.Hour, "sess-revoked")
				return tok
			},
			wantErr: true,
		},
		{
			name: "token with non-existent session",
			token: func() string {
				tok, _ := IssueTestToken(testPrivKey, "usr-1", nil, time.Hour, "sess-nonexistent")
				return tok
			},
			wantErr: true,
		},
		{
			name: "token with expired session",
			token: func() string {
				tok, _ := IssueTestToken(testPrivKey, "usr-3", nil, time.Hour, "sess-expired")
				return tok
			},
			wantErr: true,
		},
		{
			name:    "empty token",
			token:   func() string { return "" },
			wantErr: true,
		},
		{
			name:    "invalid token",
			token:   func() string { return "bad.token.here" },
			wantErr: true,
		},
		{
			name: "expired token",
			token: func() string {
				tok, _ := IssueTestToken(testPrivKey, "usr-1", nil, -time.Hour)
				return tok
			},
			wantErr: true,
		},
		{
			name: "wrong signing key",
			token: func() string {
				wrongPriv, _ := auth.MustGenerateTestKeyPair()
				tok, _ := IssueTestToken(wrongPriv, "usr-1", nil, time.Hour)
				return tok
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(testVerifier, store, slog.Default(), clock.Real())

			claims, err := svc.VerifyIntent(context.Background(), tt.token(), auth.TokenIntentAccess)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantSub, claims.Subject)
				if tt.name == "valid token with active session" {
					assert.Contains(t, claims.Roles, "admin")
				}
				assert.Equal(t, "gocell-accesscore", claims.Issuer)
			}
		})
	}
}

func TestService_VerifyIntent_NilSessionStore(t *testing.T) {
	// When sessionStore is nil (backward compatibility), sid claim is ignored.
	svc := NewService(testVerifier, nil, slog.Default(), clock.Real())

	tok, err := IssueTestToken(testPrivKey, "usr-1", nil, time.Hour, "sess-any")
	require.NoError(t, err)

	claims, err := svc.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.Equal(t, "usr-1", claims.Subject)
}

// errorSessionStore simulates infrastructure failures (DB timeout, connection reset).
type errorSessionStore struct{}

func (errorSessionStore) Create(_ context.Context, _ *session.Session) error { return nil }
func (errorSessionStore) Get(_ context.Context, _ string) (*session.Session, error) {
	return nil, fmt.Errorf("db connection timeout")
}
func (errorSessionStore) Revoke(_ context.Context, _ string) error { return nil }
func (errorSessionStore) RevokeForSubject(_ context.Context, _ string, _ session.CredentialEvent) error {
	return nil
}

func TestService_VerifyIntent_DBError_FailsClosed(t *testing.T) {
	// Infrastructure errors (not just "not found") must also fail-closed.
	svc := NewService(testVerifier, errorSessionStore{}, slog.Default(), clock.Real())

	tok, err := IssueTestToken(testPrivKey, "usr-1", nil, time.Hour, "sess-db-fail")
	require.NoError(t, err)

	_, err = svc.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)
	require.Error(t, err, "DB errors must cause verification failure (fail-closed)")
	assert.Contains(t, err.Error(), "ERR_AUTH_INVALID_TOKEN")
}

func TestService_VerifyIntent_NilSessionStore_NoSid(t *testing.T) {
	// When sessionStore is nil (demo mode), tokens without sid are accepted.
	svc := NewService(testVerifier, nil, slog.Default(), clock.Real())

	tok, err := IssueTestToken(testPrivKey, "usr-1", nil, time.Hour)
	require.NoError(t, err)

	claims, err := svc.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.Equal(t, "usr-1", claims.Subject)
}

// capturingStore wraps a real or stub session store and allows injecting errors
// with specific errcode categories for logSessionLookupError tests.
type capturingStore struct {
	getErr error
}

func (r capturingStore) Create(_ context.Context, _ *session.Session) error { return nil }
func (r capturingStore) Get(_ context.Context, _ string) (*session.Session, error) {
	return nil, r.getErr
}
func (r capturingStore) Revoke(_ context.Context, _ string) error { return nil }
func (r capturingStore) RevokeForSubject(_ context.Context, _ string, _ session.CredentialEvent) error {
	return nil
}

// TestLogSessionLookupError_LogLevel verifies S40: IsDomainNotFound whitelist
// determines log level — only whitelisted domain not-found codes produce Warn;
// all other errors (infra, non-whitelisted errcode, plain) produce Error.
func TestLogSessionLookupError_LogLevel(t *testing.T) {
	tests := []struct {
		name          string
		storeErr      error
		wantLogLevel  slog.Level
		wantLogSubstr string
	}{
		{
			name:         "plain infra error logs at Error",
			storeErr:     fmt.Errorf("db connection timeout"),
			wantLogLevel: slog.LevelError,
		},
		{
			name: "errcode ErrSessionNotFound (domain, whitelist) logs at Warn",
			storeErr: errcode.New(errcode.KindNotFound, errcode.ErrSessionNotFound, "session not found",
				errcode.WithCategory(errcode.CategoryDomain)),
			wantLogLevel: slog.LevelWarn,
		},
		{
			name: "non-whitelisted errcode domain logs at Error",
			storeErr: errcode.New(errcode.KindNotFound, errcode.ErrOrderNotFound, "order not found",
				errcode.WithCategory(errcode.CategoryDomain)),
			wantLogLevel: slog.LevelError,
		},
		{
			name:         "errcode with CategoryInfra logs at Error",
			storeErr:     errcode.New(errcode.KindInternal, errcode.ErrInternal, "db down"),
			wantLogLevel: slog.LevelError,
		},
		{
			name:         "errcode with CategoryUnspecified (zero) logs at Error (fail-closed)",
			storeErr:     errcode.New(errcode.KindNotFound, errcode.ErrSessionNotFound, "not found"),
			wantLogLevel: slog.LevelError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			svc := NewService(testVerifier, capturingStore{getErr: tt.storeErr}, logger, clock.Real())

			tok, err := IssueTestToken(testPrivKey, "usr-log", nil, time.Hour, "sess-log-test")
			require.NoError(t, err)

			_, _ = svc.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)

			logOutput := buf.String()
			require.NotEmpty(t, logOutput, "expected at least one log line")

			// P1-3: use precise JSON-line matching to avoid false positives from
			// other log lines (e.g. JWT verification Warn). We locate the specific
			// session-lookup log line by message substring before asserting level.
			if tt.wantLogLevel == slog.LevelWarn {
				entry := sloghelper.FindLogEntry(logOutput, "session not found")
				require.NotNil(t, entry,
					"expected a log line containing 'session not found'")
				assert.Equal(t, "WARN", entry["level"],
					"domain not-found whitelisted error must log at WARN")
				// Confirm no ERROR line for this specific lookup message.
				errEntry := sloghelper.FindLogEntry(logOutput, "session repo unavailable")
				assert.Nil(t, errEntry,
					"must not emit ERROR 'session repo unavailable' when domain not-found whitelist matches")
			} else {
				entry := sloghelper.FindLogEntry(logOutput, "session repo unavailable")
				require.NotNil(t, entry,
					"expected a log line containing 'session repo unavailable'")
				assert.Equal(t, "ERROR", entry["level"],
					"infra / non-whitelisted error must log at ERROR")
			}
		})
	}
}
