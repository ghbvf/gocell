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
	"github.com/ghbvf/gocell/pkg/query"
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

// WithOutboxWriter sets the outbox.Writer for transactional event publishing.
func WithOutboxWriter(w outbox.Writer) Option {
	return func(s *Service) { s.outboxWriter = w }
}

// WithTxManager sets the TxRunner for transactional guarantees (L2 atomicity).
func WithTxManager(tx persistence.TxRunner) Option {
	return func(s *Service) { s.txRunner = tx }
}

// WithRunMode sets the service run mode. RunModeDemo allows publisher-only
// failures to be logged and swallowed (matching the cell's demo-mode L2 relaxation
// for deployments that inject outbox.DiscardPublisher{}); RunModeProd — the zero
// value — keeps the service fail-closed so publisher errors propagate and L2
// atomicity is preserved.
//
// This unifies the publisher fail-open decision with pkg/query.RunMode: the cell
// translates kernel/cell.DurabilityMode → query.RunMode exactly once in Init()
// via query.RunModeForDemo and passes the resulting mode to every slice that
// needs it. Do not introduce slice-local bool flags that re-derive this signal.
// ref: zeromicro/go-zero ServiceConf.Mode — one mode field, propagated, not re-sniffed.
// ref: watermill/components/forwarder — publish failures always wrap+return.
func WithRunMode(mode query.RunMode) Option {
	return func(s *Service) { s.runMode = mode }
}

// Service implements config publish/rollback business logic.
type Service struct {
	repo         ports.ConfigRepository
	publisher    outbox.Publisher
	outboxWriter outbox.Writer
	txRunner     persistence.TxRunner
	logger       *slog.Logger
	runMode      query.RunMode
}

// NewService creates a config-publish Service. By default (RunModeProd zero
// value) publisher errors propagate to preserve L2 atomicity; demo assemblies
// opt-in to Warn+swallow behavior via WithRunMode(query.RunModeDemo).
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
		return nil, errcode.New(errcode.ErrConfigPublishInvalidInput, "key is required")
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
		Sensitive:   entry.Sensitive,
		PublishedAt: &now,
	}

	payload, err := json.Marshal(map[string]any{
		"action":    "published",
		"key":       key,
		"config_id": entry.ID,
		"version":   version.Version,
	})
	if err != nil {
		return nil, fmt.Errorf("config-publish: marshal event payload: %w", err)
	}

	if err := s.runInTx(ctx, func(txCtx context.Context) error {
		if err := s.repo.PublishVersion(txCtx, version); err != nil {
			return fmt.Errorf("config-publish: publish version: %w", err)
		}
		if err := s.publishEvent(txCtx, TopicConfigChanged, payload, key); err != nil {
			return err
		}
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
		return nil, errcode.New(errcode.ErrConfigPublishInvalidInput, "key is required")
	}
	if targetVersion < 1 {
		return nil, errcode.New(errcode.ErrConfigPublishInvalidInput,
			"rollback target version must be >= 1")
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
	// Restore the snapshot's sensitivity so a rolled-back entry inherits the
	// redaction policy that was in force at the snapshot time. Otherwise a
	// sensitivity flip between the target version and the live entry would
	// either leak a secret (entry was sensitive, snapshot was plain) or
	// over-redact a public value (snapshot was sensitive, entry is now plain).
	entry.Sensitive = ver.Sensitive
	entry.Version++
	entry.UpdatedAt = time.Now()

	payload, err := json.Marshal(map[string]any{
		"action":         "rollback",
		"key":            key,
		"target_version": targetVersion,
		"new_version":    entry.Version,
	})
	if err != nil {
		return nil, fmt.Errorf("config-publish: marshal event payload: %w", err)
	}

	if err := s.runInTx(ctx, func(txCtx context.Context) error {
		if err := s.repo.Update(txCtx, entry); err != nil {
			return fmt.Errorf("config-publish: rollback update: %w", err)
		}
		if err := s.publishEvent(txCtx, TopicConfigRollback, payload, key); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return nil, err
	}

	s.logger.Info("config rolled back",
		slog.String("key", key), slog.Int("target_version", targetVersion))
	return entry, nil
}

// runInTx executes fn in a transaction if txRunner is configured, otherwise
// calls fn(ctx) directly. Nil txRunner is intentional for query-only slices;
// Cell Init() validates txRunner presence for CUD slices before Start().
func (s *Service) runInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if s.txRunner != nil {
		return s.txRunner.RunInTx(ctx, fn)
	}
	return fn(ctx)
}

func (s *Service) publishEvent(ctx context.Context, topic string, payload []byte, key string) error {
	if s.outboxWriter != nil {
		entry := outbox.Entry{
			ID:        "evt" + "-" + uuid.NewString(),
			EventType: topic,
			Payload:   payload,
		}
		if err := s.outboxWriter.Write(ctx, entry); err != nil {
			return fmt.Errorf("config-publish: write outbox entry: %w", err)
		}
		return nil
	}
	// Publisher-only path (no outbox). Fail-closed by default to keep L2
	// atomicity honest; demo assemblies opt-in via WithRunMode(query.RunModeDemo).
	if err := s.publisher.Publish(ctx, topic, payload); err != nil {
		if s.runMode.IsDemo() {
			s.logger.Warn("config-publish: publisher failed (demo fail-open)",
				slog.Any("error", err), slog.String("key", key), slog.String("topic", topic))
			return nil
		}
		return fmt.Errorf("config-publish: publisher failed for topic %s: %w", topic, err)
	}
	return nil
}
