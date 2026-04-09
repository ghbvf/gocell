// Package configwrite implements the config-write slice: Create/Update/Delete
// config entries with event publishing.
package configwrite

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

const (
	// TopicConfigChanged is the event topic for config changes.
	TopicConfigChanged = "event.config.changed.v1"
)

// Option configures a config-write Service.
type Option func(*Service)

// WithOutboxWriter sets the outbox.Writer for transactional event publishing.
func WithOutboxWriter(w outbox.Writer) Option {
	return func(s *Service) { s.outboxWriter = w }
}

// WithTxManager sets the TxRunner for transactional guarantees (L2 atomicity).
func WithTxManager(tx persistence.TxRunner) Option {
	return func(s *Service) { s.txRunner = tx }
}

// Service implements config write business logic.
type Service struct {
	repo         ports.ConfigRepository
	publisher    outbox.Publisher
	outboxWriter outbox.Writer
	txRunner     persistence.TxRunner
	logger       *slog.Logger
}

// NewService creates a config-write Service.
func NewService(repo ports.ConfigRepository, pub outbox.Publisher, logger *slog.Logger, opts ...Option) *Service {
	s := &Service{
		repo:      repo,
		publisher: pub,
		logger:    logger,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// CreateInput holds parameters for creating a config entry.
type CreateInput struct {
	Key   string
	Value string
}

// Create creates a new config entry and publishes a change event.
func (s *Service) Create(ctx context.Context, input CreateInput) (*domain.ConfigEntry, error) {
	if input.Key == "" {
		return nil, errcode.New(errcode.ErrConfigInvalidInput, "key is required")
	}

	now := time.Now()
	entry := &domain.ConfigEntry{
		ID:        "cfg" + "-" + uuid.NewString(),
		Key:       input.Key,
		Value:     input.Value,
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.runInTx(ctx, func(txCtx context.Context) error {
		if err := s.repo.Create(txCtx, entry); err != nil {
			return fmt.Errorf("config-write: create: %w", err)
		}
		s.publishChange(txCtx, "created", entry)
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
func (s *Service) Update(ctx context.Context, input UpdateInput) (*domain.ConfigEntry, error) {
	if input.Key == "" {
		return nil, errcode.New(errcode.ErrConfigInvalidInput, "key is required")
	}

	entry, err := s.repo.GetByKey(ctx, input.Key)
	if err != nil {
		return nil, fmt.Errorf("config-write: update: %w", err)
	}

	entry.Value = input.Value
	entry.Version++
	entry.UpdatedAt = time.Now()

	if err := s.runInTx(ctx, func(txCtx context.Context) error {
		if err := s.repo.Update(txCtx, entry); err != nil {
			return fmt.Errorf("config-write: update: %w", err)
		}
		s.publishChange(txCtx, "updated", entry)
		return nil
	}); err != nil {
		return nil, err
	}

	s.logger.Info("config entry updated", slog.String("key", entry.Key), slog.Int("version", entry.Version))
	return entry, nil
}

// Delete removes a config entry by key and publishes a change event.
func (s *Service) Delete(ctx context.Context, key string) error {
	if key == "" {
		return errcode.New(errcode.ErrConfigInvalidInput, "key is required")
	}

	entry, err := s.repo.GetByKey(ctx, key)
	if err != nil {
		return fmt.Errorf("config-write: delete: %w", err)
	}

	if err := s.runInTx(ctx, func(txCtx context.Context) error {
		if err := s.repo.Delete(txCtx, key); err != nil {
			return fmt.Errorf("config-write: delete: %w", err)
		}
		s.publishChange(txCtx, "deleted", entry)
		return nil
	}); err != nil {
		return err
	}

	s.logger.Info("config entry deleted", slog.String("key", key))
	return nil
}

// runInTx executes fn in a transaction if txRunner is configured, otherwise
// executes directly.
func (s *Service) runInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if s.txRunner != nil {
		return s.txRunner.RunInTx(ctx, fn)
	}
	return fn(ctx)
}

func (s *Service) publishChange(ctx context.Context, action string, entry *domain.ConfigEntry) {
	payload, _ := json.Marshal(map[string]any{
		"action":  action,
		"key":     entry.Key,
		"value":   entry.Value,
		"version": entry.Version,
	})
	if s.outboxWriter != nil {
		outboxEntry := outbox.Entry{
			ID:        "evt" + "-" + uuid.NewString(),
			EventType: TopicConfigChanged,
			Payload:   payload,
		}
		if err := s.outboxWriter.Write(ctx, outboxEntry); err != nil {
			s.logger.Error("config-write: failed to write outbox entry",
				slog.Any("error", err),
				slog.String("key", entry.Key),
			)
		}
		return
	}
	if err := s.publisher.Publish(ctx, TopicConfigChanged, payload); err != nil {
		s.logger.Error("config-write: failed to publish event",
			slog.Any("error", err),
			slog.String("key", entry.Key),
		)
	}
}
