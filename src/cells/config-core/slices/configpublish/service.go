// Package configpublish implements the config-publish slice: Publish/Rollback
// versioned config snapshots.
package configpublish

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
	// TopicConfigRollback is the event topic for config rollbacks.
	TopicConfigRollback = "event.config.rollback.v1"
	// ErrPublishInvalidInput indicates invalid input for a publish operation.
	ErrPublishInvalidInput errcode.Code = "ERR_CONFIG_PUBLISH_INVALID_INPUT"
)

// Option configures a config-publish Service.
type Option func(*Service)

// WithOutboxWriter sets the outbox.Writer for transactional event publishing.
func WithOutboxWriter(w outbox.Writer) Option {
	return func(s *Service) { s.outboxWriter = w }
}

// WithTxManager sets the TxRunner for transactional guarantees (L2 atomicity).
func WithTxManager(tx persistence.TxRunner) Option {
	return func(s *Service) { s.txRunner = tx }
}

// Service implements config publish/rollback business logic.
type Service struct {
	repo         ports.ConfigRepository
	publisher    outbox.Publisher
	outboxWriter outbox.Writer
	txRunner     persistence.TxRunner
	logger       *slog.Logger
}

// NewService creates a config-publish Service.
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

// Publish creates a versioned snapshot of a config entry.
func (s *Service) Publish(ctx context.Context, key string) (*domain.ConfigVersion, error) {
	if key == "" {
		return nil, errcode.New(ErrPublishInvalidInput, "key is required")
	}

	entry, err := s.repo.GetByKey(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("config-publish: publish: %w", err)
	}

	now := time.Now()
	version := &domain.ConfigVersion{
		ID:          "ver" + "-" + uuid.NewString(),
		ConfigID:    entry.ID,
		Version:     entry.Version,
		Value:       entry.Value,
		PublishedAt: &now,
	}

	payload, _ := json.Marshal(map[string]any{
		"action":    "published",
		"key":       key,
		"config_id": entry.ID,
		"version":   version.Version,
	})

	if err := s.runInTx(ctx, func(txCtx context.Context) error {
		if err := s.repo.PublishVersion(txCtx, version); err != nil {
			return fmt.Errorf("config-publish: publish version: %w", err)
		}
		s.publishEvent(txCtx, TopicConfigChanged, payload, key)
		return nil
	}); err != nil {
		return nil, err
	}

	s.logger.Info("config version published",
		slog.String("key", key), slog.Int("version", version.Version))
	return version, nil
}

// Rollback reverts a config entry to a specific version.
func (s *Service) Rollback(ctx context.Context, key string, targetVersion int) (*domain.ConfigEntry, error) {
	if key == "" {
		return nil, errcode.New(ErrPublishInvalidInput, "key is required")
	}

	entry, err := s.repo.GetByKey(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("config-publish: rollback: %w", err)
	}

	ver, err := s.repo.GetVersion(ctx, entry.ID, targetVersion)
	if err != nil {
		return nil, fmt.Errorf("config-publish: rollback: version not found: %w", err)
	}

	entry.Value = ver.Value
	entry.Version++
	entry.UpdatedAt = time.Now()

	payload, _ := json.Marshal(map[string]any{
		"action":         "rollback",
		"key":            key,
		"target_version": targetVersion,
		"new_version":    entry.Version,
	})

	if err := s.runInTx(ctx, func(txCtx context.Context) error {
		if err := s.repo.Update(txCtx, entry); err != nil {
			return fmt.Errorf("config-publish: rollback update: %w", err)
		}
		s.publishEvent(txCtx, TopicConfigRollback, payload, key)
		return nil
	}); err != nil {
		return nil, err
	}

	s.logger.Info("config rolled back",
		slog.String("key", key), slog.Int("target_version", targetVersion))
	return entry, nil
}

// runInTx executes fn in a transaction if txRunner is configured, otherwise
// executes directly.
func (s *Service) runInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if s.txRunner != nil {
		return s.txRunner.RunInTx(ctx, fn)
	}
	return fn(ctx)
}

func (s *Service) publishEvent(ctx context.Context, topic string, payload []byte, key string) {
	if s.outboxWriter != nil {
		entry := outbox.Entry{
			ID:        "evt" + "-" + uuid.NewString(),
			EventType: topic,
			Payload:   payload,
		}
		if err := s.outboxWriter.Write(ctx, entry); err != nil {
			s.logger.Error("config-publish: failed to write outbox entry",
				slog.Any("error", err), slog.String("key", key))
		}
		return
	}
	if err := s.publisher.Publish(ctx, topic, payload); err != nil {
		s.logger.Error("config-publish: failed to publish event",
			slog.Any("error", err), slog.String("key", key))
	}
}
