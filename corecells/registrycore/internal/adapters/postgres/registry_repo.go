package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/corecells/registrycore/internal/ports"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	pgquery "github.com/ghbvf/gocell/framework/pkg/pgquery"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

// Compile-time check.
var _ ports.Registry = (*Registry)(nil)

// Registry is the PostgreSQL ports.Registry backed by the contract_registrations
// projection table and the append-only contract_registration_events history
// table (migration 066). The two writes of a Create/Transition land in one L1
// transaction (the caller's TxManager.RunInTx supplies the ambient tx via
// persistence.TxCtxKey; resolveWrite requires it), so a failure rolls both back.
// State-machine legality is the kernel's single source of truth — Transition
// validates via registry.Transition and never re-encodes the table in SQL.
//
// The struct carries either a *Session (production: resolves the ambient tx) or a
// bare DBTX (test-only: injected by newRegistryFromDBTX), mirroring configcore.
type Registry struct {
	db      DBTX     // test-only: set by newRegistryFromDBTX
	session *Session // production path: resolves ambient tx via persistence.TxCtxKey
	clk     clock.Clock
}

// NewRegistry builds the PG registry over pool. clk stamps registration and event
// timestamps (clock.Clock convention).
func NewRegistry(pool *pgxpool.Pool, clk clock.Clock) *Registry {
	clock.MustHaveClock(clk, "registrycore/postgres.NewRegistry")
	return &Registry{session: NewSession(pool), clk: clk}
}

// resolveRead returns the DBTX for read paths (ambient tx if present, else pool).
//
// Pool fallback scope: the pool fallback (no ambient tx) is ONLY safe for
// superuser / integration-test paths and mem-topology bootstrap reads where no
// FORCE ROW LEVEL SECURITY policy is active.  In a production PG tenant-scoped
// read the GUC app.tenant_id MUST be set via TxManager.RunInTx before any query
// touches contract_registrations; without it the restricted serving role returns
// 0 rows under FORCE RLS — the read fail-closes silently (no error, no data leak,
// but also no correct tenant data).  The bound read service guarantees the ambient
// tx by routing every read through registrycore/internal/scopedread (#2392), so in
// production this branch always returns the ambient tx; the pool fallback survives
// only for the superuser/integration/mem-bootstrap callers above — matching
// configcore session.resolve (read = pool fallback; resolveWrite = fail-fast).
func (r *Registry) resolveRead(ctx context.Context) DBTX {
	if r.session != nil {
		return r.session.resolve(ctx)
	}
	return r.db
}

// resolveWrite returns the DBTX for write paths, requiring an ambient tx in
// production so the projection + history writes are atomic (L1).
func (r *Registry) resolveWrite(ctx context.Context) (DBTX, error) {
	if r.session != nil {
		return r.session.resolveWrite(ctx)
	}
	return r.db, nil
}

const (
	colsProjection   = `kind, payload_schema, submitter, approver, state, created_at, updated_at`
	msgInvalidTenant = "registry repo: invalid tenant"
)

