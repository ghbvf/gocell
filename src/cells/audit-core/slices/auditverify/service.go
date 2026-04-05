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
	"github.com/ghbvf/gocell/pkg/id"
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

// Service verifies hash chain integrity.
type Service struct {
	repo         ports.AuditRepository
	chain        *domain.HashChain
	publisher    outbox.Publisher
	outboxWriter outbox.Writer
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

	// Publish verification result.
	payload, _ := json.Marshal(map[string]any{
		"valid":               valid,
		"first_invalid_index": firstInvalid,
		"entries_checked":     len(entries),
	})
	if s.outboxWriter != nil {
		outboxEntry := outbox.Entry{
			ID:        id.New("evt"),
			EventType: TopicIntegrityVerified,
			Payload:   payload,
		}
		if writeErr := s.outboxWriter.Write(ctx, outboxEntry); writeErr != nil {
			s.logger.Error("audit-verify: failed to write outbox entry",
				slog.Any("error", writeErr))
		}
	} else if pubErr := s.publisher.Publish(ctx, TopicIntegrityVerified, payload); pubErr != nil {
		s.logger.Error("audit-verify: failed to publish event",
			slog.Any("error", pubErr))
	}

	s.logger.Info("hash chain verification completed",
		slog.Bool("valid", valid), slog.Int("entries_checked", len(entries)))

	return result, nil
}
