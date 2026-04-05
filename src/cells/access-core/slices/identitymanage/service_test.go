package identitymanage

import (
	"context"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestService() *Service {
	return NewService(mem.NewUserRepository(), eventbus.New(), slog.Default())
}

func TestService_Create(t *testing.T) {
	tests := []struct {
		name    string
		input   CreateInput
		wantErr bool
	}{
		{name: "valid", input: CreateInput{Username: "alice", Email: "a@b.c", Password: "hash"}, wantErr: false},
		{name: "empty username", input: CreateInput{Username: "", Email: "a@b.c", Password: "hash"}, wantErr: true},
		{name: "empty email", input: CreateInput{Username: "alice", Email: "", Password: "hash"}, wantErr: true},
		{name: "empty password", input: CreateInput{Username: "alice", Email: "a@b.c", Password: ""}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestService()
			user, err := svc.Create(context.Background(), tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.NotEmpty(t, user.ID)
				assert.Equal(t, tt.input.Username, user.Username)
			}
		})
	}
}

func TestService_LockUnlock(t *testing.T) {
	svc := newTestService()
	user, err := svc.Create(context.Background(), CreateInput{
		Username: "bob", Email: "b@c.d", Password: "hash",
	})
	require.NoError(t, err)

	// Lock
	require.NoError(t, svc.Lock(context.Background(), user.ID))
	locked, _ := svc.GetByID(context.Background(), user.ID)
	assert.True(t, locked.IsLocked())

	// Unlock
	require.NoError(t, svc.Unlock(context.Background(), user.ID))
	unlocked, _ := svc.GetByID(context.Background(), user.ID)
	assert.False(t, unlocked.IsLocked())
}

func TestService_Delete(t *testing.T) {
	svc := newTestService()
	user, _ := svc.Create(context.Background(), CreateInput{
		Username: "del", Email: "d@e.f", Password: "hash",
	})

	require.NoError(t, svc.Delete(context.Background(), user.ID))
	_, err := svc.GetByID(context.Background(), user.ID)
	assert.Error(t, err)
}

func TestService_Update(t *testing.T) {
	svc := newTestService()
	user, _ := svc.Create(context.Background(), CreateInput{
		Username: "upd", Email: "old@e.f", Password: "hash",
	})

	updated, err := svc.Update(context.Background(), UpdateInput{ID: user.ID, Email: "new@e.f"})
	require.NoError(t, err)
	assert.Equal(t, "new@e.f", updated.Email)
}
