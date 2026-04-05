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
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/uid"
)

const (
	// TopicConfigChanged is the event topic for config changes.
	TopicConfigChanged = "event.config.changed.v1"
	// TopicConfigRollback is the event topic for config rollbacks.
	TopicConfigRollback = "event.config.rollback.v1"
	// ErrPublishInvalidInput indicates invalid input for a publish operation.
	ErrPublishInvalidInput errcode.Code = "ERR_CONFIG_PUBLISH_INVALID_INPUT"
)

// Service implements config publish/rollback business logic.
type Service struct {
	repo      ports.ConfigRepository
	publisher outbox.Publisher
	logger    *slog.Logger
}

// NewService creates a config-publish Service.
func NewService(repo ports.ConfigRepository, pub outbox.Publisher, logger *slog.Logger) *Service {
	return &Service{
		repo:      repo,
		publisher: pub,
		logger:    logger,
	}
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
		ID:          uid.NewWithPrefix("ver"),
		ConfigID:    entry.ID,
		Version:     entry.Version,
		Value:       entry.Value,
		PublishedAt: &now,
	}

	if err := s.repo.PublishVersion(ctx, version); err != nil {
		return nil, fmt.Errorf("config-publish: publish version: %w", err)
	}

	payload, _ := json.Marshal(map[string]any{
		"action":    "published",
		"key":       key,
		"config_id": entry.ID,
		"version":   version.Version,
	})
	if err := s.publisher.Publish(ctx, TopicConfigChanged, payload); err != nil {
		s.logger.Error("config-publish: failed to publish event",
			slog.Any("error", err), slog.String("key", key))
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

	if err := s.repo.Update(ctx, entry); err != nil {
		return nil, fmt.Errorf("config-publish: rollback update: %w", err)
	}

	payload, _ := json.Marshal(map[string]any{
		"action":         "rollback",
		"key":            key,
		"target_version": targetVersion,
		"new_version":    entry.Version,
	})
	if err := s.publisher.Publish(ctx, TopicConfigRollback, payload); err != nil {
		s.logger.Error("config-publish: failed to publish rollback event",
			slog.Any("error", err), slog.String("key", key))
	}

	s.logger.Info("config rolled back",
		slog.String("key", key), slog.Int("target_version", targetVersion))
	return entry, nil
}
