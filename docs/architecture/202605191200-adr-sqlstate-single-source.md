# ADR: SQLSTATE classifier single source enforcement (SQLSTATE-SINGLE-SOURCE-01)

- Status: Accepted
- Date: 2026-05-19
- Context refs: DX4 plan `docs/plans/202605082145-034-pg-corecell-b-route-plan.md` §DX4;
  PR #578 (DX4 PG adapter maintainability); PR #578 post-merge review (P1 finding).

## Context

DX4 consolidated the PostgreSQL SQLSTATE classifiers that pkg/pgquery owns —
`23505` unique-violation, `23503` foreign-key-violation, `P0001` PL/pgSQL RAISE
EXCEPTION — into `pkg/pgquery`. Before DX4 the unique/FK classifier was
copy-pasted verbatim into `adapters/postgres`, `cells/accesscore/internal/adapters/postgres`
and the iotdevice example cell-private adapter.

PR #578 deleted two of the duplicates but added **no machine enforcement**. A
third duplicate (`examples/iotdevice/cells/devicecell/internal/adapters/postgres/pgerrors.go`,
introduced by PR #575) survived undetected and falsified the PR's "single source
of truth" claim. The post-merge review filed this as the P1 finding: the
regression root cause was *deleting duplicates without an enforcement gate*, so
the same class of drift can recur the moment a new cell-private PG adapter is
added.

## Decision

Introduce a type-aware archtest **SQLSTATE-SINGLE-SOURCE-01**
(`tools/archtest/sqlstate_single_source_test.go`) plus the supporting code
changes that make the rule carve-out-free:

1. Add `pgquery.IsRaiseException(err) error` to `pkg/pgquery`. This (a) gives
   the previously dead-exported `SQLStateRaiseException` const a real consumer
   (resolves the review's separate P2 dead-API finding) and (b) lets accesscore
   stop reading `pgconn.PgError.Code` itself.
2. Refactor `cells/accesscore/internal/adapters/postgres/lastadmin.go` to gate
   on `pgquery.IsRaiseException(err)` and only read `pgconn.PgError.Message`
   (the trigger-attribution signal, which is not a SQLSTATE code and stays
   local). It no longer reads `.Code`.
3. Migrate `examples/iotdevice/.../device_repo.go` to
   `pgquery.IsUniqueViolation` and delete `pgerrors.go` (the surviving
   duplicate).

### Rule shape (value-scoped, form-unique)

Outside `pkg/pgquery/`, no comparison may test a `pgconn.PgError.Code` field
against an **owned** SQLSTATE literal. Covered AST forms:

- `BinaryExpr` `<x>.Code == "<owned>"` / `!=`, either operand order.
- `SwitchStmt` `switch <x>.Code { case "<owned>": … }`.

`<x>` is resolved via `*types.Info` to the exact named type
`github.com/jackc/pgx/v5/pgconn.PgError` (pointer dereferenced); the literal is
resolved via `EvaluateConstString`. The owned set is sourced directly from the
`pkg/pgquery` exported consts (`SQLStateUniqueViolation` /
`SQLStateForeignKeyViolation` / `SQLStateRaiseException`) — a fourth owned
classifier const there extends this rule with no test edit.

The transient/connection classifier in `adapters/postgres/classify.go` reads
`pgErr.Code` but compares it to `40001` / `40P01` / class-prefix `08` — codes
pkg/pgquery does **not** own. That is a *separate* single source (one copy, not
duplicated) and is correctly outside the owned set, so **no carve-out is
required**. This is why SQLSTATE-SINGLE-SOURCE-01 deliberately does **not**
reuse the `ERRCODE-KIND-LITERAL-01` carve-out registry in ADR
`202605121800-adr-archtest-carveout-narrow.md`: that registry's consistency
check (`ERRCODE-CARVEOUT-ADR-CONSISTENCY-01`) parses every row regardless of the
`Rule` column, so adding an unrelated row there would break it. Zero carve-outs
keeps the two rules fully decoupled.

### Companion positive-anchor self-check

`TestSQLStateSingleSource_SelfCheck` freezes the exact set of production files
permitted to read `pgconn.PgError.Code`:

| File | Role |
|---|---|
| `pkg/pgquery/sqlstate.go` | the single source for owned classifiers |
| `adapters/postgres/classify.go` | transient/connection classifier (separate single source: 40001 / 40P01 / class 08) |

Adding a third reader, or deleting/renaming either sanctioned reader, fails CI.
This anchor closes the value-scoped rule's blind spots (non-const RHS, switch
tag captured into a local) because *any* new `.Code` reader — in any AST form —
appears as a new file in the set.

## AI-robust grading

**Medium** (ai-robust.md §三档分级). Type-aware via `*types.Info` exact named-
type resolution + `EvaluateConstString` owned-set membership — form-unique, no
string anchor, no comment escape, no carve-out map. Archtest-bound, not
compile-time: Go cannot forbid reading an exported struct field, so the ceiling
is the same as `PANIC-REGISTERED-01` /
`ADAPTER-ERROR-CLASSIFICATION-TRANSIENT-01` (form-uniqueness + fail-on-deviation
in CI). Blind spots BS-1..BS-3 and their compensating positive-anchor self-check
are enumerated verbatim in the test package godoc.

Hard terminal state (not pursued now — over-engineering at current scale): wrap
every owned SQLSTATE classification behind a sealed pkg/pgquery API and forbid
`pgconn` imports outside pkg/pgquery + the transient classifier. Tracked
implicitly by the self-check anchor; revisit if a third legitimate `.Code`
reader is ever justified.

## Consequences

- A new cell-private PG adapter that re-implements `isUniqueViolation` /
  `isForeignKeyViolation` / a P0001 check fails CI immediately with an
  actionable message pointing at the `pgquery.Is*` helpers.
- `pkg/pgquery` is now also the single source for the P0001 (RAISE EXCEPTION)
  classification via `IsRaiseException`.
- No carve-out registry coupling; SQLSTATE-SINGLE-SOURCE-01 and
  ERRCODE-KIND-LITERAL-01 remain independent.

## References

- `tools/archtest/sqlstate_single_source_test.go`
- `tools/archtest/internal/sqlstatesinglesourcefixture/fixture.go`
- `pkg/pgquery/sqlstate.go`
- ai-robust.md §三档分级, §"工具选定后强制盲区自检"
- PR #578 post-merge review (P1 + dead-const P2 findings)
