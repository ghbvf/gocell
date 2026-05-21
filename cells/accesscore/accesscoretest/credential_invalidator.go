package accesscoretest

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/accesscore/internal/credentialinvalidate"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// CredentialInvalidatorOption configures NewCredentialInvalidator.
type CredentialInvalidatorOption func(*invalidatorDeps)

// WithInvalidatorUserRepo overrides the FakeUserRepo used by the invalidator.
// By default NewCredentialInvalidator wires the FakeUserRepo from its argument
// (if called via BuildIdentityManageService) or creates a new empty one.
func WithInvalidatorUserRepo(repo *FakeUserRepo) CredentialInvalidatorOption {
	return func(d *invalidatorDeps) { d.userRepo = repo }
}

type invalidatorDeps struct {
	userRepo *FakeUserRepo
}

// NewCredentialInvalidator constructs a real *credentialinvalidate.Invalidator
// backed by stub session and refresh stores. The user repository is a FakeUserRepo
// so callers can seed and inspect state without a database.
//
// t.Cleanup is used for resource safety; the function will call t.Fatal on
// construction errors.
func NewCredentialInvalidator(t *testing.T, opts ...CredentialInvalidatorOption) *credentialinvalidate.Invalidator {
	t.Helper()
	d := &invalidatorDeps{userRepo: NewFakeUserRepo()}
	for _, o := range opts {
		o(d)
	}
	inv, err := credentialinvalidate.New(d.userRepo, &noopSessionStore{}, &noopRefreshStore{})
	if err != nil {
		t.Fatalf("accesscoretest.NewCredentialInvalidator: %v", err)
	}
	return inv
}

// noopSessionStore satisfies session.Store for tests that only exercise the
// invalidator's BumpAuthzEpoch path; RevokeForSubject is a no-op.
type noopSessionStore struct{}

var _ session.Store = (*noopSessionStore)(nil)

func (s *noopSessionStore) Create(_ context.Context, _ *session.Session) error { return nil }
func (s *noopSessionStore) Get(_ context.Context, _ string) (*session.ValidateView, error) {
	return nil, errcode.New(errcode.KindInternal, errcode.ErrNotImplemented, "noopSessionStore: Get not implemented")
}
func (s *noopSessionStore) Revoke(_ context.Context, _ string) error { return nil }
func (s *noopSessionStore) RevokeForSubject(_ context.Context, _ string, _ session.CredentialEvent) error {
	return nil
}
func (s *noopSessionStore) RepoReady(_ context.Context) error { return nil }

// noopRefreshStore satisfies refresh.Store; RevokeUser is a no-op.
type noopRefreshStore struct{}

var _ refresh.Store = (*noopRefreshStore)(nil)

func (s *noopRefreshStore) Issue(_ context.Context, _, _ string, _ int64) (string, *refresh.Token, error) {
	return "", nil, errcode.New(errcode.KindInternal, errcode.ErrNotImplemented, "noopRefreshStore: Issue not implemented")
}

func (s *noopRefreshStore) Peek(_ context.Context, _ string) (*refresh.Token, error) {
	return nil, errcode.New(errcode.KindInternal, errcode.ErrNotImplemented, "noopRefreshStore: Peek not implemented")
}

func (s *noopRefreshStore) Rotate(_ context.Context, _ string) (string, *refresh.Token, error) {
	return "", nil, errcode.New(errcode.KindInternal, errcode.ErrNotImplemented, "noopRefreshStore: Rotate not implemented")
}
func (s *noopRefreshStore) RevokeSession(_ context.Context, _ string) error         { return nil }
func (s *noopRefreshStore) RevokeSessionDetached(_ context.Context, _ string) error { return nil }
func (s *noopRefreshStore) RevokeUser(_ context.Context, _ string) error            { return nil }
func (s *noopRefreshStore) GC(_ context.Context, _ time.Time) (int, error)          { return 0, nil }
func (s *noopRefreshStore) RepoReady(_ context.Context) error                       { return nil }
