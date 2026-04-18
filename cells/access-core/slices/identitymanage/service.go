// Package identitymanage implements the identity-manage slice: CRUD + Lock/Unlock
// user accounts.
package identitymanage

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/dto"
	"github.com/ghbvf/gocell/cells/access-core/internal/ports"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/google/uuid"
)

// TokenIssuer is a narrow interface for issuing a new token pair after a
// password change. The implementation is sessionlogin.Service.IssueForUser,
// injected via WithTokenIssuer to avoid a cross-slice import. The returned
// type is *dto.TokenPair (internal/dto) so identitymanage does not import
// sessionlogin directly (F-ARCH-1).
type TokenIssuer interface {
	IssueForUser(ctx context.Context, userID string) (*dto.TokenPair, error)
}

const (
	TopicUserCreated = "event.user.created.v1"
	TopicUserLocked  = "event.user.locked.v1"
)

// Option configures an identity-manage Service.
type Option func(*Service)

// WithOutboxWriter sets the outbox.Writer for transactional event publishing.
func WithOutboxWriter(w outbox.Writer) Option {
	return func(s *Service) { s.outboxWriter = w }
}

// WithTxManager sets the TxRunner for transactional guarantees (L2 atomicity).
func WithTxManager(tx persistence.TxRunner) Option {
	return func(s *Service) { s.txRunner = tx }
}

// WithTokenIssuer injects the token issuer used by ChangePassword to issue a
// fresh TokenPair after a successful password change. tokenIssuer must not be
// nil; NewService returns an error if it is not provided or is nil.
func WithTokenIssuer(ti TokenIssuer) Option {
	return func(s *Service) { s.tokenIssuer = ti }
}

// Service implements identity management business logic.
type Service struct {
	repo         ports.UserRepository
	sessionRepo  ports.SessionRepository
	publisher    outbox.Publisher
	outboxWriter outbox.Writer
	txRunner     persistence.TxRunner
	logger       *slog.Logger
	tokenIssuer  TokenIssuer
}

// NewService creates an identity-manage Service. tokenIssuer is required;
// callers must supply it via WithTokenIssuer. Omitting it or passing nil
// returns errcode.ErrCellMissingTokenIssuer so mis-wired assemblies fail at
// startup rather than at the first ChangePassword call.
func NewService(repo ports.UserRepository, sessionRepo ports.SessionRepository, pub outbox.Publisher, logger *slog.Logger, opts ...Option) (*Service, error) {
	s := &Service{repo: repo, sessionRepo: sessionRepo, publisher: pub, logger: logger}
	for _, o := range opts {
		o(s)
	}
	if s.tokenIssuer == nil {
		return nil, errcode.New(errcode.ErrCellMissingTokenIssuer,
			"identity-manage: tokenIssuer is required; wire via WithTokenIssuer")
	}
	return s, nil
}

// CreateInput holds parameters for creating a user.
type CreateInput struct {
	Username             string
	Email                string
	Password             string
	RequirePasswordReset bool
}

// Create creates a new user and publishes an event.
// The plain-text password is bcrypt-hashed before storage.
func (s *Service) Create(ctx context.Context, input CreateInput) (*domain.User, error) {
	if input.Password == "" {
		return nil, errcode.New(errcode.ErrAuthIdentityInvalidInput, "password is required")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), domain.BcryptCost)
	if err != nil {
		return nil, fmt.Errorf("identity-manage: hash password: %w", err)
	}

	user, err := domain.NewUser(input.Username, input.Email, string(hash))
	if err != nil {
		return nil, err
	}

	user.ID = "usr" + "-" + uuid.NewString()
	if input.RequirePasswordReset {
		user.MarkPasswordResetRequired()
	}

	eventPayload := map[string]any{"user_id": user.ID, "username": user.Username}
	if err := s.runInTx(ctx, func(txCtx context.Context) error {
		if err := s.repo.Create(txCtx, user); err != nil {
			return fmt.Errorf("identity-manage: create: %w", err)
		}
		if err := s.publish(txCtx, TopicUserCreated, eventPayload); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return nil, err
	}

	s.logger.Info("user created", slog.String("user_id", user.ID))
	return user, nil
}

// GetByID retrieves a user by ID.
func (s *Service) GetByID(ctx context.Context, id string) (*domain.User, error) {
	user, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("identity-manage: get: %w", err)
	}
	return user, nil
}

