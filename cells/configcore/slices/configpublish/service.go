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

// Option configures a config-publish Service.
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

// Service implements config publish/rollback business logic.
type Service struct {
	repo     ports.ConfigRepository
	txRunner persistence.TxRunner
	emitter  outbox.Emitter
	logger   *slog.Logger
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
	if err := validation.RequireNotBlank(errcode.ErrConfigPublishInvalidInput,
		validation.F("key", key),
	); err != nil {
		return nil, err
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

		if err := s.repo.PublishVersion(txCtx, version); err != nil {
			return fmt.Errorf("config-publish: publish version: %w", err)
		}
		return outbox.Emit(txCtx, s.emitter, domain.TopicConfigVersionPublished, domain.ConfigVersionPublishedEvent{
			Key:      key,
			ConfigID: entry.ID,
			Version:  version.Version,
		})
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
	if err := validation.RequireNotBlank(errcode.ErrConfigPublishInvalidInput,
		validation.F("key", key),
	); err != nil {
		return nil, err
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

		// Metadata-only: event carries key+version only.
		// Subscribers MUST refetch via GET /api/v1/config/{key} to obtain the value.
		// ref: NATS subject+bytes / Watermill payload-bytes boundary.
		if err := outbox.Emit(txCtx, s.emitter, domain.TopicConfigEntryUpserted, domain.ConfigEntryUpsertedEvent{
			Key:     key,
			Version: updated.Version,
		}); err != nil {
			return err
		}

		return outbox.Emit(txCtx, s.emitter, domain.TopicConfigRollback, domain.ConfigRollbackEvent{
			Key:           key,
			TargetVersion: targetVersion,
			NewVersion:    updated.Version,
		})
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
