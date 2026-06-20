# ADR: RowScope=all FR-007 audit signal — slog, not ledger.Append (#1761)

- Status: Accepted (2026-06-20)
- Epic: #1337 (tenancy)
- Related: #1810 (super-admin cross-tenant audit read — scoped this signal choice
  OUT), #1760 (sealed `RowScopeAll` construction), PR #1759 / #1343 (PR-5, FR-007
  slog landed), #1755 (startup full-chain verify)

## Context

PR-5 (#1759) landed the framework derivation `(*auth.Principal).CrossTenantVisibility(ctx)`
— the **sole production mint** of `tenant.RowScopeAll` — with a mandatory FR-007
`slog.Error` audit (`actor` / `scope="all"` / `tenant` / `reason="cross_tenant_read"`)
emitted **immediately before** constructing the sealed cross-tenant obligation
(`framework/runtime/auth/rowscope.go:158-164`). #1761 was carved out of PR-5 to decide
whether that FR-007 audit should instead (or additionally) be a **tamper-evident
hash-chain row** via `ledger.Store.Append`.

Three facts frame the decision:

1. **FR-007 does not require tamper-evidence.** `spec.md:144`: super-admin
   `RowScope=all` "MUST 是显式独立决策路径并强制审计" — an explicit, mandatory,
   observable grant audit. Tamper-evidence / hash-chain / ledger appear nowhere in the
   requirement.
2. **The implementing task lists slog as an equal satisfier.** `tasks.md:140` (T5.2):
   "…必须写审计——调 `ledger.Append`（**或**结构化 `slog.Error`…带 actor/tenant/reason）；
   acceptance 断言无 RowScope=all 请求能绕过审计写入". Both sinks satisfy T5.2 by
   construction; the acceptance is **no-bypass**, not **tamper-evidence**.
3. **#1810 pre-scoped #1761 as a signal-format choice.** ADR `202606131900-1810` lists
   "#1761 (slog-vs-ledger audit signal — OUT of scope here)" and "FR-007 slog is
   preserved here".

So #1761 is a **signal-format** decision, not a coverage gap. The no-bypass acceptance is
already satisfied (see §No-bypass acceptance).

## Decision

**`slog.Error` is the sanctioned FR-007 sink for the super-admin cross-tenant grant.
`ledger.Append` is NOT adopted — not deferred, but rejected as architecturally wrong for
this event class.** Tamper-evidence for grant-class audit events is a sink/SIEM-layer
concern, deliberately not inline.

Options weighed:

| Option | Verdict | Why |
|---|---|---|
| **A. Keep slog (ADOPTED)** | ✅ | Satisfies FR-007; no-bypass already machine-proven; zero new mechanism / path |
| C. Hybrid (slog + async ledger row) | ✗ | Permanent dual sink + new contract / topic / slice / namespace / HMAC key / wiring (~13–16 files, ≥ cx-3); async append is a fail-open path; buys inline tamper-evidence the spec does not require and industry does not apply to grants |
| B. Full migrate (delete slog) | ✗ | Loses the synchronous co-located guarantee the no-bypass funnel relies on; tenant-less grant cannot ride the tenant-scoped L1 business chain; couples authn derivation to persistence; adds a fail-open hole |

### Why every `ledger.Append` path crosses a clean boundary

- **Auth derivation layer has no tx / store.** The mint is `(*Principal).CrossTenantVisibility`,
  a method on a JWT-derived value type with no `clock.Clock`, no `txRunner`, no
  `ledger.Store`. `ledger.Store.Append` requires all three (L1 `RunInTx`). Wiring a write
  store into the authn derivation path inverts layering (authn is upstream of persistence)
  and makes obligation minting depend on a fallible DB write.
- **`auditquery` is compile-time read-only.** The read slice holds only `ledger.QueryStore`
  (`var _ QueryStore = Store(nil)`); it has no `Append` / emitter. Appending here destroys
  the L0 read-only boundary and the structural write-incapability guarantee.
- **The grant is tenant-less; `ledger.Append` is tenant-scoped L1.** Cross-tenant
  `scope="all"` is intrinsically tenant-less; the four business appenders `rejectEmptyTenant`.
  Only a bootstrap-style isolated namespace allows tenant-less rows — i.e. Option C's full
  apparatus.

### Open-source benchmark

For **authorization-grant / access-decision** events (vs **data-mutation ledger** events),
tamper-evidence is applied at the **sink / SIEM layer, asynchronously**, never inline:

- **Kubernetes audit** → policy + backend (file / webhook); no in-process hash chain.
- **AWS CloudTrail log-file-integrity-validation** → digest files hash-chained in S3,
  async, over *delivered* logs.
- **HashiCorp Vault audit devices** → integrity delegated to the destination (append-only
  file / SIEM).
- **SPIFFE/SPIRE** → SVID-issuance integrity is a function of the log destination.

Inline hash chains are reserved for mutation ledgers — exactly GoCell's existing `ledger`
for appended business audit *facts*, not for grant breadcrumbs.

### Zero-trust threat argument (decisive)

Under "assume breach," the FR-007 threat is a **compromised super-admin**. An inline
`ledger.Append` chain signs rows with an HMAC key co-located **in the same process** as that
super-admin: a host-compromised attacker holds the key, can rewrite the chain, and `Verify`
still passes. `slog.Error` shipped to an **external append-only / SIEM sink** moves the
evidence out of the compromised trust domain immediately. Inline ledger tamper-evidence only
narrows one threat — a DBA with `audit_entries` table access but no app secrets editing rows
post-hoc — which is **not** the FR-007 actor. **∴ for the relevant threat, external SIEM is
more zero-trust-aligned than an inline chain; slog is not merely simpler, it is safer.**

## AI-robust threat matrix

| Surface | Mechanism | Rating |
|---|---|---|
| `RowScopeAll` mint | `NewRowVisibility` rejects `RowScopeAll`; sealed `CrossTenantVisibility` (unexported field) — forge = compile error | **Hard** |
| Cross-tenant read gate | read API takes sealed `CrossTenantVisibility` positional param — forget = compile error | **Hard** |
| Audit precedes read | slog (`rowscope.go:158`) is lexically before `NewCrossTenantVisibility()` (`:164`) in the sole mint, with no early-return / branch between; the sealed obligation is the only key to the read | **Hard-by-construction** (sequential execution in a sealed funnel) |
| Minter caller-restriction | `ROWSCOPEALL-AUDIT-FUNNEL-01` allowlist + anti-vacuity; `pkg/tenant` cannot import `runtime/auth` (cycle) → not Go-expressible as Hard | **Medium** (permanent Go ceiling, not a Soft TODO) |
| Audit independent of admin-pool provisioning | `TestHandleQuery_SuperAdmin_SingleAuditRecord` / `TestHandleGetByID_SuperAdmin_SingleAuditRecord` assert exactly-1 FR-007 record on **both** the 200 and 501 paths | **Medium** regression lock |
| Reliable delivery to a tamper-evident sink | **NOT in-repo enforced** — operational control (ship slog to append-only / SIEM) | Operational (documented, out of repo scope) |

This decision adds **no new enforcement mechanism** and introduces **no Soft mechanism**; it
documents the existing Hard/Medium guarantees and the honest in-repo-vs-operational boundary.

## No-bypass acceptance — already satisfied

The acceptance "断言无 RowScope=all 请求能绕过审计写入" holds without new code:

- `ROWSCOPEALL-AUDIT-FUNNEL-01` (`tools/archtest/rowscopeall_audit_funnel_test.go`) pins the
  sole production minter co-located with the slog (allowlist + anti-vacuity).
- `framework/runtime/auth/rowscope_test.go` asserts the super-admin derivation emits exactly
  one Error record with all four fields, including the `TenantID==""` case.
- `corecells/auditcore/slices/auditquery/handler_test.go`
  (`TestHandleQuery_SuperAdmin_SingleAuditRecord`, `TestHandleGetByID_SuperAdmin_SingleAuditRecord`)
  assert exactly-1 FR-007 record on **both** the 200 (admin pool wired) and 501 (admin pool
  absent) paths — i.e. the audit is independent of admin-pool provisioning and of read
  outcome.

## Scope boundaries (explicit, terminal — no loose TODO)

- **IN:** the decision (slog is the FR-007 sink) + its rationale + citation of the existing
  no-bypass proof.
- **Terminal, not deferred:** `ledger.Append` for the cross-tenant *grant* is **rejected on
  architecture**, not postponed. There is no "migrate to ledger later" backlog item;
  re-opening requires a new, concrete trigger.
- **Revisit trigger (single, named):** a regulatory / compliance requirement that mandates
  **inline** tamper-evidence of authorization-grant events. Only then revisit Option C (never
  B).
- **OUT (unchanged, owned elsewhere):** tamper-evident chaining of the *data rows* a
  super-admin reads is already provided by the existing `ledger` + #1810 admin-pool serving +
  #1755 startup verify; #1761 does not touch it.

## Consequences

- No code change. The FR-007 sink remains `slog.Error` at the single audited mint.
- **Operational obligation:** operators ship `slog.Error` records to an append-only / SIEM
  sink; the grant event's integrity guarantee lives there, deliberately not inline. Residual:
  log loss between emission and ship is possible and accepted — the grant is a breadcrumb;
  the cross-tenant *reads* it gates are independently RLS-enforced and, when the admin pool is
  provisioned, served from the chain-verified ledger.

## References

- Audited mint: `framework/runtime/auth/rowscope.go:158-164` (slog at :158-163, before
  `NewCrossTenantVisibility` at :164; enclosing method `CrossTenantVisibility` :141-165)
- Sealed obligation: `framework/pkg/tenant/rowvisibility.go`
- No-bypass funnel: `tools/archtest/rowscopeall_audit_funnel_test.go` (ROWSCOPEALL-AUDIT-FUNNEL-01)
- Derivation FR-007 test: `framework/runtime/auth/rowscope_test.go`
- Handler exactly-1 FR-007 tests (200 + 501):
  `corecells/auditcore/slices/auditquery/handler_test.go`
- Spec: `docs/plans/specs/1220-tenancy-abac-dataperm/spec.md` FR-007 (:144);
  `docs/plans/specs/1220-tenancy-abac-dataperm/tasks.md` T5.2 (:140)
- Sibling ADRs: `202606131900-1810-adr-super-admin-cross-tenant-audit-read.md` (this signal
  scoped OUT), #1760 (sealed `RowScopeAll`)
- Rule index: `.claude/rules/gocell/tenancy.md`
