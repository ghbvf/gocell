// Package configwrite implements the config-write slice: Create/Update/Delete
// config entries with event publishing.
package configwrite

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	configevents "github.com/ghbvf/gocell/cells/configcore/internal/events"
	"github.com/ghbvf/gocell/cells/configcore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/auth"
)

// Option configures a config-write Service.
type Option func(*Service)

// WithEmitter sets the event emitter.
func WithEmitter(e outbox.Emitter) Option {
	return func(s *Service) {
		if e != nil {
			s.emitter = e
		}
	}
}

// WithTxManager sets the TxRunner for transactional guarantees (L2 atomicity).
func WithTxManager(tx persistence.TxRunner) Option {
	return func(s *Service) { s.txRunner = persistence.RunnerOrNoop(tx) }
}

// Service implements config write business logic.
type Service struct {
	repo     ports.ConfigRepository
	txRunner persistence.TxRunner
	emitter  outbox.Emitter
	logger   *slog.Logger
	clock    clock.Clock
}

// NewService creates a config-write Service.
// clk must be non-nil; pass clock.Real() in production and clockmock.New() in tests.
func NewService(repo ports.ConfigRepository, logger *slog.Logger, clk clock.Clock, opts ...Option) *Service {
	clock.MustHaveClock(clk, "configwrite.NewService")
	s := &Service{
		repo:     repo,
		txRunner: persistence.NoopTxRunner{},
		emitter:  outbox.NewNoopEmitter(),
		logger:   logger,
		clock:    clk,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// CreateInput holds parameters for creating a config entry.
type CreateInput struct {
	Key       string
	Value     string
	Sensitive bool
}

// Create creates a new config entry and publishes a change event.
func (s *Service) Create(ctx context.Context, input CreateInput) (*domain.ConfigEntry, error) {
	if err := validation.RequireNotBlank(errcode.ErrConfigInvalidInput,
		validation.F("key", input.Key),
	); err != nil {
		return nil, err
	}

	actor, err := actorFromContext(ctx)
	if err != nil {
		return nil, err
	}

	now := s.clock.Now()
	entry := &domain.ConfigEntry{
		ID:        "cfg" + "-" + uuid.NewString(),
		Key:       input.Key,
		Value:     input.Value,
		Sensitive: input.Sensitive,
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.runInTx(ctx, func(txCtx context.Context) error {
		if err := s.repo.Create(txCtx, entry); err != nil {
			return fmt.Errorf("config-write: create: %w", err)
		}
		return s.publishUpserted(txCtx, entry, actor)
	}); err != nil {
		return nil, err
	}

	s.logger.Info("config entry created", slog.String("key", entry.Key))
	return entry, nil
}

// UpdateInput holds parameters for updating a config entry.
type UpdateInput struct {
	Key   string
	Value string
}

// Update modifies an existing config entry and publishes a change event.
// The repo reads the sensitive flag internally via SELECT...FOR UPDATE, so no
// pre-read is needed here. The entire update and outbox write are wrapped in
// a single transaction for L2 atomicity.
func (s *Service) Update(ctx context.Context, input UpdateInput) (*domain.ConfigEntry, error) {
	if err := validation.RequireNotBlank(errcode.ErrConfigInvalidInput,
		validation.F("key", input.Key),
	); err != nil {
		return nil, err
	}

	actor, err := actorFromContext(ctx)
	if err != nil {
		return nil, err
	}

	var updated *domain.ConfigEntry
	if err := s.runInTx(ctx, func(txCtx context.Context) error {
		var err error
		updated, err = s.repo.Update(txCtx, input.Key, input.Value)
		if err != nil {
			return fmt.Errorf("config-write: update: %w", err)
		}
		return s.publishUpserted(txCtx, updated, actor)
	}); err != nil {
		return nil, err
	}

	s.logger.Info("config entry updated", slog.String("key", updated.Key), slog.Int("version", updated.Version))
	return updated, nil
}

// Delete removes a config entry by key and publishes a change event.
func (s *Service) Delete(ctx context.Context, key string) error {
	if err := validation.RequireNotBlank(errcode.ErrConfigInvalidInput,
		validation.F("key", key),
	); err != nil {
		return err
	}

	actor, err := actorFromContext(ctx)
	if err != nil {
		return err
	}

	if err := s.runInTx(ctx, func(txCtx context.Context) error {
		deleted, err := s.repo.Delete(txCtx, key)
		if err != nil {
			return fmt.Errorf("config-write: delete: %w", err)
		}
		return s.publishDeleted(txCtx, deleted, actor)
	}); err != nil {
		return err
	}

	s.logger.Info("config entry deleted", slog.String("key", key))
	return nil
}

func (s *Service) runInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return s.txRunner.RunInTx(ctx, fn)
}

// actorFromContext extracts the admin actor from the request context.
// Config write paths are admin-only; an empty Subject is a wiring error.
func actorFromContext(ctx context.Context) (string, error) {
	p, ok := auth.FromContext(ctx)
	if !ok || p.Subject == "" {
		return "", errcode.New(errcode.ErrAuthUnauthorized, "config-write: actor required — admin auth must be present")
	}
	return p.Subject, nil
}

func (s *Service) publishUpserted(ctx context.Context, entry *domain.ConfigEntry, actor string) error {
	// Metadata-only: event carries key+version only.
	// Subscribers MUST refetch via GET /api/v1/config/{key} to obtain the value.
	// ref: NATS subject+bytes / Watermill payload-bytes boundary.
	return outbox.Emit(ctx, s.emitter, domain.TopicConfigEntryUpserted, configevents.EntryUpserted{
		Key:     entry.Key,
		Version: entry.Version,
		ActorID: actor,
	})
}

func (s *Service) publishDeleted(ctx context.Context, entry *domain.ConfigEntry, actor string) error {
	// Metadata-only: event carries key+version of the deleted entry.
	// Subscribers use version for monotonic tombstone protection against stale upsert replays.
	return outbox.Emit(ctx, s.emitter, domain.TopicConfigEntryDeleted, configevents.EntryDeleted{
		Key:     entry.Key,
		Version: entry.Version,
		ActorID: actor,
	})
}
