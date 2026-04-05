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
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/id"
)

const (
	// TopicConfigChanged is the event topic for config changes.
	TopicConfigChanged = "event.config.changed.v1"
	// ErrConfigInvalidInput indicates invalid input for a config operation.
	ErrConfigInvalidInput errcode.Code = "ERR_CONFIG_INVALID_INPUT"
)

// Option configures a config-write Service.
type Option func(*Service)

// WithOutboxWriter sets the outbox.Writer for transactional event publishing.
func WithOutboxWriter(w outbox.Writer) Option {
	return func(s *Service) { s.outboxWriter = w }
}

// Service implements config write business logic.
type Service struct {
	repo         ports.ConfigRepository
	publisher    outbox.Publisher
	outboxWriter outbox.Writer
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
		return nil, errcode.New(ErrConfigInvalidInput, "key is required")
	}

	now := time.Now()
	entry := &domain.ConfigEntry{
		ID:        id.New("cfg"),
		Key:       input.Key,
		Value:     input.Value,
		Version:   1,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.repo.Create(ctx, entry); err != nil {
		return nil, fmt.Errorf("config-write: create: %w", err)
	}

	s.publishChange(ctx, "created", entry)
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
		return nil, errcode.New(ErrConfigInvalidInput, "key is required")
	}

	entry, err := s.repo.GetByKey(ctx, input.Key)
	if err != nil {
		return nil, fmt.Errorf("config-write: update: %w", err)
	}

	entry.Value = input.Value
	entry.Version++
	entry.UpdatedAt = time.Now()

	if err := s.repo.Update(ctx, entry); err != nil {
		return nil, fmt.Errorf("config-write: update: %w", err)
	}

	s.publishChange(ctx, "updated", entry)
	s.logger.Info("config entry updated", slog.String("key", entry.Key), slog.Int("version", entry.Version))
	return entry, nil
}

// Delete removes a config entry by key and publishes a change event.
func (s *Service) Delete(ctx context.Context, key string) error {
	if key == "" {
		return errcode.New(ErrConfigInvalidInput, "key is required")
	}

	entry, err := s.repo.GetByKey(ctx, key)
	if err != nil {
		return fmt.Errorf("config-write: delete: %w", err)
	}

	if err := s.repo.Delete(ctx, key); err != nil {
		return fmt.Errorf("config-write: delete: %w", err)
	}

	s.publishChange(ctx, "deleted", entry)
	s.logger.Info("config entry deleted", slog.String("key", key))
	return nil
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
			ID:        id.New("evt"),
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
