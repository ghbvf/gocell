// Package flagwrite implements the flag-write slice: Create/Update/Delete/Toggle
// feature flags with transactional outbox event publishing (L2 consistency).
//
// L2 OutboxFact: repo writes + outbox writes are wrapped in a single RunInTx
// per operation. Failure in either rolls back both.
//
// ref: Unleash src/lib/db/feature-environment-store.ts — "write + event must
// be in the same transaction" (Unleash lesson: splitting them caused data loss).
package flagwrite

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/cells/config-core/internal/ports"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/google/uuid"
)

// TopicFlagChanged is the outbox event topic for flag change events.
const TopicFlagChanged = domain.TopicFlagChanged

// FlagChangedPayload is the typed event payload for flag.changed.v1.
// JSON keys are camelCase per GoCell event payload convention.
type FlagChangedPayload struct {
	EventID    string    `json:"eventId"`
	Action     string    `json:"action"`
	Key        string    `json:"key"`
	Enabled    bool      `json:"enabled"`
	Version    int       `json:"version"`
	OccurredAt time.Time `json:"occurredAt"`
}

// Option configures a flag-write Service.
type Option func(*Service)

// WithOutboxWriter sets the outbox.Writer for transactional event publishing.
func WithOutboxWriter(w outbox.Writer) Option {
	return func(s *Service) { s.outboxWriter = w }
}

// WithTxManager sets the TxRunner for transactional guarantees (L2 atomicity).
func WithTxManager(tx persistence.TxRunner) Option {
	return func(s *Service) { s.txRunner = tx }
}

// Service implements flag write business logic (L2 OutboxFact).
type Service struct {
	repo         ports.FlagRepository
	outboxWriter outbox.Writer
	txRunner     persistence.TxRunner
	logger       *slog.Logger
}

// NewService creates a flag-write Service.
func NewService(repo ports.FlagRepository, logger *slog.Logger, opts ...Option) *Service {
	s := &Service{
		repo:   repo,
		logger: logger,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// CreateInput holds parameters for creating a feature flag.
type CreateInput struct {
	Key               string
	Enabled           bool
	RolloutPercentage int
	Description       string
}

// UpdateInput holds parameters for updating a feature flag.
type UpdateInput struct {
	Key               string
	Enabled           bool
	RolloutPercentage int
	Description       string
}

// Create creates a new feature flag and emits flag.changed.v1 (action=created).
func (s *Service) Create(ctx context.Context, input CreateInput) (*domain.FeatureFlag, error) {
	if input.Key == "" {
		return nil, errcode.New(errcode.ErrFlagInvalidInput, "key is required")
	}

	now := time.Now()
	flag := &domain.FeatureFlag{
		ID:                "flg-" + uuid.NewString(),
		Key:               input.Key,
		Enabled:           input.Enabled,
		RolloutPercentage: input.RolloutPercentage,
		Description:       input.Description,
		Version:           1,
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	if err := s.runInTx(ctx, func(txCtx context.Context) error {
		if err := s.repo.Create(txCtx, flag); err != nil {
			return fmt.Errorf("flag-write: create: %w", err)
		}
		return s.emitFlagChanged(txCtx, "created", flag)
	}); err != nil {
		return nil, err
	}

	s.logger.Info("feature flag created", slog.String("key", flag.Key))
	return flag, nil
}

// Update modifies an existing feature flag and emits flag.changed.v1 (action=updated).
// The repo UPDATE uses version=version+1 RETURNING to eliminate the read-modify-write
// TOCTOU race: two concurrent Updates both see the same DB-authoritative version.
func (s *Service) Update(ctx context.Context, input UpdateInput) (*domain.FeatureFlag, error) {
	if input.Key == "" {
		return nil, errcode.New(errcode.ErrFlagInvalidInput, "key is required")
	}

	var updated *domain.FeatureFlag

	if err := s.runInTx(ctx, func(txCtx context.Context) error {
		var err error
		updated, err = s.repo.Update(txCtx, input.Key, input.Enabled, input.RolloutPercentage, input.Description)
		if err != nil {
			return fmt.Errorf("flag-write: update: %w", err)
		}
		return s.emitFlagChanged(txCtx, "updated", updated)
	}); err != nil {
		return nil, err
	}

	s.logger.Info("feature flag updated",
		slog.String("key", updated.Key),
		slog.Int("version", updated.Version))
	return updated, nil
}

// Toggle toggles the enabled state of a feature flag and emits flag.changed.v1 (action=toggled).
// Toggle does not overwrite rollout_percentage or description.
func (s *Service) Toggle(ctx context.Context, key string, enabled bool) (*domain.FeatureFlag, error) {
	if key == "" {
		return nil, errcode.New(errcode.ErrFlagInvalidInput, "key is required")
	}

	var updated *domain.FeatureFlag

	if err := s.runInTx(ctx, func(txCtx context.Context) error {
		var err error
		updated, err = s.repo.Toggle(txCtx, key, enabled)
		if err != nil {
			return fmt.Errorf("flag-write: toggle: %w", err)
		}
		return s.emitFlagChanged(txCtx, "toggled", updated)
	}); err != nil {
		return nil, err
	}

	s.logger.Info("feature flag toggled",
		slog.String("key", key),
		slog.Bool("enabled", enabled))
	return updated, nil
}

// Delete removes a feature flag and emits flag.changed.v1 (action=deleted).
// The repo DELETE uses RETURNING to obtain the deleted entity atomically, eliminating
// the read-before-delete TOCTOU race where a concurrent Update could change the
// flag between GetByKey and DELETE.
func (s *Service) Delete(ctx context.Context, key string) error {
	if key == "" {
		return errcode.New(errcode.ErrFlagInvalidInput, "key is required")
	}

	if err := s.runInTx(ctx, func(txCtx context.Context) error {
		deleted, err := s.repo.Delete(txCtx, key)
		if err != nil {
			return fmt.Errorf("flag-write: delete: %w", err)
		}
		return s.emitFlagChanged(txCtx, "deleted", deleted)
	}); err != nil {
		return err
	}

	s.logger.Info("feature flag deleted", slog.String("key", key))
	return nil
}

// runInTx executes fn in a transaction if txRunner is configured, otherwise
// calls fn(ctx) directly. Cell Init() validates txRunner presence for CUD slices.
func (s *Service) runInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if s.txRunner != nil {
		return s.txRunner.RunInTx(ctx, fn)
	}
	return fn(ctx)
}

func (s *Service) emitFlagChanged(ctx context.Context, action string, flag *domain.FeatureFlag) error {
	if s.outboxWriter == nil {
		return nil
	}
	payload, err := json.Marshal(FlagChangedPayload{
		EventID:    uuid.NewString(),
		Action:     action,
		Key:        flag.Key,
		Enabled:    flag.Enabled,
		Version:    flag.Version,
		OccurredAt: time.Now(),
	})
	if err != nil {
		return fmt.Errorf("flag-write: marshal flag.changed.v1 payload: %w", err)
	}

	entry := outbox.Entry{
		ID:        outbox.NewEntryID(),
		EventType: TopicFlagChanged,
		Payload:   payload,
	}
	if err := s.outboxWriter.Write(ctx, entry); err != nil {
		return fmt.Errorf("flag-write: write outbox entry: %w", err)
	}
	return nil
}
