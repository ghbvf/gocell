package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/adapters/postgres/internal/pgexec"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/ctxcancel"
	"github.com/ghbvf/gocell/pkg/errcode"
	pgquery "github.com/ghbvf/gocell/pkg/pgquery"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

// Compile-time assertions.
var (
	_ ledger.Store       = (*LedgerStore)(nil)
	_ healthz.RepoProber = (*LedgerStore)(nil)
)

// SQL statements for audit_entries operations.
// All statements use positional parameters ($N); no dynamic SQL concatenation.
const (
	// lockNamespaceSQL acquires a transaction-scoped advisory lock keyed on the
	// namespace string. hashtextextended(text, 0) returns a stable int64 from any
	// text value — same function used by refresh_store.go for session locking.
	//
	// ref: adapters/postgres/refresh_store.go lockSessionSQL — advisory lock pattern.
	lockNamespaceSQL = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`

	// selectTailForUpdateSQL reads the highest seq_no row in the namespace and
	// locks it with SELECT FOR UPDATE to fence concurrent Appends within the same
	// namespace (second safety guard after the advisory lock).
	selectTailForUpdateSQL = `
SELECT seq_no, hash
FROM audit_entries
WHERE namespace = $1
ORDER BY seq_no DESC
LIMIT 1
FOR UPDATE`

	// insertEntrySQL inserts a new audit entry row. 16 columns post-046:
	// the original 15 columns plus trace_id (added in 046_audit_entries_trace_id.sql
	// as an observability correlation column, NOT part of the HMAC chain).
	// All six NOT NULL no-DEFAULT columns must be supplied explicitly.
	insertEntrySQL = `
INSERT INTO audit_entries
    (id, namespace, seq_no, event_id, event_type, actor_id,
     subject_id, tenant_id, session_id, correlation_id, trace_id, occurred_at,
     timestamp, payload, prev_hash, hash)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`

	// selectBySeqSQL fetches a single entry by namespace + seq_no. 15 selected
	// columns (id + 14 entry fields; seq_no replays the WHERE filter).
	selectBySeqSQL = `
SELECT id, seq_no, event_id, event_type, actor_id,
       subject_id, tenant_id, session_id, correlation_id, trace_id, occurred_at,
       timestamp, payload, prev_hash, hash
FROM audit_entries
WHERE namespace = $1
  AND seq_no    = $2`

	// selectRangeSQL fetches a contiguous seq_no range for Verify in ascending
	// order. 14 columns (no id needed — Verify only checks chain linkage).
	selectRangeSQL = `
SELECT seq_no, event_id, event_type, actor_id,
       subject_id, tenant_id, session_id, correlation_id, trace_id, occurred_at,
       timestamp, payload, prev_hash, hash
FROM audit_entries
WHERE namespace = $1
  AND seq_no >= $2
  AND seq_no <= $3
ORDER BY seq_no ASC`

	// selectFingerprintSQL checks for an existing entry with the same stable
	// identity key used by the ContentFingerprint idempotency mode.
	//
	// F-CR-2: fingerprint is now EventID-only (not EventID+EventType+ActorID+
	// Timestamp+Payload). At-least-once redelivery produces the same EventID
	// each time; Timestamp changes on every retry — the old multi-field form
	// produced a different fingerprint on each attempt, defeating idempotency.
	//
	// The DB-level uq_audit_namespace_event_id UNIQUE INDEX (migration 021)
	// provides a second-line guard against concurrent bypass of this check.
	//
	// ref: Watermill router.go — message.UUID as dedup key.
	// ref: NServiceBus MessageDeduplicationBehavior — message ID as idempotency key.
	selectFingerprintSQL = `
SELECT 1 FROM audit_entries
WHERE namespace = $1
  AND event_id  = $2
LIMIT 1`
)

// LedgerStore is a PostgreSQL implementation of ledger.Store. It persists
// audit entries in a tamper-evident hash chain using the following design:
//
//   - pg_advisory_xact_lock(hashtextextended(namespace, 0)) serializes concurrent
//     Append calls within the same namespace. Different namespaces use different
//     int64 hash keys, so their advisory locks never contend (B2-C-10).
//   - SELECT ... FOR UPDATE on the tail row fences the read-modify-write cycle.
//   - All DML runs inside the caller's ambient transaction via txRunner.RunInTx.
//   - Idempotency uses a stable EventID fingerprint check before inserting.
//     EventID (UUID from the outbox entry) is the same across at-least-once
//     redeliveries; Timestamp changes per retry so it is excluded (F-CR-2).
//     A DB-level UNIQUE INDEX on (namespace, event_id) (migration 021) is the
//     second-line guard against concurrent bypass of the application check.
//
// Consistency level: L1 LocalTx — Append is a single-transaction write that
// participates in the caller's ambient transaction. L2 callers compose this
// store with an outbox.Writer inside the same RunInTx block.
//
// ref: google/trillian storage/log_storage.go ReadWriteTransaction pattern.
// ref: adapters/postgres/refresh_store.go — advisory lock + ambient tx model.
type LedgerStore struct {
	db       pgexec.PGExecutor
	txRunner persistence.TxRunner
	protocol *ledger.Protocol
	clock    clock.Clock
}

// NewLedgerStore constructs a LedgerStore. Returns a non-nil error when any
// required dependency is absent (fail-fast at construction, not at runtime):
//
//   - pool nil → ErrValidationFailed
//   - txRunner nil or typed-nil → ErrValidationFailed
//   - protocol nil → ErrValidationFailed
//   - clk nil or typed-nil → ErrValidationFailed
//
// pool is wrapped in a pgExecutor so all SQL paths (Append, Tail, GetBySeq,
// Query, Verify, RepoReady) route through the ambient transaction when ctx
// carries one, and fall back to pool otherwise.
func NewLedgerStore(
	pool *pgxpool.Pool,
	txRunner persistence.TxRunner,
	protocol *ledger.Protocol,
	clk clock.Clock,
) (*LedgerStore, error) {
	if pool == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"postgres.NewLedgerStore: pool must not be nil")
	}
	if validation.IsNilInterface(txRunner) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"postgres.NewLedgerStore: txRunner must not be nil")
	}
	if protocol == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"postgres.NewLedgerStore: protocol must not be nil")
	}
	if validation.IsNilInterface(clk) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"postgres.NewLedgerStore: clock must not be nil")
	}
	return &LedgerStore{
		db:       pgexec.New(pool),
		txRunner: txRunner,
		protocol: protocol,
		clock:    clk,
	}, nil
}

// Protocol returns the immutable protocol decisions backing this store.
func (s *LedgerStore) Protocol() *ledger.Protocol { return s.protocol }

// namespace returns the string form of the configured NamespaceID.
func (s *LedgerStore) namespace() string { return string(s.protocol.Namespace()) }

// Append persists a new audit entry in the namespace's hash chain.
//
// Algorithm (all within txRunner.RunInTx):
//  1. Validate payload is valid JSON.
//  2. Acquire pg_advisory_xact_lock(hashtextextended(namespace, 0)) to serialize.
//  3. Check idempotency fingerprint (event_id only) inside the lock to eliminate
//     TOCTOU between concurrent Appends. Timestamp is excluded from the check:
//     at-least-once redelivery produces the same EventID but a new clk.Now(),
//     so fingerprinting on Timestamp would defeat idempotency (F-CR-2).
//  4. SELECT tail row FOR UPDATE (prevents concurrent tail reads in the same namespace).
//  5. Compute next seq_no = tail.seq_no + 1 (or 1 for empty).
//  6. Compute hash = protocol.ComputeHash(tail.hash, entry).
//  7. INSERT.
//
// F2: advisory lock (step 2) must precede fingerprint check (step 3) so that
// concurrent Appends with identical content cannot both pass the fingerprint
// check and both insert. MemStore acquires its mutex before the fingerprint
// check — PG Append now mirrors that ordering.
func (s *LedgerStore) Append(ctx context.Context, e *ledger.Entry) error {
	if e == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger: Append requires non-nil Entry")
	}
	if err := validateAuditPayloadJSON(e.Payload); err != nil {
		return err
	}

	ns := s.namespace()

	return s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		// Step 2: advisory lock — serializes all Append calls for this namespace.
		// Must run BEFORE the fingerprint check to prevent TOCTOU: two concurrent
		// goroutines with identical payloads would both pass a pre-lock fingerprint
		// check and both attempt to INSERT, causing a duplicate chain entry.
		if _, lockErr := s.db.Exec(txCtx, lockNamespaceSQL, ns); lockErr != nil {
			return ctxcancel.WrapOrInfra(lockErr, "advisory_lock", ns,
				ErrAdapterPGQuery, "audit ledger: namespace advisory lock failed")
		}

		// Step 3: idempotency fingerprint check — now inside the advisory lock.
		dup, err := s.checkFingerprint(txCtx, ns, e)
		if err != nil {
			return err
		}
		if dup {
			return errcode.New(errcode.KindConflict, errcode.ErrAuditLedgerAlreadyExists,
				"audit ledger: duplicate content fingerprint")
		}

		// Step 4: read current tail (SELECT FOR UPDATE).
		prevHash, nextSeqNo, tailErr := s.readTailForUpdate(txCtx, ns)
		if tailErr != nil {
			return tailErr
		}

		// Steps 5+6: assign seq_no and compute hash.
		e.SeqNo = nextSeqNo
		e.PrevHash = prevHash
		e.Hash = s.protocol.ComputeHash(prevHash, e)

		// Step 7: insert the row.
		id := uuid.New()
		if _, insertErr := s.db.Exec(
			txCtx, insertEntrySQL,
			id.String(), ns, e.SeqNo,
			e.EventID, e.EventType, e.ActorID,
			e.SubjectID, e.TenantID, e.SessionID, e.CorrelationID, e.TraceID, e.OccurredAt,
			e.Timestamp, e.Payload, e.PrevHash, e.Hash,
		); insertErr != nil {
			return ctxcancel.WrapOrInfra(insertErr, "insert", ns,
				ErrAdapterPGQuery, "audit ledger: insert entry failed")
		}
		e.ID = id.String()
		return nil
	})
}

// checkFingerprint returns true if an entry with the same EventID already
// exists in the namespace. EventID (UUID from the outbox entry) is the stable
// identity across at-least-once redeliveries; other fields (Timestamp, Payload)
// may change between retries and must not be part of the fingerprint.
func (s *LedgerStore) checkFingerprint(ctx context.Context, ns string, e *ledger.Entry) (bool, error) {
	var marker int
	err := s.db.QueryRow(ctx, selectFingerprintSQL, ns, e.EventID).Scan(&marker)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, ctxcancel.WrapOrInfra(err, "fingerprint_check", ns,
			ErrAdapterPGQuery, "audit ledger: fingerprint check failed")
	}
	return true, nil
}

// readTailForUpdate reads the current tail (prevHash, nextSeqNo) within an
// advisory-locked transaction context. Returns ("", 1, nil) for an empty namespace.
func (s *LedgerStore) readTailForUpdate(ctx context.Context, ns string) (prevHash string, nextSeqNo int64, err error) {
	var tailSeqNo int64
	var tailHash string
	scanErr := s.db.QueryRow(ctx, selectTailForUpdateSQL, ns).Scan(&tailSeqNo, &tailHash)
	if errors.Is(scanErr, pgx.ErrNoRows) {
		return "", 1, nil
	}
	if scanErr != nil {
		return "", 0, ctxcancel.WrapOrInfra(scanErr, "tail_select", ns,
			ErrAdapterPGQuery, "audit ledger: read tail for update failed")
	}
	return tailHash, tailSeqNo + 1, nil
}

// tailWithCountSQL retrieves the latest seq_no + hash + total row count in a
// single query, avoiding a separate COUNT(*) round-trip.
// Returns (0, "", 0) via ErrNoRows when the namespace is empty.
//
// F13: merged Tail + Count into one query.
//
// $1 (namespace) appears twice — in the COUNT(*) subquery and the outer WHERE.
// pgx binds a single positional parameter to all occurrences of $1, so both
// clauses receive the same namespace value; this is intentional, not a
// parameter mismatch. If the two clauses ever needed different values they
// would use $1 and $2 respectively.
const tailWithCountSQL = `
SELECT seq_no, hash,
       (SELECT COUNT(*) FROM audit_entries WHERE namespace=$1) AS total
FROM audit_entries
WHERE namespace=$1
ORDER BY seq_no DESC
LIMIT 1`

// Tail returns the current chain tail snapshot. Returns zero TailSnapshot for
// an empty namespace (not an error). Routes through pgExecutor (ambient-tx
// aware; falls back to pool when no tx in ctx).
//
// F13: uses a single SQL query to retrieve seq_no, hash, and total count.
//
// Caveat: when called within a caller's ambient transaction, the result
// reflects that transaction's uncommitted chain state. Callers requiring
// post-commit integrity verification must call Tail after the transaction
// commits.
func (s *LedgerStore) Tail(ctx context.Context) (ledger.TailSnapshot, error) {
	ns := s.namespace()

	var seqNo int64
	var hash string
	var count int64
	err := s.db.QueryRow(ctx, tailWithCountSQL, ns).Scan(&seqNo, &hash, &count)
	if errors.Is(err, pgx.ErrNoRows) {
		return ledger.TailSnapshot{}, nil
	}
	if err != nil {
		return ledger.TailSnapshot{}, ctxcancel.WrapOrInfra(err, "tail", ns,
			ErrAdapterPGQuery, "audit ledger: tail query failed")
	}

	return ledger.TailSnapshot{
		SeqNo:      seqNo,
		PrevHash:   hash,
		EntryCount: count,
	}, nil
}

// ledgerRepoReadySQL is a representative zero-cost query for the audit_entries
// table. It returns no rows but exercises schema existence and table-level
// permissions, surfacing migration drift that a pool-level ping cannot detect.
// Matches the SELECT 1 FROM <t> WHERE false pattern used by PGSessionStore.
const ledgerRepoReadySQL = `SELECT 1 FROM audit_entries WHERE false`

// RepoReady implements healthz.RepoProber. It issues a cheap
// non-transactional representative query against the audit_entries table so
// that schema/migration drift and table-level permission loss are surfaced as a
// differentiated failure domain distinct from the pool-level postgres_ready
// probe registered by *Pool.
//
// The ambient-tx fallback in pgExecutor is a no-op for this probe: health
// handler contexts never carry a pgx.Tx, so pgExecutor routes directly to the
// pool, keeping the health check independent of any caller transaction state.
func (s *LedgerStore) RepoReady(ctx context.Context) error {
	_, err := s.db.Exec(ctx, ledgerRepoReadySQL)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery,
			"audit ledger: repo ready", err)
	}
	return nil
}

// GetBySeq fetches a single entry by sequence number.
// Returns ErrAuditLedgerNotFound when the seq does not exist.
func (s *LedgerStore) GetBySeq(ctx context.Context, seq int64) (*ledger.Entry, error) {
	ns := s.namespace()
	var e ledger.Entry
	err := s.db.QueryRow(ctx, selectBySeqSQL, ns, seq).Scan(
		&e.ID, &e.SeqNo,
		&e.EventID, &e.EventType, &e.ActorID,
		&e.SubjectID, &e.TenantID, &e.SessionID, &e.CorrelationID, &e.TraceID, &e.OccurredAt,
		&e.Timestamp, &e.Payload, &e.PrevHash, &e.Hash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errcode.New(
			errcode.KindNotFound, errcode.ErrAuditLedgerNotFound,
			"audit ledger: entry not found",
			errcode.WithDetails(errcode.PublicInt("seqNo", seq)),
		)
	}
	if err != nil {
		return nil, ctxcancel.WrapOrInfra(err, "get_by_seq", ns,
			ErrAdapterPGQuery, "audit ledger: get by seq failed")
	}
	return &e, nil
}

// Query lists entries matching the supplied AuditFilters using keyset cursor
// pagination pushed into SQL via pgquery.AppendKeyset: params.Sort drives the
// ORDER BY and the keyset WHERE predicate, and params.FetchLimit() (Limit+1) is
// the LIMIT for N+1 hasMore detection. The idx_audit_namespace_ts_id composite
// index covers the (timestamp DESC, id ASC) keyset, so this is an index scan.
// params.Sort must be non-empty (callers pass ledger.QuerySort); AppendKeyset
// returns ErrValidationFailed on empty Sort. Returns an empty (non-nil) slice
// when no entries match.
func (s *LedgerStore) Query(ctx context.Context, filters ledger.AuditFilters, params query.ListParams) ([]*ledger.Entry, error) {
	ns := s.namespace()

	params, err := bindTimestampCursor(params)
	if err != nil {
		return nil, err
	}

	b := pgquery.NewBuilder()
	b.AppendParam(`SELECT id, seq_no, event_id, event_type, actor_id,
       subject_id, tenant_id, session_id, correlation_id, trace_id, occurred_at,
       timestamp, payload, prev_hash, hash
FROM audit_entries WHERE namespace = `, ns)
	b.AppendIf(filters.EventType != "", `AND event_type = `, filters.EventType)
	b.AppendIf(filters.ActorID != "", `AND actor_id = `, filters.ActorID)
	b.AppendIf(filters.SubjectID != "", `AND subject_id = `, filters.SubjectID)
	b.AppendIf(filters.TraceID != "", `AND trace_id = `, filters.TraceID)
	b.AppendIf(!filters.From.IsZero(), `AND timestamp >= `, filters.From)
	b.AppendIf(!filters.To.IsZero(), `AND timestamp <= `, filters.To)
	if ksErr := pgquery.AppendKeyset(b, params); ksErr != nil {
		return nil, ksErr
	}

	sql, args := b.Build()
	rows, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, ctxcancel.WrapOrInfra(err, "query", ns,
			ErrAdapterPGQuery, "audit ledger: query failed")
	}
	defer rows.Close()

	result, scanErr := s.scanEntries(rows, ns)
	if scanErr != nil {
		return nil, scanErr
	}
	if result == nil {
		result = []*ledger.Entry{}
	}
	return result, nil
}

// bindTimestampCursor converts the cursor value for the "timestamp" sort column
// from its RFC3339Nano string wire form back to time.Time. Cursor values arrive
// as strings: the auditquery service Extract closure formats the timestamp as
// RFC3339Nano, and the cursor codec round-trips values through JSON. pgx binds
// time.Time into the timestamptz keyset predicate — exactly as the filters.From
// / filters.To bindings above already do — but cannot encode a bare string for
// a timestamptz parameter. The id UUID tie-breaker already uses the cursor's
// string form and is left untouched. Returns params unchanged on the first page
// (no cursor).
//
// When ledger.QuerySort gains another non-text keyset column (e.g. another
// timestamptz or a numeric column), add a matching conversion case here: pgx
// cannot bind the RFC3339Nano/JSON string wire form to a non-text column.
// Audit is currently the only non-text keyset consumer in the codebase.
func bindTimestampCursor(params query.ListParams) (query.ListParams, error) {
	if params.CursorValues == nil {
		return params, nil
	}
	vals := slices.Clone(params.CursorValues)
	for i, col := range params.Sort {
		if col.Name != "timestamp" || i >= len(vals) {
			continue
		}
		raw, ok := vals[i].(string)
		if !ok {
			continue
		}
		ts, parseErr := time.Parse(time.RFC3339Nano, raw)
		if parseErr != nil {
			return query.ListParams{}, errcode.New(errcode.KindInvalid, errcode.ErrCursorInvalid,
				"invalid cursor; restart from first page (client should discard stored cursor)",
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("timestamp cursor parse: %v", parseErr))))
		}
		vals[i] = ts
	}
	params.CursorValues = vals
	return params, nil
}

// scanEntries scans all rows from a pgx.Rows result into []*ledger.Entry.
func (s *LedgerStore) scanEntries(rows pgx.Rows, ns string) ([]*ledger.Entry, error) {
	var entries []*ledger.Entry
	for rows.Next() {
		var e ledger.Entry
		if err := rows.Scan(
			&e.ID, &e.SeqNo,
			&e.EventID, &e.EventType, &e.ActorID,
			&e.SubjectID, &e.TenantID, &e.SessionID, &e.CorrelationID, &e.TraceID, &e.OccurredAt,
			&e.Timestamp, &e.Payload, &e.PrevHash, &e.Hash,
		); err != nil {
			return nil, ctxcancel.WrapOrInfra(err, "scan", ns,
				ErrAdapterPGQuery, "audit ledger: scan entry failed")
		}
		entries = append(entries, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, ctxcancel.WrapOrInfra(err, "rows_err", ns,
			ErrAdapterPGQuery, "audit ledger: iterate entries failed")
	}
	return entries, nil
}

// Verify re-computes HMAC-SHA256 hash for each entry in [fromSeq, toSeq]
// and checks chain linkage (PrevHash). Returns valid=true and firstInvalidSeq=-1
// when all entries are intact. Routes through pgExecutor (ambient-tx aware).
//
// Sub-range correctness: when fromSeq > 1 the first entry in the range has a
// non-empty PrevHash pointing at entries[fromSeq-1]. Verify fetches that
// predecessor's hash as the baseline so the first PrevHash linkage check is
// evaluated against the correct expected value rather than the empty string used
// for the chain's genesis entry.
//
// Caveat: when called within a caller's ambient transaction, the result
// reflects that transaction's uncommitted chain state. Callers requiring
// post-commit integrity verification must call Verify after the transaction
// commits.
func (s *LedgerStore) Verify(ctx context.Context, fromSeq, toSeq int64) (valid bool, firstInvalidSeq int64, err error) {
	ns := s.namespace()

	if fromSeq < 1 || toSeq < fromSeq {
		return false, fromSeq, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger: Verify requires 1 <= fromSeq <= toSeq")
	}

	prevHash, baseErr := s.verifyBaseline(ctx, ns, fromSeq)
	if baseErr != nil {
		return false, fromSeq, baseErr
	}

	return s.verifyRange(ctx, ns, fromSeq, toSeq, prevHash)
}

// verifyBaseline returns the hash of entries[fromSeq-1] when fromSeq > 1 (the
// sub-range baseline), or "" when fromSeq == 1 (chain genesis). A missing
// baseline row returns ErrAuditLedgerNotFound.
func (s *LedgerStore) verifyBaseline(ctx context.Context, ns string, fromSeq int64) (string, error) {
	if fromSeq == 1 {
		return "", nil
	}
	var baselineHash string
	err := s.db.QueryRow(
		ctx,
		`SELECT hash FROM audit_entries WHERE namespace=$1 AND seq_no=$2`,
		ns, fromSeq-1,
	).Scan(&baselineHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errcode.New(errcode.KindNotFound, errcode.ErrAuditLedgerNotFound,
			"audit ledger: Verify baseline entry not found",
			errcode.WithDetails(errcode.PublicInt("baselineSeqNo", fromSeq-1)))
	}
	if err != nil {
		return "", ctxcancel.WrapOrInfra(err, "verify_baseline", ns,
			ErrAdapterPGQuery, "audit ledger: verify baseline lookup failed")
	}
	return baselineHash, nil
}

// verifyRange scans entries in [fromSeq, toSeq] and validates gap-freeness,
// PrevHash linkage, and hash recomputation. prevHash is the expected PrevHash
// of the first scanned entry (empty string for the chain genesis).
func (s *LedgerStore) verifyRange(ctx context.Context, ns string, fromSeq, toSeq int64, prevHash string) (bool, int64, error) {
	rows, queryErr := s.db.Query(ctx, selectRangeSQL, ns, fromSeq, toSeq)
	if queryErr != nil {
		return false, 0, ctxcancel.WrapOrInfra(queryErr, "verify_query", ns,
			ErrAdapterPGQuery, "audit ledger: verify range query failed")
	}
	defer rows.Close()

	expectedSeq := fromSeq
	for rows.Next() {
		var e ledger.Entry
		if scanErr := rows.Scan(
			&e.SeqNo,
			&e.EventID, &e.EventType, &e.ActorID,
			&e.SubjectID, &e.TenantID, &e.SessionID, &e.CorrelationID, &e.TraceID, &e.OccurredAt,
			&e.Timestamp, &e.Payload, &e.PrevHash, &e.Hash,
		); scanErr != nil {
			return false, 0, ctxcancel.WrapOrInfra(scanErr, "verify_scan", ns,
				ErrAdapterPGQuery, "audit ledger: verify scan failed")
		}
		if e.SeqNo != expectedSeq {
			return false, expectedSeq, nil
		}
		expectedSeq++
		if e.PrevHash != prevHash {
			return false, e.SeqNo, nil
		}
		if e.Hash != s.protocol.ComputeHash(e.PrevHash, &e) {
			return false, e.SeqNo, nil
		}
		prevHash = e.Hash
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return false, 0, ctxcancel.WrapOrInfra(rowsErr, "verify_rows_err", ns,
			ErrAdapterPGQuery, "audit ledger: verify rows error")
	}
	if expectedSeq <= toSeq {
		return false, expectedSeq, errcode.New(
			errcode.KindNotFound, errcode.ErrAuditLedgerNotFound,
			"audit ledger: entry not found during Verify",
			errcode.WithDetails(errcode.PublicInt("missingSeqNo", expectedSeq)),
		)
	}
	return true, -1, nil
}

// validateAuditPayloadJSON checks that payload is a valid JSON object or null.
// nil or empty payload is treated as JSON null (valid).
// F21: arrays and scalar JSON values are rejected — audit entries must carry
// structured event metadata, not bare scalars or lists.
func validateAuditPayloadJSON(payload []byte) error {
	if len(payload) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.NewDecoder(bytes.NewReader(payload)).Decode(&m); err != nil {
		return errcode.New(
			errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger: payload must be a JSON object or null",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("json decode: %v", err))),
		)
	}
	return nil
}
