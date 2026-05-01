// Package configreceive implements the config-receive slice: consumes
// config state-sync events from configcore. Currently logs changes for
// observability; future use: refresh JWT TTL, key rotation intervals, etc.
package configreceive

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
)

const (
	// TopicConfigEntryUpserted is the config state-sync topic consumed by this slice.
	TopicConfigEntryUpserted = "event.config.entry-upserted.v1"
	// TopicConfigEntryDeleted is the config delete state-sync topic consumed by this slice.
	TopicConfigEntryDeleted = "event.config.entry-deleted.v1"
)

// Service consumes config change events for accesscore.
//
// NOTE: HandleEntryUpserted/HandleEntryDeleted are currently observability-only
// (logs only). Real consumers (JWT TTL refresh, key rotation interval) will land
// in a follow-up; the current subscription is a placeholder per ADV-05
// (active event must have subscribers).
//
// Consumer: cg-accesscore-config-events
// Idempotency: log-only (no side effects), inherently idempotent
// Disposition: Ack on success / Reject on permanent unmarshal or semantic error
// DLX: broker-native via DispositionReject → Nack(requeue=false).
type Service struct {
	logger               *slog.Logger
	configGetter         ports.ConfigGetter // optional; nil disables GetEntry fetch
	configEventCollector obmetrics.ConfigEventCollector
}

// Option configures a configreceive Service.
type Option func(*Service)

// WithConfigGetter injects the ConfigGetter used to fetch the current config
// entry value after an upsert event. When nil or not provided the service
// operates in log-only mode (no cross-cell HTTP call is made).
func WithConfigGetter(c ports.ConfigGetter) Option {
	return func(s *Service) { s.configGetter = c }
}

// WithConfigEventCollector injects config event process metrics.
func WithConfigEventCollector(c obmetrics.ConfigEventCollector) Option {
	return func(s *Service) {
		if c == nil {
			c = obmetrics.NoopConfigEventCollector{}
		}
		s.configEventCollector = c
	}
}

// NewService creates a config-receive Service.
func NewService(logger *slog.Logger, opts ...Option) *Service {
	s := &Service{
		logger:               logger,
		configEventCollector: obmetrics.NoopConfigEventCollector{},
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// HandleEntryUpserted processes an event.config.entry-upserted.v1 event.
// When a ConfigGetter is configured it fetches the current entry value from
// configcore (contract: http.config.internal.get.v1) and logs it. Fetch
// failures are retriable: a transient error is returned (triggering Requeue)
// so the consumer pipeline retries. A 404 (entry truly gone) is treated as
// a stale event: log Warn and Ack (retry cannot help).
func (s *Service) HandleEntryUpserted(ctx context.Context, entry outbox.Entry) error {
	event, err := dto.DecodeEntryUpserted(entry.Payload)
	if err != nil {
		s.logger.Error("config-receive: failed to unmarshal entry-upserted event, routing to dead letter",
			slog.Any("error", err), slog.String("entry_id", entry.ID))
		s.recordConfigEventProcess(ctx, obmetrics.ConfigEventProcessReasonPermanentError)
		return outbox.NewPermanentError(fmt.Errorf("config-receive: unmarshal entry-upserted payload: %w", err))
	}

	s.logger.Debug("config-receive: config upserted",
		slog.String("key", event.Key),
		slog.Int("version", event.Version))

	if s.configGetter != nil {
		cfg, fetchErr := s.configGetter.GetEntry(ctx, event.Key)
		if fetchErr != nil {
			// If the config entry is genuinely gone (404), the event is stale;
			// retrying won't help, so log at Warn and Ack.
			if errcode.IsDomainNotFound(fetchErr, errcode.ErrConfigNotFound, errcode.ErrConfigRepoNotFound) {
				s.logger.Warn("config-receive: config entry not found after upsert (stale event), skipping",
					slog.Any("error", fetchErr),
					slog.String("key", event.Key),
					slog.Int("version", event.Version))
				s.recordConfigEventProcess(ctx, obmetrics.ConfigEventProcessReasonStale)
				return nil
			}
			// Transient failure — return error so the legacy handler wrapper
			// triggers Requeue and the consumer pipeline retries.
			s.logger.Error("config-receive: failed to fetch config entry after upsert",
				slog.Any("error", fetchErr),
				slog.String("key", event.Key),
				slog.Int("version", event.Version))
			return fetchErr
		}
		s.logger.Info("config-receive: fetched config entry",
			slog.String("key", cfg.Key),
			slog.Int("version", cfg.Version),
			slog.Bool("sensitive", cfg.Sensitive))
	}

	s.recordConfigEventProcess(ctx, obmetrics.ConfigEventProcessReasonAck)
	return nil
}

// HandleEntryDeleted processes an event.config.entry-deleted.v1 event.
func (s *Service) HandleEntryDeleted(ctx context.Context, entry outbox.Entry) error {
	event, err := dto.DecodeEntryDeleted(entry.Payload)
	if err != nil {
		s.logger.Error("config-receive: failed to unmarshal entry-deleted event, routing to dead letter",
			slog.Any("error", err), slog.String("entry_id", entry.ID))
		s.recordConfigEventProcess(ctx, obmetrics.ConfigEventProcessReasonPermanentError)
		return outbox.NewPermanentError(fmt.Errorf("config-receive: unmarshal entry-deleted payload: %w", err))
	}

	s.logger.Debug("config-receive: config deleted",
		slog.String("key", event.Key),
		slog.Int("version", event.Version))
	s.recordConfigEventProcess(ctx, obmetrics.ConfigEventProcessReasonAck)
	return nil
}

func (s *Service) recordConfigEventProcess(ctx context.Context, reason obmetrics.ConfigEventProcessReason) {
	if s.configEventCollector == nil {
		return
	}
	obmetrics.RecordConfigEventProcess(ctx, s.configEventCollector, reason)
}
