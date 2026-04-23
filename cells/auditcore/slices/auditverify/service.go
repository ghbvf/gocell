// Package auditverify implements the audit-verify slice: verifies hash chain
// integrity and publishes verification results.
package auditverify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/auditcore/internal/domain"
	"github.com/ghbvf/gocell/cells/auditcore/internal/ports"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
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

// Service verifies hash chain integrity.
type Service struct {
	repo     ports.AuditRepository
	chain    *domain.HashChain
	txRunner persistence.TxRunner
	emitter  outbox.Emitter
	logger   *slog.Logger
}

// NewService creates an audit-verify Service.
func NewService(
	repo ports.AuditRepository,
	hmacKey []byte,
	logger *slog.Logger,
	opts ...Option,
) *Service {
	s := &Service{
		repo:     repo,
		chain:    domain.NewHashChain(hmacKey),
		txRunner: persistence.NoopTxRunner{},
		emitter:  outbox.NewNoopEmitter(),
		logger:   logger,
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

	s.logger.Info("hash chain verification completed",
		slog.Bool("valid", valid), slog.Int("entries_checked", len(entries)))

	return result, nil
}

// buildPersistFn returns a transaction function that writes the outbox event.
func (s *Service) buildPersistFn(payload []byte) func(context.Context) error {
	return func(txCtx context.Context) error {
		return s.emitter.Emit(txCtx, outbox.Entry{
			ID:        outbox.NewEntryID(),
			EventType: TopicIntegrityVerified,
			Payload:   payload,
		})
	}
}

func (s *Service) runPersist(ctx context.Context, fn func(context.Context) error) error {
	return s.txRunner.RunInTx(ctx, fn)
}
