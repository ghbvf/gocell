// Package configpublish implements the config-publish slice: Publish/Rollback
// versioned config snapshots.
//
// All reads and writes happen inside runInTx to eliminate the TOCTOU stale-read
// race that existed when GetByKey/GetVersion were called before the transaction.
//
// ref: flagwrite — same "all-inside-tx" pattern.
package configpublish

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
	"github.com/google/uuid"
)

// Re-exported from domain for backward compatibility within this package's
// tests and callers.
const (
	TopicConfigChanged  = domain.TopicConfigChanged
	TopicConfigRollback = domain.TopicConfigRollback
)

// Option configures a config-publish Service.
type Option func(*Service)

func directPublishMode(mode PublishFailureMode) outbox.DirectPublishFailureMode {
	if mode.IsFailOpen() {
		return outbox.DirectPublishFailOpen
	}
	return outbox.DirectPublishFailClosed
}

// WithEmitter sets the event emitter.
func WithEmitter(e outbox.Emitter) Option {
	return func(s *Service) {
		if e != nil {
			s.emitter = e
		}
	}
}

// WithOutboxWriter adapts an outbox.Writer for existing tests and wiring.
func WithOutboxWriter(w outbox.Writer) Option {
	return func(s *Service) {
		if e, err := outbox.NewWriterEmitter(w); err == nil {
			s.emitter = e
		}
	}
}

// WithTxManager sets the TxRunner for transactional guarantees (L2 atomicity).
func WithTxManager(tx persistence.TxRunner) Option {
	return func(s *Service) { s.txRunner = persistence.RunnerOrNoop(tx) }
}

// WithPublishFailureMode records direct-publisher failure intent for Cell-level
// emitter construction. Zero value is fail-closed (safe production default).
//
// S10 MODE-SEMANTIC-SPLIT-01: separated from query.RunMode so read-path cursor
// tolerance and write-path publisher failure semantics evolve independently.
func WithPublishFailureMode(mode PublishFailureMode) Option {
	return func(s *Service) { s.publishFailureMode = mode }
}

// Service implements config publish/rollback business logic.
type Service struct {
	repo               ports.ConfigRepository
	txRunner           persistence.TxRunner
	emitter            outbox.Emitter
	publishFailureMode PublishFailureMode
	logger             *slog.Logger
}

// NewService creates a config-publish Service.
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

// Publish creates a versioned snapshot of a config entry.
// All reads happen inside runInTx so the snapshot is consistent with the write.
func (s *Service) Publish(ctx context.Context, key string) (*domain.ConfigVersion, error) {
	if key == "" {
		return nil, errcode.New(errcode.ErrConfigPublishInvalidInput, "key is required")
	}

	var version *domain.ConfigVersion
	if err := s.runInTx(ctx, func(txCtx context.Context) error {
		entry, err := s.repo.GetByKey(txCtx, key)
		if err != nil {
			return fmt.Errorf("config-publish: publish: %w", err)
		}

		now := time.Now()
		version = &domain.ConfigVersion{
			ID:          "ver" + "-" + uuid.NewString(),
			ConfigID:    entry.ID,
			Version:     entry.Version,
			Value:       entry.Value,
			Sensitive:   entry.Sensitive,
			PublishedAt: &now,
		}

		payload, err := json.Marshal(map[string]any{
			"action":    "published",
			"key":       key,
			"config_id": entry.ID,
			"version":   version.Version,
			"sensitive": entry.Sensitive,
		})
		if err != nil {
			return fmt.Errorf("config-publish: marshal event payload: %w", err)
		}

		if err := s.repo.PublishVersion(txCtx, version); err != nil {
			return fmt.Errorf("config-publish: publish version: %w", err)
		}
		return s.publishEvent(txCtx, TopicConfigChanged, payload)
	}); err != nil {
		return nil, err
	}

	s.logger.Info("config version published",
		slog.String("key", key), slog.Int("version", version.Version))
	return version, nil
}

// Rollback reverts a config entry to a specific version.
// All reads (GetByKey + GetVersion) and the atomic Update happen inside runInTx
// to eliminate the TOCTOU stale-read race where a concurrent write could change
// the entry between the reads and the update.
func (s *Service) Rollback(ctx context.Context, key string, targetVersion int) (*domain.ConfigEntry, error) {
	if key == "" {
		return nil, errcode.New(errcode.ErrConfigPublishInvalidInput, "key is required")
	}
	if targetVersion < 1 {
		return nil, errcode.New(errcode.ErrConfigPublishInvalidInput,
			"rollback target version must be >= 1")
	}

	var updated *domain.ConfigEntry
	if err := s.runInTx(ctx, func(txCtx context.Context) error {
		entry, err := s.repo.GetByKey(txCtx, key)
		if err != nil {
			return fmt.Errorf("config-publish: rollback: %w", err)
		}

		ver, err := s.repo.GetVersion(txCtx, entry.ID, targetVersion)
		if err != nil {
			return fmt.Errorf("config-publish: rollback: version not found: %w", err)
		}

		// Atomic UPDATE...RETURNING restores the snapshot's value and sensitivity.
		// The repo handles version=version+1 and updated_at=now() internally.
		updated, err = s.repo.UpdateForRollback(txCtx, key, ver.Value, ver.Sensitive)
		if err != nil {
			return fmt.Errorf("config-publish: rollback update: %w", err)
		}

		payload, err := json.Marshal(map[string]any{
			"action":         "rollback",
			"key":            key,
			"target_version": targetVersion,
			"new_version":    updated.Version,
		})
		if err != nil {
			return fmt.Errorf("config-publish: marshal event payload: %w", err)
		}
		return s.publishEvent(txCtx, TopicConfigRollback, payload)
	}); err != nil {
		return nil, err
	}

	s.logger.Info("config rolled back",
		slog.String("key", key), slog.Int("target_version", targetVersion))
	return updated, nil
}

func (s *Service) runInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return s.txRunner.RunInTx(ctx, fn)
}

// publishEvent emits the config event through the injected emitter.
func (s *Service) publishEvent(ctx context.Context, topic string, payload []byte) error {
	entry := outbox.Entry{
		ID:        outbox.NewEntryID(),
		EventType: topic,
		Payload:   payload,
	}
	if err := s.emitter.Emit(ctx, entry); err != nil {
		return fmt.Errorf("config-publish: emit event for topic %s: %w", topic, err)
	}
	return nil
}