// UpdateInput holds parameters for updating a user (JSON merge patch semantics).
// Nil pointer fields mean "do not update"; non-nil means "set to this value".
type UpdateInput struct {
	ID                   string
	Name                 *string
	Email                *string
	Status               *string
	RequirePasswordReset *bool // nil=no change, true=mark, false=clear
}

// Update modifies user attributes using JSON merge patch semantics:
// only non-nil fields are applied; missing fields are left unchanged.
func (s *Service) Update(ctx context.Context, input UpdateInput) (*domain.User, error) {
	if input.ID == "" {
		return nil, errcode.New(errcode.ErrAuthIdentityInvalidInput, "id is required")
	}

	user, err := s.repo.GetByID(ctx, input.ID)
	if err != nil {
		return nil, fmt.Errorf("identity-manage: update: %w", err)
	}

	if input.Name != nil {
		user.Username = *input.Name
	}
	if input.Email != nil {
		user.Email = *input.Email
	}
	if input.Status != nil {
		status := domain.UserStatus(*input.Status)
		if *input.Status != string(domain.StatusActive) && *input.Status != string(domain.StatusSuspended) {
			return nil, errcode.New(errcode.ErrAuthIdentityInvalidInput, "status must be 'active' or 'suspended'")
		}
		user.Status = status
	}
	if input.RequirePasswordReset != nil {
		if *input.RequirePasswordReset {
			user.MarkPasswordResetRequired()
		} else {
			user.ClearPasswordResetRequired()
		}
	}
	user.UpdatedAt = time.Now()

	if err := s.repo.Update(ctx, user); err != nil {
		return nil, fmt.Errorf("identity-manage: update: %w", err)
	}

	s.logger.Info("user updated", slog.String("user_id", user.ID))
	return user, nil
}

// Delete removes a user.
func (s *Service) Delete(ctx context.Context, id string) error {
	if id == "" {
		return errcode.New(errcode.ErrAuthIdentityInvalidInput, "id is required")
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return fmt.Errorf("identity-manage: delete: %w", err)
	}
	s.logger.Info("user deleted", slog.String("user_id", id))
	return nil
}

// Lock locks a user account and publishes an event.
func (s *Service) Lock(ctx context.Context, id string) error {
	if id == "" {
		return errcode.New(errcode.ErrAuthIdentityInvalidInput, "id is required")
	}

	user, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("identity-manage: lock: %w", err)
	}

	user.Lock()
	if err := s.runInTx(ctx, func(txCtx context.Context) error {
		if err := s.repo.Update(txCtx, user); err != nil {
			return fmt.Errorf("identity-manage: lock: %w", err)
		}
		// F17: revoke all sessions for the locked user. Failure must abort the
		// transaction (mirrors ChangePassword): silently logging would commit
		// the lock flag while leaving stolen access tokens able to call
		// business endpoints until natural expiry — which is the exact attack
		// vector "Lock" exists to prevent.
		if err := s.sessionRepo.RevokeByUserID(txCtx, id); err != nil {
			return fmt.Errorf("identity-manage: lock revoke sessions: %w", err)
		}
		if err := s.publish(txCtx, TopicUserLocked, map[string]any{"user_id": id}); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}

	s.logger.Info("user locked", slog.String("user_id", id))
	return nil
}

// Unlock unlocks a user account.
func (s *Service) Unlock(ctx context.Context, id string) error {
	if id == "" {
		return errcode.New(errcode.ErrAuthIdentityInvalidInput, "id is required")
	}

	user, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("identity-manage: unlock: %w", err)
	}

	user.Unlock()
	if err := s.repo.Update(ctx, user); err != nil {
		return fmt.Errorf("identity-manage: unlock: %w", err)
	}

	s.logger.Info("user unlocked", slog.String("user_id", id))
	return nil
}

// ChangePasswordInput holds the parameters for changing a user password.
type ChangePasswordInput struct {
	UserID      string
	OldPassword string
	NewPassword string
}

