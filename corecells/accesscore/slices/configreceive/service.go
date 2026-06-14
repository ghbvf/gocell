// Package configreceive implements the config-receive slice: consumes
// config state-sync events from configcore. When a ConfigGetter is wired it
// refetches the current entry value from configcore using the real tenant
// derived from the consumer context — ensuring the lookup is scoped to the
// caller's tenant tier rather than the legacy system tier.
package configreceive

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	obmetrics "github.com/ghbvf/gocell/framework/runtime/observability/metrics"
)

const (
	// TopicConfigEntryUpserted is the config state-sync topic consumed by this slice.
	TopicConfigEntryUpserted = "event.config.entry-upserted.v1"
	// TopicConfigEntryDeleted is the config delete state-sync topic consumed by this slice.
	TopicConfigEntryDeleted = "event.config.entry-deleted.v1"
)

// Service consumes config change events for accesscore.
//
// When a ConfigGetter is wired, HandleEntryUpserted refetches the current
// entry value from configcore using the real tenant derived from the consumer
// context (restored by SubscriberWithMiddleware from the outbox principal
// envelope). The refetch is observability/log-only for now; future consumers
// (JWT TTL refresh, key rotation interval) will read the fetched value.
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
// When a ConfigGetter is configured it derives the real tenant from ctx via
// tenant.FromContext (populated by SubscriberWithMiddleware from the outbox
// principal envelope) and fetches the current entry value from configcore
// (contract: http.config.internal.get.v1) scoped to that tenant tier.
//
// If no tenant is in ctx the event is Rejected to DLQ (PermanentError): a
// config event reaching this consumer without a tenant is an envelope/restore
// pipeline violation of this PR's tenant-correct invariant (#1577), not a
// safely-consumable event — fail-closed surfaces it for investigation instead
// of silently Acking (codex review F2).
//
// Fetch failures are retriable: a transient Requeue is returned so the
// consumer pipeline retries. A 404 (entry genuinely absent in the tenant
// tier) is treated as a stale event: log Warn and Ack (retry cannot help).
func (s *Service) HandleEntryUpserted(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
	event, err := dto.DecodeEntryUpserted(entry.Payload())
	if err != nil {
		s.logger.Error("config-receive: failed to unmarshal entry-upserted event, routing to dead letter",
			slog.Any("error", err), slog.String("entry_id", entry.ID()))
		s.recordConfigEventProcess(ctx, obmetrics.ConfigEventProcessReasonPermanentError)
		return outbox.Reject(outbox.NewPermanentError(fmt.Errorf("config-receive: unmarshal entry-upserted payload: %w", err)))
	}

	s.logger.Debug("config-receive: config upserted",
		slog.String("key", event.Key),
		slog.Int("version", event.Version))

	if s.configGetter != nil {
		t, terr := tenant.FromContext(ctx)
		if terr != nil {
			// Fail-closed: a config event without a tenant in ctx is an
			// envelope/restore pipeline violation of the tenant-correct
			// invariant (#1577), not a safely-consumable stale event. Reject to
			// DLQ so operators investigate, rather than silently Acking.
			s.logger.Error("config-receive: no tenant in event envelope, cannot scope refetch; routing to DLQ",
				slog.Any("error", terr), slog.String("key", event.Key), slog.String("entry_id", entry.ID()))
			s.recordConfigEventProcess(ctx, obmetrics.ConfigEventProcessReasonNoTenant)
			return outbox.Reject(outbox.NewPermanentError(
				fmt.Errorf("config-receive: missing tenant in event envelope, cannot scope refetch: %w", terr)))
		}

		cfg, fetchErr := s.configGetter.GetEntry(ctx, t, event.Key)
		if fetchErr != nil {
			// A 404 means the key is genuinely absent in this tenant's tier
			// (truly stale event). Retrying cannot help.
			if errcode.IsDomainNotFound(fetchErr, errcode.ErrConfigRepoNotFound) {
				s.logger.Warn("config-receive: config entry not found (stale event), skipping",
					slog.Any("error", fetchErr),
					slog.String("key", event.Key),
					slog.Int("version", event.Version))
				s.recordConfigEventProcess(ctx, obmetrics.ConfigEventProcessReasonStale)
				return outbox.Ack()
			}
			// 401/403 auth/authz failures and 400 bad-request (invalid
			// X-Tenant-ID) are permanent: retrying with the same credentials
			// or broken context cannot recover. Route to DLQ via Reject +
			// PermanentError so operators can investigate configuration drift
			// instead of silently consuming retry budget.
			if isPermanentRefetchError(fetchErr) {
				s.logger.Error("config-receive: permanent auth/validation failure fetching config entry, routing to DLQ",
					slog.Any("error", fetchErr),
					slog.String("key", event.Key),
					slog.Int("version", event.Version),
					slog.String("entry_id", entry.ID()))
				s.recordConfigEventProcess(ctx, obmetrics.ConfigEventProcessReasonPermanentError)
				return outbox.Reject(outbox.NewPermanentError(fetchErr))
			}
			// Transient failure — Requeue so the consumer pipeline retries.
			s.logger.Error("config-receive: failed to fetch config entry after upsert",
				slog.Any("error", fetchErr),
				slog.String("key", event.Key),
				slog.Int("version", event.Version),
				slog.String("entry_id", entry.ID()))
			s.recordConfigEventProcess(ctx, obmetrics.ConfigEventProcessReasonTransient)
			return outbox.Requeue(fetchErr)
		}
		s.logger.Info("config-receive: fetched config entry",
			slog.String("key", cfg.Key),
			slog.Int("version", cfg.Version),
			slog.Bool("sensitive", cfg.Sensitive))
	}

	s.recordConfigEventProcess(ctx, obmetrics.ConfigEventProcessReasonAck)
	return outbox.Ack()
}

// HandleEntryDeleted processes an event.config.entry-deleted.v1 event.
func (s *Service) HandleEntryDeleted(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
	event, err := dto.DecodeEntryDeleted(entry.Payload())
	if err != nil {
		s.logger.Error("config-receive: failed to unmarshal entry-deleted event, routing to dead letter",
			slog.Any("error", err), slog.String("entry_id", entry.ID()))
		s.recordConfigEventProcess(ctx, obmetrics.ConfigEventProcessReasonPermanentError)
		return outbox.Reject(outbox.NewPermanentError(fmt.Errorf("config-receive: unmarshal entry-deleted payload: %w", err)))
	}

	s.logger.Debug("config-receive: config deleted",
		slog.String("key", event.Key),
		slog.Int("version", event.Version))
	s.recordConfigEventProcess(ctx, obmetrics.ConfigEventProcessReasonAck)
	return outbox.Ack()
}

// isPermanentRefetchError reports whether err is a permanent client error
// that retrying cannot recover: 401/403 auth/authz failures from configcore
// (invalid service token or caller_cell not in contract.clients allowlist),
// or a 400 bad-request (absent, malformed, or nil-UUID X-Tenant-ID header)
// which signals a permanent misconfiguration in the consumer pipeline.
// Such failures must Reject (DLQ) instead of Requeue.
func isPermanentRefetchError(err error) bool {
	if err == nil {
		return false
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		return false
	}
	return ec.Code == errcode.ErrAuthUnauthorized ||
		ec.Code == errcode.ErrAuthForbidden ||
		ec.Kind == errcode.KindInvalid
}

func (s *Service) recordConfigEventProcess(ctx context.Context, reason obmetrics.ConfigEventProcessReason) {
	if s.configEventCollector == nil {
		return
	}
	obmetrics.RecordConfigEventProcess(ctx, s.configEventCollector, reason)
}
