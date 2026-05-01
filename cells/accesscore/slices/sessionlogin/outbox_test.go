package sessionlogin

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/internal/testoutbox"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	refreshmem "github.com/ghbvf/gocell/runtime/auth/refresh/memstore"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newOutboxRefreshStore() refresh.Store {
	clock := storetest.NewFakeClock(time.Now())
	return refreshmem.MustNew(refresh.Policy{ReuseInterval: testtime.D2s, MaxAge: time.Hour}, clock, nil)
}

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
var testCredential = []byte("test-fixture-password")

// --- tests ---

func seedUserDirect(repo *mem.UserRepository, username, passwordHash string) {
	user, _ := domain.NewUser(username, username+"@test.com", passwordHash)
	user.ID = "usr-" + username
	_ = repo.Create(context.Background(), user)
}

func TestService_WithEmitter(t *testing.T) {
	userRepo := mem.NewUserRepository()
	ow := &stubOutboxWriter{}
	svc := MustNewService(userRepo, mem.NewSessionRepository(), mem.NewRoleRepository(),
		newOutboxRefreshStore(), testIssuer, slog.Default(), WithEmitter(testoutbox.MustEmitter(t, ow)))

	hash, _ := bcrypt.GenerateFromPassword(testCredential, bcrypt.MinCost)
	seedUserDirect(userRepo, "alice", string(hash))

	_, err := svc.Login(context.Background(), LoginInput{Username: "alice", Password: string(testCredential)})
	require.NoError(t, err)

	require.Len(t, ow.entries, 1)
	assert.Equal(t, dto.TopicSessionCreated, ow.entries[0].EventType)
}

func TestService_WithTxManager(t *testing.T) {
	userRepo := mem.NewUserRepository()
	tx := &stubTxRunner{}
	svc := MustNewService(userRepo, mem.NewSessionRepository(), mem.NewRoleRepository(),
		newOutboxRefreshStore(), testIssuer, slog.Default(), WithTxManager(tx))

	hash, _ := bcrypt.GenerateFromPassword(testCredential, bcrypt.MinCost)
	seedUserDirect(userRepo, "bob", string(hash))

	_, err := svc.Login(context.Background(), LoginInput{Username: "bob", Password: string(testCredential)})
	require.NoError(t, err)
	assert.Equal(t, 1, tx.calls)
}

// failingEmitter returns an error on every Emit call.
type failingEmitter struct{ err error }

func (f *failingEmitter) Emit(_ context.Context, _ outbox.Entry) error { return f.err }

// trackingOutboxSessionRepo wraps mem.SessionRepository and records Delete calls.
type trackingOutboxSessionRepo struct {
	*mem.SessionRepository
	deleted []string
}

func (r *trackingOutboxSessionRepo) Delete(ctx context.Context, id string) error {
	r.deleted = append(r.deleted, id)
	return r.SessionRepository.Delete(ctx, id)
}

// TestPersistSessionWithRefresh_DurableTx_EmitFails_NoExplicitCleanup verifies
// that when a durable (non-noop) TxRunner is used and outbox.Emit fails,
// no explicit cleanupIssuedSession call is made. The tx rollback handles
// atomicity; explicit cleanup would double-delete in a real durable setup.
func TestPersistSessionWithRefresh_DurableTx_EmitFails_NoExplicitCleanup(t *testing.T) {
	userRepo := mem.NewUserRepository()
	sessionRepo := &trackingOutboxSessionRepo{SessionRepository: mem.NewSessionRepository()}
	roleRepo := mem.NewRoleRepository()

	emitter := &failingEmitter{err: fmt.Errorf("broker down")}
	// stubTxRunner is NOT a Nooper — isNoopTx(tx) returns false.
	tx := &stubTxRunner{}

	svc := MustNewService(userRepo, sessionRepo, roleRepo, newOutboxRefreshStore(), testIssuer, slog.Default(),
		WithEmitter(emitter),
		WithTxManager(tx))

	hash, _ := bcrypt.GenerateFromPassword(testCredential, bcrypt.MinCost)
	seedUserDirect(userRepo, "durable-emit-fail", string(hash))

	_, err := svc.Login(context.Background(), LoginInput{Username: "durable-emit-fail", Password: string(testCredential)})
	require.Error(t, err, "emit failure must propagate as an error")

	// In durable tx mode, cleanupIssuedSession must NOT be called (tx rollback handles it).
	assert.Len(t, sessionRepo.deleted, 0,
		"durable tx: no explicit Delete during emit failure — tx rollback is the recovery mechanism")
}

// TestPersistSessionWithRefresh_NoopTxRunner_EmitFails_CleanupRuns verifies
// that when NoopTxRunner (demo mode) is in use and outbox.Emit fails,
// cleanupIssuedSession IS called to compensate the already-written session.
// This is the mirror case of the durable-tx test above.
func TestPersistSessionWithRefresh_NoopTxRunner_EmitFails_CleanupRuns(t *testing.T) {
	userRepo := mem.NewUserRepository()
	sessionRepo := &trackingOutboxSessionRepo{SessionRepository: mem.NewSessionRepository()}
	roleRepo := mem.NewRoleRepository()

	emitter := &failingEmitter{err: fmt.Errorf("broker down")}
	// No WithTxManager → service defaults to NoopTxRunner — isNoopTx returns true.

	svc := MustNewService(userRepo, sessionRepo, roleRepo, newOutboxRefreshStore(), testIssuer, slog.Default(),
		WithEmitter(emitter))

	hash, _ := bcrypt.GenerateFromPassword(testCredential, bcrypt.MinCost)
	seedUserDirect(userRepo, "noop-emit-fail", string(hash))

	_, err := svc.Login(context.Background(), LoginInput{Username: "noop-emit-fail", Password: string(testCredential)})
	require.Error(t, err, "emit failure must propagate as an error")

	// In noop tx mode, cleanupIssuedSession must compensate the session write.
	assert.Len(t, sessionRepo.deleted, 1,
		"noop tx (demo mode): explicit Delete must run to compensate the already-written session")
}
