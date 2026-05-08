// Package ports defines the driven-side interfaces for accesscore.
// Implementations live in adapters/ and are injected at assembly time.
package ports

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
)

// UserPatch carries optional field updates with optimistic-concurrency gating.
// Nil pointer fields mean "do not touch"; non-nil fields are written.
//
// Concurrency contract: ApplyPatch UPDATEs only when DB row version matches
// CurrentVersion; mismatch returns errcode.ErrAuthConcurrentUpdate. Callers
// must Get the row, mutate, and submit a Patch carrying the snapshot version.
//
// ref: K8s apimachinery resourceVersion CAS pattern
type UserPatch struct {
	ID                    string
	Username              *string
	Email                 *string
	PasswordHash          *string
	PasswordResetRequired *bool
	Status                *domain.UserStatus
	UpdatedAt             time.Time
	CurrentVersion        int64
}

// UserRepository persists and retrieves User aggregates.
type UserRepository interface {
	Create(ctx context.Context, user *domain.User) error
	GetByID(ctx context.Context, id string) (*domain.User, error)
	// GetByIDForUpdate is the row-locking variant used by flows that must
	// serialize against credential issuance by user id. PG implementation uses
	// SELECT ... FOR UPDATE; mem implementation acquires the write mutex. Must
	// be called inside an active TxRunner.RunInTx.
	GetByIDForUpdate(ctx context.Context, id string) (*domain.User, error)
	GetByUsername(ctx context.Context, username string) (*domain.User, error)
	// GetByUsernameForUpdate is the row-locking variant used by login flows.
	// PG implementation uses SELECT … FOR UPDATE; mem implementation acquires
	// the write mutex. Must be called inside an active TxRunner.RunInTx.
	GetByUsernameForUpdate(ctx context.Context, username string) (*domain.User, error)
	Delete(ctx context.Context, id string) error
	// ApplyPatch updates the user atomically gated on UserPatch.CurrentVersion.
	// Returns the post-update User on success; ErrAuthConcurrentUpdate when the
	// row's version no longer matches; ErrAuthUserDuplicate / ErrAuthEmailDuplicate
	// on uniqueness collision; ErrAuthUserNotFound when no row matches the ID.
	ApplyPatch(ctx context.Context, p UserPatch) (*domain.User, error)
}
