// Package auditverify implements the audit-verify slice: verifies hash chain
// integrity and publishes verification results.
package auditverify

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/auditcore/internal/domain"
	"github.com/ghbvf/gocell/cells/auditcore/internal/dto"
	"github.com/ghbvf/gocell/cells/auditcore/internal/ports"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
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

// WithTxManager sets the TxRunner for transactional guarantees (L2 atomicity).
func WithTxManager(tx persistence.TxRunner) Option {
	return func(s *Service) { s.txRunner = tx }
}

// Service verifies hash chain integrity.
type Service struct {
	repo     ports.AuditRepository
	chain    *domain.HashChain
	txRunner persistence.TxRunner
	emitter  outbox.Emitter
	logger   *slog.Logger
}

// NewService creates an audit-verify Service. Returns an error if txRunner is nil.
// When no TxRunner is provided (publisher-only demo path), a pass-through
// directRunner is used; L2 atomicity is not guaranteed in that mode.
func NewService(
	repo ports.AuditRepository,
	hmacKey []byte,
	logger *slog.Logger,
	opts ...Option,
) (*Service, error) {
	s := &Service{
		repo:    repo,
		chain:   domain.NewHashChain(hmacKey),
		emitter: outbox.NewNoopEmitter(),
		logger:  logger,
	}
	for _, o := range opts {
		o(s)
	}
	if s.txRunner == nil {
		// Publisher-only demo path: provide a pass-through runner.
		s.txRunner = directRunner{}
	}
	return s, nil
}

// directRunner executes fn directly with no transaction wrapper.
// Used when no TxRunner is injected (publisher-only demo mode).
type directRunner struct{}

func (directRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
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

	// Publish the verification result through the injected emitter. Cell wiring
	// decides whether that emitter is backed by durable outbox delivery or demo
	// direct delivery.
	event := dto.AuditIntegrityVerifiedEvent{
		Valid:             valid,
		FirstInvalidIndex: firstInvalid,
		EntriesChecked:    len(entries),
	}

	// Persist + outbox write in a transaction for L2 atomicity.
	persistFn := s.buildPersistFn(event)
	if persistErr := s.runPersist(ctx, persistFn); persistErr != nil {
		return result, fmt.Errorf("audit-verify: persist: %w", persistErr)
	}

	s.logger.Info("hash chain verification completed",
		slog.Bool("valid", valid), slog.Int("entries_checked", len(entries)))

	return result, nil
}

// buildPersistFn returns a transaction function that writes the outbox event.
func (s *Service) buildPersistFn(event dto.AuditIntegrityVerifiedEvent) func(context.Context) error {
	return func(txCtx context.Context) error {
		return outbox.Emit(txCtx, s.emitter, dto.TopicAuditIntegrityVerified, event)
	}
}

func (s *Service) runPersist(ctx context.Context, fn func(context.Context) error) error {
	return s.txRunner.RunInTx(ctx, fn)
}