// ChangePassword verifies the old password, hashes the new one, clears the
// PasswordResetRequired flag, updates the user, and issues a fresh TokenPair.
//
// Validation order (P1-9 fix: cheap checks before bcrypt to avoid wasted CPU):
//  1. Required-field check (empty userID / oldPassword / newPassword).
//  2. Cheap string equality check (new == old rejected before bcrypt cost).
//  3. bcrypt.CompareHashAndPassword (old password verification).
//  4. Hash new password.
//  5. Persist updated user.
//  6. Issue new TokenPair via tokenIssuer.
//
// Consistency level: L1 (single-cell local transaction, no outbox event).
// The token pair is issued synchronously so the client can replace stale tokens
// without a forced re-login — this is critical when the old token carried
// password_reset_required=true and would be rejected by the middleware.
func (s *Service) ChangePassword(ctx context.Context, input ChangePasswordInput) (*dto.TokenPair, error) {
	if input.UserID == "" || input.OldPassword == "" || input.NewPassword == "" {
		return nil, errcode.New(errcode.ErrAuthIdentityInvalidInput, "userID, oldPassword and newPassword are required")
	}

	// Step 2: Cheap equality check before the expensive bcrypt call.
	// An authenticated user submitting new==old is a client error regardless of
	// whether the old password is correct; no bcrypt cost is warranted.
	if input.NewPassword == input.OldPassword {
		return nil, errcode.New(errcode.ErrAuthLoginInvalidInput, "new password must differ from old password")
	}

	user, err := s.repo.GetByID(ctx, input.UserID)
	if err != nil {
		return nil, fmt.Errorf("identity-manage: change-password get user: %w", err)
	}

	// Step 3: Verify old password (expensive).
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(input.OldPassword)); err != nil {
		return nil, errcode.New(errcode.ErrAuthLoginFailed, "old password incorrect")
	}

	newHash, err := bcrypt.GenerateFromPassword([]byte(input.NewPassword), domain.BcryptCost)
	if err != nil {
		return nil, fmt.Errorf("identity-manage: change-password hash: %w", err)
	}

	user.PasswordHash = string(newHash)
	user.ClearPasswordResetRequired()

	// F2 session convergence + F10 atomic boundary: wrap the password write and
	// the session revoke in a single transaction so a RevokeByUserID failure
	// rolls back the password change. Without this, PG could commit the new hash
	// but leave old sessions live — a stolen refresh token would keep minting
	// access tokens despite the password rotation. IssueForUser stays outside
	// the tx because it creates a NEW session that must not be caught by the
	// revoke sweep, and because signing failure should not roll back a
	// legitimate password change.
	if err := s.runInTx(ctx, func(txCtx context.Context) error {
		if err := s.repo.Update(txCtx, user); err != nil {
			return fmt.Errorf("identity-manage: change-password update: %w", err)
		}
		if err := s.sessionRepo.RevokeByUserID(txCtx, user.ID); err != nil {
			return fmt.Errorf("identity-manage: change-password revoke sessions: %w", err)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	s.logger.Info("user password changed; prior sessions revoked",
		slog.String("user_id", user.ID))

	pair, err := s.tokenIssuer.IssueForUser(ctx, user.ID)
	if err != nil {
		return nil, fmt.Errorf("identity-manage: change-password issue token: %w", err)
	}
	return pair, nil
}

func (s *Service) publish(ctx context.Context, topic string, payload map[string]any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("identity-manage: marshal event payload: %w", err)
	}
	if s.outboxWriter != nil {
		entry := outbox.Entry{
			ID:        outbox.NewEntryID(),
			EventType: topic,
			Payload:   data,
		}
		if err := s.outboxWriter.Write(ctx, entry); err != nil {
			return fmt.Errorf("identity-manage: write outbox entry: %w", err)
		}
		return nil
	}
	// Demo mode: publisher failure is logged but not propagated since
	// demo mode does not guarantee L2 atomicity.
	if err := s.publisher.Publish(ctx, topic, data); err != nil {
		s.logger.Warn("identity-manage: failed to publish event (demo mode)",
			slog.Any("error", err), slog.String("topic", topic))
	}
	return nil
}

// runInTx executes fn in a transaction if txRunner is configured, otherwise
// calls fn(ctx) directly. Nil txRunner is intentional for query-only slices;
// Cell Init() validates txRunner presence for CUD slices before Start().
func (s *Service) runInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if s.txRunner != nil {
		return s.txRunner.RunInTx(ctx, fn)
	}
	return fn(ctx)
}
