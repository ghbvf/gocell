package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/adapters/postgres/internal/pgexec"
	"github.com/ghbvf/gocell/framework/kernel/projection"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// insertDeadLetterSQL records one poison event. ON CONFLICT (cell_id,
// projection_id, global_seq) DO NOTHING makes Record idempotent: a re-driven skip
// (the Tailer crashed before the advance committed, then replayed) re-INSERTs the
// same poison event as a no-op. recorded_at defaults to NOW(); the natural
// composite PK doubles as the idempotency key, so there is no surrogate id.
const insertDeadLetterSQL = `INSERT INTO saga_projection_dead_letters
  (cell_id, projection_id, global_seq, event_id, stream, error_type, error_message, occurred_at, recorded_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW())
ON CONFLICT (cell_id, projection_id, global_seq) DO NOTHING`

// SagaProjectionDeadLetterStore is the PostgreSQL projection.DeadLetterStore
// backend for the saga-journal Tailer's poison-event sink (#2110). Like
// ProjectionCheckpointStore it holds no raw pool: the sole field is a
// pgexec.PGExecutor that routes the INSERT through the ambient transaction carried
// by ctx (persistence.TxFromContext).
//
// The Tailer calls Record AND OwnerCheckpointStore.AdvanceIfOwner inside ONE
// TxRunner.RunInTx, so the dead-letter row and the checkpoint advance commit
// atomically — "skipped ⟺ recorded" (a crash can never leave the checkpoint
// advanced past an unrecorded poison event). Holding pgexec.PGExecutor (not
// *pgxpool.Pool) is the ambient-tx contract enforced by
// PROJECTION-CHECKPOINT-TX-BOUND-01.
//
// ref: JasperFx/marten async-daemon DeadLetterEvent (mt_doc_deadletterevent).
// ref: AxonFramework SequencedDeadLetterQueue (dead_letter_entry).
// ref: docs/architecture/202606191200-2110-adr-saga-tailer-poison-dead-letter.md.
type SagaProjectionDeadLetterStore struct {
	db pgexec.PGExecutor
}

// compile-time interface check.
var _ projection.DeadLetterStore = (*SagaProjectionDeadLetterStore)(nil)

// NewSagaProjectionDeadLetterStore wraps the pool in the sealed pgexec funnel
// (PG-REPO-AMBIENT-TX-01 R2). A nil pool is rejected with ErrValidationFailed.
func NewSagaProjectionDeadLetterStore(pool *pgxpool.Pool) (*SagaProjectionDeadLetterStore, error) {
	if pool == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"postgres.NewSagaProjectionDeadLetterStore: pool must not be nil")
	}
	return &SagaProjectionDeadLetterStore{db: pgexec.New(pool)}, nil
}

// Record durably stores one poison event's dead-letter entry inside the ambient
// transaction. Idempotent on (CellID, ProjectionID, GlobalSeq). On failure the
// error carries cell_id/projection_id/global_seq as server-only internal detail
// for ops correlation (the error_message is already redacted by the caller).
func (s *SagaProjectionDeadLetterStore) Record(ctx context.Context, dl projection.DeadLetter) error {
	if _, err := s.db.Exec(ctx, insertDeadLetterSQL,
		dl.CellID, dl.ProjectionID, dl.GlobalSeq, dl.EventID, dl.Stream,
		dl.ErrorType, dl.ErrorMessage, dl.OccurredAt); err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
			"saga projection dead-letter store: record", err,
			errcode.WithInternal(
				errcode.InternalAttr("cell_id", dl.CellID),
				errcode.InternalAttr("projection_id", dl.ProjectionID),
				errcode.InternalAttr("global_seq", dl.GlobalSeq),
			))
	}
	return nil
}
