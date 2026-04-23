// Package configwrite implements the config-write slice: Create/Update/Delete
// config entries with event publishing.
package configwrite

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/configcore/internal/ports"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/google/uuid"
)

// TopicConfigChanged is re-exported from domain for backward compatibility
// within this package's tests and callers.
const TopicConfigChanged = domain.TopicConfigChanged

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
}

// NewService creates a config-write Service.
func NewService(repo ports.ConfigRepository, logger *slog.Logger, opts ...Option) *Service {
	s := &Service{
		repo:     repo,
		txRunner: persistence.NoopTxRunner{},
		emitter:  outbox.NewNoopEmitter(),
		logger:   logger,
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

	now := time.Now()
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
		if err := s.publishChange(txCtx, "created", entry); err != nil {
			return err
		}
		return nil
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

	var updated *domain.ConfigEntry
	if err := s.runInTx(ctx, func(txCtx context.Context) error {
		var err error
		updated, err = s.repo.Update(txCtx, input.Key, input.Value)
		if err != nil {
			return fmt.Errorf("config-write: update: %w", err)
		}
		return s.publishChange(txCtx, "updated", updated)
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

	if err := s.runInTx(ctx, func(txCtx context.Context) error {
		deleted, err := s.repo.Delete(txCtx, key)
		if err != nil {
			return fmt.Errorf("config-write: delete: %w", err)
		}
		if err := s.publishChange(txCtx, "deleted", deleted); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}

	s.logger.Info("config entry deleted", slog.String("key", key))
	return nil
}

func (s *Service) runInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return s.txRunner.RunInTx(ctx, fn)
}

func (s *Service) publishChange(ctx context.Context, action string, entry *domain.ConfigEntry) error {
	eventValue := entry.Value
	if entry.Sensitive {
		eventValue = "******"
	}
	payload, err := json.Marshal(map[string]any{
		"action":  action,
		"key":     entry.Key,
		"value":   eventValue,
		"version": entry.Version,
	})
	if err != nil {
		return fmt.Errorf("config-write: marshal event payload: %w", err)
	}
	outboxEntry := outbox.Entry{
		ID:        outbox.NewEntryID(),
		EventType: TopicConfigChanged,
		Payload:   payload,
	}
	if err := s.emitter.Emit(ctx, outboxEntry); err != nil {
		return fmt.Errorf("config-write: emit event: %w", err)
	}
	return nil
}