func (r *Registry) Create(ctx context.Context, t tenant.TenantID, in registry.SubmitInput) (registry.ContractRegistration, error) {
	if err := t.Validate(); err != nil {
		return registry.ContractRegistration{}, invalidTenant(err)
	}
	in = in.Normalized()
	if err := in.Validate(); err != nil { // kernel validation — single source, no mem/PG fork
		return registry.ContractRegistration{}, err
	}
	db, err := r.resolveWrite(ctx)
	if err != nil {
		return registry.ContractRegistration{}, err
	}
	now := r.clk.Now()
	submitted := registry.StateSubmitted()
	// ON CONFLICT DO NOTHING + RowsAffected detects the per-tenant duplicate
	// without aborting the transaction (so the caller's tx stays usable).
	// approver is omitted — the column's DB DEFAULT '' applies (a submission has no
	// approver until the pending-approval → approved transition records one).
	n, err := db.Exec(ctx,
		`INSERT INTO contract_registrations
		   (tenant_id, id, kind, payload_schema, submitter, state, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
		 ON CONFLICT (tenant_id, id) DO NOTHING`,
		t.String(), in.ID, in.Kind, in.PayloadSchema, in.Submitter, submitted.String(), now)
	if err != nil {
		return registry.ContractRegistration{}, queryErr("create", err)
	}
	if n == 0 {
		return registry.ContractRegistration{}, dupErr(in.ID)
	}
	// Initial migration event: From = zero sentinel (stored as ''), To = submitted.
	if err := r.appendEvent(ctx, db, t, in.ID, registry.RegistrationState{}, submitted, in.Submitter, "", now); err != nil {
		return registry.ContractRegistration{}, err
	}
	return registry.ContractRegistration{
		ID:            in.ID,
		Kind:          in.Kind,
		PayloadSchema: in.PayloadSchema,
		Submitter:     in.Submitter,
		State:         submitted,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

func (r *Registry) Transition(ctx context.Context, t tenant.TenantID, in registry.AdvanceInput) (registry.ContractRegistration, error) {
	if err := t.Validate(); err != nil {
		return registry.ContractRegistration{}, invalidTenant(err)
	}
	in = in.Normalized()
	if err := in.Validate(); err != nil {
		return registry.ContractRegistration{}, err
	}
	db, err := r.resolveWrite(ctx)
	if err != nil {
		return registry.ContractRegistration{}, err
	}
	cur, err := r.loadForUpdate(ctx, db, t, in.ID)
	if err != nil {
		return registry.ContractRegistration{}, err
	}
	if err := registry.Transition(cur.State, in.To); err != nil { // single-source legality
		return registry.ContractRegistration{}, err
	}
	now := r.clk.Now()
	approver := cur.Approver
	if in.To == registry.StateApproved() {
		approver = in.Actor
	}
	if _, err := db.Exec(ctx,
		`UPDATE contract_registrations SET state = $1, approver = $2, updated_at = $3
		 WHERE tenant_id = $4 AND id = $5`,
		in.To.String(), approver, now, t.String(), in.ID); err != nil {
		return registry.ContractRegistration{}, queryErr("transition", err)
	}
	if err := r.appendEvent(ctx, db, t, in.ID, cur.State, in.To, in.Actor, in.Reason, now); err != nil {
		return registry.ContractRegistration{}, err
	}
	cur.State = in.To
	cur.Approver = approver
	cur.UpdatedAt = now
	return cur, nil
}

// loadForUpdate reads the current projection row under a row lock (FOR UPDATE) so
// the read-validate-write of a transition is serialized per registration.
func (r *Registry) loadForUpdate(ctx context.Context, db DBTX, t tenant.TenantID, id string) (registry.ContractRegistration, error) {
	row := db.QueryRow(ctx,
		`SELECT `+colsProjection+`
		 FROM contract_registrations WHERE tenant_id = $1 AND id = $2 FOR UPDATE`,
		t.String(), id)
	reg, ok, err := scanRegistration(row, id)
	if err != nil {
		return registry.ContractRegistration{}, err
	}
	if !ok {
		return registry.ContractRegistration{}, notFoundErr(id)
	}
	return reg, nil
}

// appendEvent inserts one append-only migration event, assigning the next 1-based
// per-registration seq via a scalar subquery so the seq read and the insert are a
// SINGLE atomic statement (no read-then-write window). Shared by Create (from =
// zero sentinel) and Transition.
//
// Concurrency: same-registration writes are already serialized upstream — Create
// by the projection PK (ON CONFLICT lets only one Create win) and Transition by
// the FOR UPDATE row lock in loadForUpdate — so two appendEvent calls never race
// the same (tenant_id, registration_id). The single-statement seq is defense in
// depth on top of that, not the primary guard.
func (r *Registry) appendEvent(
	ctx context.Context, db DBTX, t tenant.TenantID, regID string,
	from, to registry.RegistrationState, actor, reason string, now time.Time,
) error {
	fromStr := "" // zero sentinel persists as '' (ParseState("") → zero)
	if !from.IsZero() {
		fromStr = from.String()
	}
	if _, err := db.Exec(ctx,
		`INSERT INTO contract_registration_events
		   (tenant_id, registration_id, seq, from_state, to_state, actor, reason, occurred_at)
		 VALUES (
		   $1, $2,
		   (SELECT COALESCE(MAX(seq), 0) + 1 FROM contract_registration_events
		     WHERE tenant_id = $1 AND registration_id = $2),
		   $3, $4, $5, $6, $7)`,
		t.String(), regID, fromStr, to.String(), actor, reason, now); err != nil {
		return queryErr("append-event", err)
	}
	return nil
}

func (r *Registry) Get(ctx context.Context, t tenant.TenantID, id string) (registry.ContractRegistration, bool, error) {
	if err := t.Validate(); err != nil {
		return registry.ContractRegistration{}, false, invalidTenant(err)
	}
	row := r.resolveRead(ctx).QueryRow(ctx,
		`SELECT `+colsProjection+`
		 FROM contract_registrations WHERE tenant_id = $1 AND id = $2`,
		t.String(), id)
	return scanRegistration(row, id)
}

func (r *Registry) List(
	ctx context.Context, t tenant.TenantID, params query.ListParams, filter ports.ListFilter,
) ([]registry.ContractRegistration, error) {
	if err := t.Validate(); err != nil {
		return nil, invalidTenant(err)
	}
	b := pgquery.NewBuilder()
	b.AppendParam("SELECT id, "+colsProjection+" FROM contract_registrations WHERE tenant_id = ", t.String())
	// optional state filter: applied before AppendKeyset so the keyset/ORDER/LIMIT
	// operate on the already-filtered result set
	b.AppendIf(!filter.State.IsZero(), " AND state = ", filter.State.String())
	if err := pgquery.AppendKeyset(b, params); err != nil {
		return nil, queryErr("list-keyset", err)
	}
	sqlStr, args := b.Build()
	rows, err := r.resolveRead(ctx).Query(ctx, sqlStr, args...)
	if err != nil {
		return nil, queryErr("list", err)
	}
	defer rows.Close()
	var out []registry.ContractRegistration
	for rows.Next() {
		reg, err := scanRegistrationRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, reg)
	}
	if err := rows.Err(); err != nil {
		return nil, queryErr("list-rows", err)
	}
	return out, nil
}

func (r *Registry) History(ctx context.Context, t tenant.TenantID, id string) ([]registry.RegistrationEvent, error) {
	if err := t.Validate(); err != nil {
		return nil, invalidTenant(err)
	}
	rows, err := r.resolveRead(ctx).Query(ctx,
		`SELECT seq, from_state, to_state, actor, reason, occurred_at
		 FROM contract_registration_events
		 WHERE tenant_id = $1 AND registration_id = $2 ORDER BY seq ASC`,
		t.String(), id)
	if err != nil {
		return nil, queryErr("history", err)
	}
	defer rows.Close()
	var out []registry.RegistrationEvent
	for rows.Next() {
		var (
			seq                           int
			fromStr, toStr, actor, reason string
			occurredAt                    time.Time
		)
		if err := rows.Scan(&seq, &fromStr, &toStr, &actor, &reason, &occurredAt); err != nil {
			return nil, queryErr("history-scan", err)
		}
		from, okFrom := registry.ParseState(fromStr) // '' → zero sentinel (initial event)
		if !okFrom {
			return nil, corruptStateErr(id, fromStr)
		}
		// to_state is NOT NULL and always a real state (never the zero sentinel),
		// symmetric with the projection-state guard in buildRegistration.
		to, okTo := registry.ParseState(toStr)
		if !okTo || to.IsZero() {
			return nil, corruptStateErr(id, toStr)
		}
		out = append(out, registry.RegistrationEvent{
			RegistrationID: id, Seq: seq, From: from, To: to, Actor: actor, Reason: reason, OccurredAt: occurredAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, queryErr("history-rows", err)
	}
	return out, nil
}

// buildRegistration assembles a ContractRegistration from scanned column values,
// parsing the sealed state and fail-closing on a corrupt (unparseable or zero)
// state read from the store. Single source for both scan helpers so adding a
// projection column changes one place. A projection state is always a real,
// non-zero state.
func buildRegistration(
	id, kind, payloadSchema, submitter, approver, stateStr string, createdAt, updatedAt time.Time,
) (registry.ContractRegistration, error) {
	state, ok := registry.ParseState(stateStr)
	if !ok || state.IsZero() {
		return registry.ContractRegistration{}, corruptStateErr(id, stateStr)
	}
	return registry.ContractRegistration{
		ID: id, Kind: kind, PayloadSchema: payloadSchema, Submitter: submitter,
		Approver: approver, State: state, CreatedAt: createdAt, UpdatedAt: updatedAt,
	}, nil
}

// scanRegistration builds a ContractRegistration from a single-row scanner
// selecting colsProjection. Returns ok=false (nil error) when the row is absent.
func scanRegistration(row RowScanner, id string) (registry.ContractRegistration, bool, error) {
	var (
		kind, payloadSchema, submitter, approver, stateStr string
		createdAt, updatedAt                               time.Time
	)
	if err := row.Scan(&kind, &payloadSchema, &submitter, &approver, &stateStr, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return registry.ContractRegistration{}, false, nil
		}
		return registry.ContractRegistration{}, false, queryErr("scan", err)
	}
	reg, err := buildRegistration(id, kind, payloadSchema, submitter, approver, stateStr, createdAt, updatedAt)
	if err != nil {
		return registry.ContractRegistration{}, false, err
	}
	return reg, true, nil
}

// scanRegistrationRow builds a ContractRegistration from a multi-row scanner
// selecting id + colsProjection (List path).
func scanRegistrationRow(rows Rows) (registry.ContractRegistration, error) {
	var (
		id, kind, payloadSchema, submitter, approver, stateStr string
		createdAt, updatedAt                                   time.Time
	)
	if err := rows.Scan(&id, &kind, &payloadSchema, &submitter, &approver, &stateStr, &createdAt, &updatedAt); err != nil {
		return registry.ContractRegistration{}, queryErr("list-scan", err)
	}
	return buildRegistration(id, kind, payloadSchema, submitter, approver, stateStr, createdAt, updatedAt)
}

func invalidTenant(err error) error {
	return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgInvalidTenant, err)
}

func dupErr(id string) error {
	return errcode.New(errcode.KindConflict, errcode.ErrRegistrationDuplicate,
		"registry repo: registration id already exists",
		errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("id=%q", id))))
}

func notFoundErr(id string) error {
	return errcode.New(errcode.KindNotFound, errcode.ErrRegistrationNotFound,
		"registry repo: registration not found",
		errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("id=%q", id))))
}

func corruptStateErr(id, state string) error {
	return errcode.New(errcode.KindInternal, errcode.ErrRegistrationRepoQuery,
		"registry repo: unparseable state from store",
		errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("id=%q state=%q", id, state))))
}

func queryErr(op string, err error) error {
	return errcode.Wrap(errcode.KindInternal, errcode.ErrRegistrationRepoQuery,
		"registry repo query failed", err,
		errcode.WithInternal(errcode.InternalAttr("_", "op="+op)))
}
