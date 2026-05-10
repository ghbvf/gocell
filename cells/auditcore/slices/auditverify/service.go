// Package auditverify implements the audit-verify slice: verifies hash chain
// integrity via ledger.Store and publishes verification results.
package auditverify

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/auditcore/internal/dto"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

// VerifyResult holds the outcome of a chain verification.
type VerifyResult struct {
	Valid            bool  `json:"valid"`
	FirstInvalidSeq  int64 `json:"firstInvalidSeq"`
	EntriesChecked   int64 `json:"entriesChecked"`
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
	return func(s *Service) {
		if tx != nil {
			s.txRunner = tx
		}
	}
}

// Service verifies ledger hash chain integrity.
type Service struct {
	store    ledger.Store
	txRunner persistence.TxRunner
	emitter  outbox.Emitter
	logger   *slog.Logger
}

// NewService creates an audit-verify Service. Returns an error if txRunner is nil.
// TxRunner must be provided via WithTxManager; nil txRunner is rejected to
// prevent silent loss of L2 atomicity guarantees.
func NewService(
	store ledger.Store,
	logger *slog.Logger,
	opts ...Option,
) (*Service, error) {
	s := &Service{
		store:   store,
		emitter: outbox.NewNoopEmitter(),
		logger:  logger,
	}
	for _, o := range opts {
		o(s)
	}
	if s.txRunner == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"auditverify: TxRunner required; use WithTxManager")
	}
	return s, nil
}

// VerifyChain verifies the integrity of entries in the range [fromSeq, toSeq].
func (s *Service) VerifyChain(ctx context.Context, fromSeq, toSeq int64) (*VerifyResult, error) {
	valid, firstInvalidSeq, err := s.store.Verify(ctx, fromSeq, toSeq)
	if err != nil {
		return nil, fmt.Errorf("audit-verify: verify: %w", err)
	}

	entriesChecked := toSeq - fromSeq + 1
	if !valid && firstInvalidSeq >= 0 {
		entriesChecked = firstInvalidSeq - fromSeq
	}

	result := &VerifyResult{
		Valid:           valid,
		FirstInvalidSeq: firstInvalidSeq,
		EntriesChecked:  entriesChecked,
	}

	event := dto.AuditIntegrityVerifiedEvent{
		Valid:             valid,
		FirstInvalidIndex: int(firstInvalidSeq),
		EntriesChecked:    int(entriesChecked),
	}

	// Persist + outbox write in a transaction for L2 atomicity.
	if persistErr := s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		return outbox.Emit(txCtx, s.emitter, dto.TopicAuditIntegrityVerified, event)
	}); persistErr != nil {
		return result, fmt.Errorf("audit-verify: persist: %w", persistErr)
	}

	s.logger.Info("hash chain verification completed",
		slog.Bool("valid", valid),
		slog.Int64("entries_checked", entriesChecked))

	return result, nil
}
