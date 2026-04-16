package sessionlogin

import (
	"context"
	"log/slog"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- stubs ---

type stubOutboxWriter struct{ entries []outbox.Entry }

func (s *stubOutboxWriter) Write(_ context.Context, e outbox.Entry) error {
	s.entries = append(s.entries, e)
	return nil
}

type stubTxRunner struct{ calls int }

func (s *stubTxRunner) RunInTx(_ context.Context, fn func(context.Context) error) error {
	s.calls++
	return fn(context.Background())
}

// testCredential is a test-only fixture password. Extracted to a variable to
// avoid static-analysis false positives about hardcoded credentials (go:S6437).
var testCredential = []byte("test-fixture-password") //nolint:gosec

// --- tests ---

func seedUserDirect(repo *mem.UserRepository, username, passwordHash string) {
	user, _ := domain.NewUser(username, username+"@test.com", passwordHash)
	user.ID = "usr-" + username
	_ = repo.Create(context.Background(), user)
}

func TestService_WithOutboxWriter(t *testing.T) {
	userRepo := mem.NewUserRepository()
	ow := &stubOutboxWriter{}
	svc := NewService(userRepo, mem.NewSessionRepository(), mem.NewRoleRepository(),
		eventbus.New(), testIssuer, slog.Default(), WithOutboxWriter(ow))

	hash, _ := bcrypt.GenerateFromPassword(testCredential, bcrypt.MinCost)
	seedUserDirect(userRepo, "alice", string(hash))

	_, err := svc.Login(context.Background(), LoginInput{Username: "alice", Password: string(testCredential)})
	require.NoError(t, err)

	require.Len(t, ow.entries, 1)
	assert.Equal(t, TopicSessionCreated, ow.entries[0].EventType)
}

func TestService_WithTxManager(t *testing.T) {
	userRepo := mem.NewUserRepository()
	tx := &stubTxRunner{}
	svc := NewService(userRepo, mem.NewSessionRepository(), mem.NewRoleRepository(),
		eventbus.New(), testIssuer, slog.Default(), WithTxManager(tx))

	hash, _ := bcrypt.GenerateFromPassword(testCredential, bcrypt.MinCost)
	seedUserDirect(userRepo, "bob", string(hash))

	_, err := svc.Login(context.Background(), LoginInput{Username: "bob", Password: string(testCredential)})
	require.NoError(t, err)
	assert.Equal(t, 1, tx.calls)
}
