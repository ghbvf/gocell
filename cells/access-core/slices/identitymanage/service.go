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
	"github.com/ghbvf/gocell/cells/access-core/internal/ports"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/google/uuid"
)

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

// Service implements identity management business logic.
type Service struct {
	repo         ports.UserRepository
	sessionRepo  ports.SessionRepository
	publisher    outbox.Publisher
	outboxWriter outbox.Writer
	txRunner     persistence.TxRunner
	logger       *slog.Logger
}

// NewService creates an identity-manage Service.
func NewService(repo ports.UserRepository, sessionRepo ports.SessionRepository, pub outbox.Publisher, logger *slog.Logger, opts ...Option) *Service {
	s := &Service{repo: repo, sessionRepo: sessionRepo, publisher: pub, logger: logger}
	for _, o := range opts {
		o(s)
	}
	return s
}

// CreateInput holds parameters for creating a user.
type CreateInput struct {
	Username string
	Email    string
	Password string
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
	ID     string
	Name   *string
	Email  *string
	Status *string
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
		// Revoke all sessions for the locked user so existing tokens become invalid.
		if err := s.sessionRepo.RevokeByUserID(txCtx, id); err != nil {
			s.logger.Error("identity-manage: failed to revoke sessions on lock",
				slog.Any("error", err), slog.String("user_id", id))
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

func (s *Service) publish(ctx context.Context, topic string, payload map[string]any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("identity-manage: marshal event payload: %w", err)
	}
	if s.outboxWriter != nil {
		entry := outbox.Entry{
			ID:        "evt" + "-" + uuid.NewString(),
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
