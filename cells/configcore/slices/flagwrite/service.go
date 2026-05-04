// Package flagwrite implements the flag-write slice: Create/Update/Delete/Toggle
// feature flags with transactional repo writes (L1 consistency).
//
// L1 LocalTx: repo writes are wrapped in a single RunInTx per operation.
// Failure rolls back the transaction.
//
// event.flag.changed.v1 was retired by PR-CFG-B (2026-04-25): the contract
// never had a subscriber, so emitting it was dead work. The contract is now
// lifecycle: deprecated. Flag write is downgraded to L1.
package flagwrite

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/configcore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// Option configures a flag-write Service.
type Option func(*Service)

// WithTxManager sets the TxRunner for transactional guarantees (L1 atomicity).
func WithTxManager(tx persistence.TxRunner) Option {
	return func(s *Service) {
		if tx != nil {
			s.txRunner = tx
		}
	}
}

// Service implements flag write business logic (L1 LocalTx).
type Service struct {
	repo     ports.FlagRepository
	txRunner persistence.TxRunner
	logger   *slog.Logger
	clock    clock.Clock
}

// NewService creates a flag-write Service.
// clk must be non-nil; pass clock.Real() in production and clockmock.New() in tests.
// TxRunner must be provided via WithTxManager; nil txRunner is rejected to
// prevent silent loss of L1 atomicity guarantees on flag writes.
func NewService(repo ports.FlagRepository, logger *slog.Logger, clk clock.Clock, opts ...Option) (*Service, error) {
	clock.MustHaveClock(clk, "flagwrite.NewService")
	s := &Service{
		repo:   repo,
		logger: logger,
		clock:  clk,
	}
	for _, o := range opts {
		o(s)
	}
	if s.txRunner == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"flagwrite: TxRunner required; use WithTxManager")
	}
	return s, nil
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

// Create creates a new feature flag (L1 LocalTx).
func (s *Service) Create(ctx context.Context, input CreateInput) (*domain.FeatureFlag, error) {
	if err := validation.RequireNotEmpty(errcode.ErrFlagInvalidInput,
		validation.F("key", input.Key),
	); err != nil {
		return nil, err
	}

	now := s.clock.Now()
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
		return nil
	}); err != nil {
		return nil, err
	}

	s.logger.Info("feature flag created", slog.String("key", flag.Key))
	return flag, nil
}

// Update modifies an existing feature flag (L1 LocalTx).
// The repo UPDATE uses version=version+1 RETURNING to eliminate the read-modify-write
// TOCTOU race: two concurrent Updates both see the same DB-authoritative version.
func (s *Service) Update(ctx context.Context, input UpdateInput) (*domain.FeatureFlag, error) {
	if err := validation.RequireNotEmpty(errcode.ErrFlagInvalidInput,
		validation.F("key", input.Key),
	); err != nil {
		return nil, err
	}

	var updated *domain.FeatureFlag

	if err := s.runInTx(ctx, func(txCtx context.Context) error {
		var err error
		updated, err = s.repo.Update(txCtx, input.Key, input.Enabled, input.RolloutPercentage, input.Description)
		if err != nil {
			return fmt.Errorf("flag-write: update: %w", err)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	s.logger.Info("feature flag updated",
		slog.String("key", updated.Key),
		slog.Int("version", updated.Version))
	return updated, nil
}

// Toggle toggles the enabled state of a feature flag (L1 LocalTx).
// Toggle does not overwrite rollout_percentage or description.
func (s *Service) Toggle(ctx context.Context, key string, enabled bool) (*domain.FeatureFlag, error) {
	if err := validation.RequireNotEmpty(errcode.ErrFlagInvalidInput,
		validation.F("key", key),
	); err != nil {
		return nil, err
	}

	var updated *domain.FeatureFlag

	if err := s.runInTx(ctx, func(txCtx context.Context) error {
		var err error
		updated, err = s.repo.Toggle(txCtx, key, enabled)
		if err != nil {
			return fmt.Errorf("flag-write: toggle: %w", err)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	s.logger.Info("feature flag toggled",
		slog.String("key", key),
		slog.Bool("enabled", enabled))
	return updated, nil
}

// Delete removes a feature flag (L1 LocalTx).
// The repo DELETE uses RETURNING to obtain the deleted entity atomically, eliminating
// the read-before-delete TOCTOU race where a concurrent Update could change the
// flag between GetByKey and DELETE.
func (s *Service) Delete(ctx context.Context, key string) error {
	if err := validation.RequireNotEmpty(errcode.ErrFlagInvalidInput,
		validation.F("key", key),
	); err != nil {
		return err
	}

	if err := s.runInTx(ctx, func(txCtx context.Context) error {
		if _, err := s.repo.Delete(txCtx, key); err != nil {
			return fmt.Errorf("flag-write: delete: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}

	s.logger.Info("feature flag deleted", slog.String("key", key))
	return nil
}

// runInTx wraps fn in a transaction. txRunner is guaranteed non-nil by the
// constructor's fail-fast check; demo callers must inject an explicit
// pass-through TxRunner via WithTxManager.
func (s *Service) runInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return s.txRunner.RunInTx(ctx, fn)
}
