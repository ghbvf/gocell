// Package auditverify implements the audit-verify slice: verifies hash chain
// integrity and publishes verification results.
package auditverify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/audit-core/internal/domain"
	"github.com/ghbvf/gocell/cells/audit-core/internal/ports"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	outboxrt "github.com/ghbvf/gocell/runtime/outbox"
)

const (
	TopicIntegrityVerified = "event.audit.integrity-verified.v1"
)

// VerifyResult holds the outcome of a chain verification.
type VerifyResult struct {
	Valid             bool `json:"valid"`
	FirstInvalidIndex int  `json:"firstInvalidIndex"`
	EntriesChecked    int  `json:"entriesChecked"`
}

// Option configures an audit-verify Service.
type Option func(*Service)

// WithOutboxWriter sets the outbox.Writer for transactional event publishing.
func WithOutboxWriter(w outbox.Writer) Option {
	return func(s *Service) { s.outboxWriter = w }
}

// WithTxManager sets the TxRunner for transactional guarantees (L2 atomicity).
func WithTxManager(tx persistence.TxRunner) Option {
	return func(s *Service) { s.txRunner = tx }
}

// Service verifies hash chain integrity.
type Service struct {
	repo         ports.AuditRepository
	chain        *domain.HashChain
	publisher    outbox.Publisher
	outboxWriter outbox.Writer
	txRunner     persistence.TxRunner
	logger       *slog.Logger
}

// NewService creates an audit-verify Service.
func NewService(
	repo ports.AuditRepository,
	hmacKey []byte,
	pub outbox.Publisher,
	logger *slog.Logger,
	opts ...Option,
) *Service {
	s := &Service{
		repo:      repo,
		chain:     domain.NewHashChain(hmacKey),
		publisher: pub,
		logger:    logger,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// VerifyChain verifies the integrity of all entries in the given range.
func (s *Service) VerifyChain(ctx context.Context, from, to int) (*VerifyResult, error) {
	entries, err := s.repo.GetRange(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("audit-verify: get range: %w", err)
	}

	valid, firstInvalid := s.chain.Verify(entries)

	result := &VerifyResult{
		Valid:             valid,
		FirstInvalidIndex: firstInvalid,
		EntriesChecked:    len(entries),
	}

	// Publish verification result via outbox (durable) or direct publish (demo).
	payload, err := json.Marshal(map[string]any{
		"valid":             valid,
		"firstInvalidIndex": firstInvalid,
		"entriesChecked":    len(entries),
	})
	if err != nil {
		return result, fmt.Errorf("audit-verify: marshal payload: %w", err)
	}

	// Persist + outbox write in a transaction for L2 atomicity.
	persistFn := s.buildPersistFn(payload)
	if persistErr := s.runPersist(ctx, persistFn); persistErr != nil {
		return result, fmt.Errorf("audit-verify: persist: %w", persistErr)
	}

	// Fallback direct publish when outbox is not in use. Wrap in v1 wire envelope
	// so the eventbus fail-closed schema check (P1-14) accepts the message.
	if s.outboxWriter == nil {
		envelope := outboxrt.MarshalDirectEnvelope(TopicIntegrityVerified, TopicIntegrityVerified, outbox.NewEntryID(), payload)
		if pubErr := s.publisher.Publish(ctx, TopicIntegrityVerified, envelope); pubErr != nil {
			s.logger.Warn("audit-verify: failed to publish event (demo mode)",
				slog.Any("error", pubErr),
				slog.String("topic", TopicIntegrityVerified))
		}
	}

	s.logger.Info("hash chain verification completed",
		slog.Bool("valid", valid), slog.Int("entries_checked", len(entries)))

	return result, nil
}

// buildPersistFn returns a transaction function that writes the outbox event.
func (s *Service) buildPersistFn(payload []byte) func(context.Context) error {
	return func(txCtx context.Context) error {
		if s.outboxWriter == nil {
			return nil
		}
		return s.outboxWriter.Write(txCtx, outbox.Entry{
			ID:        outbox.NewEntryID(),
			EventType: TopicIntegrityVerified,
			Payload:   payload,
		})
	}
}

// runPersist executes fn in a transaction if txRunner is configured, otherwise
// calls fn(ctx) directly. Nil txRunner is intentional for query-only slices;
// Cell Init() validates txRunner presence for CUD slices before Start().
func (s *Service) runPersist(ctx context.Context, fn func(context.Context) error) error {
	if s.txRunner != nil {
		return s.txRunner.RunInTx(ctx, fn)
	}
	return fn(ctx)
}
