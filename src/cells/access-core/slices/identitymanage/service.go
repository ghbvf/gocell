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
	"github.com/ghbvf/gocell/pkg/errcode"
)

const (
	TopicUserCreated = "event.user.created.v1"
	TopicUserLocked  = "event.user.locked.v1"
	ErrIdentityInput errcode.Code = "ERR_AUTH_IDENTITY_INVALID_INPUT"
)

// Service implements identity management business logic.
type Service struct {
	repo      ports.UserRepository
	publisher outbox.Publisher
	logger    *slog.Logger
}

// NewService creates an identity-manage Service.
func NewService(repo ports.UserRepository, pub outbox.Publisher, logger *slog.Logger) *Service {
	return &Service{repo: repo, publisher: pub, logger: logger}
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
		return nil, errcode.New(ErrIdentityInput, "password is required")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("identity-manage: hash password: %w", err)
	}

	user, err := domain.NewUser(input.Username, input.Email, string(hash))
	if err != nil {
		return nil, err
	}

	user.ID = fmt.Sprintf("usr-%d", time.Now().UnixNano())

	if err := s.repo.Create(ctx, user); err != nil {
		return nil, fmt.Errorf("identity-manage: create: %w", err)
	}

	s.publish(ctx, TopicUserCreated, map[string]any{
		"user_id": user.ID, "username": user.Username,
	})
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

// UpdateInput holds parameters for updating a user.
type UpdateInput struct {
	ID    string
	Email string
}

// Update modifies user attributes.
func (s *Service) Update(ctx context.Context, input UpdateInput) (*domain.User, error) {
	if input.ID == "" {
		return nil, errcode.New(ErrIdentityInput, "id is required")
	}

	user, err := s.repo.GetByID(ctx, input.ID)
	if err != nil {
		return nil, fmt.Errorf("identity-manage: update: %w", err)
	}

	if input.Email != "" {
		user.Email = input.Email
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
		return errcode.New(ErrIdentityInput, "id is required")
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
		return errcode.New(ErrIdentityInput, "id is required")
	}

	user, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("identity-manage: lock: %w", err)
	}

	user.Lock()
	if err := s.repo.Update(ctx, user); err != nil {
		return fmt.Errorf("identity-manage: lock: %w", err)
	}

	s.publish(ctx, TopicUserLocked, map[string]any{"user_id": id})
	s.logger.Info("user locked", slog.String("user_id", id))
	return nil
}

// Unlock unlocks a user account.
func (s *Service) Unlock(ctx context.Context, id string) error {
	if id == "" {
		return errcode.New(ErrIdentityInput, "id is required")
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

func (s *Service) publish(ctx context.Context, topic string, payload map[string]any) {
	data, _ := json.Marshal(payload)
	if err := s.publisher.Publish(ctx, topic, data); err != nil {
		s.logger.Error("identity-manage: failed to publish event",
			slog.Any("error", err), slog.String("topic", topic))
	}
}
