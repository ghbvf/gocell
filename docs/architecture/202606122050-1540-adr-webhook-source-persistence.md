# ADR: Webhook source secret persistence — dedicated global encrypted store

- Status: Accepted
- Date: 2026-06-12
- Context: KERNEL-WEBHOOK-01 follow-up (#1540; PR-6 closeout #1538 carve)
- Supersedes / amends: none (greenfield); the SourceStore seam was reserved as a
  follow-up in `202605291200-adr-webhook-signing-algorithm.md`

## Context

`kernel/webhook.SourceStore.Lookup(id) (Source, bool)` is the secret-lookup seam
the receiver (verify) and dispatcher (sign) hit on every delivery. Its only
implementation was the in-memory `SourceRegistry` (eager-registered at startup,
immutable at runtime); secrets were hardcoded at the composition root
(`examples/webhookdemo`). #1540 asks for a durable, encrypted backing store so a
production deployment does not carry HMAC secrets in code.

Three constraints framed the design:

1. **The seam stays as-is.** `Lookup(id)` is synchronous, has no `ctx`, no
   `error`, and **no tenant** — it is a runtime-hot read. The issue scope is
   "swap the implementation behind the interface", so changing the kernel
   signature (e.g. tenant-scoping it) is out of scope. Webhook sources therefore
   remain **platform-global** (a sender such as "github" is one source for the
   whole deployment).

2. **configcore is strictly tenant-scoped with no platform tenant.** Every
   `configcore.ConfigRepository` method demands a real per-tenant UUID, and the
   `SystemTenantID` sentinel was deliberately removed (`pkg/tenant/tenant_id.go`).
   Routing global webhook secrets through configcore's entry model would mean
   inventing a synthetic platform tenant — fighting that prior decision — plus a
   boot-time cross-cell read.

3. **The reusable, security-critical part is the crypto, not configcore's entry
   model.** `kernel/crypto.ValueTransformer` (AES-GCM envelope) over
   `adapters/vault.TransitKeyProvider` / local-AES, with **caller-supplied AAD**,
   is what gives encryption-at-rest. configcore is merely one consumer of it.

## Decision

1. **A dedicated, global `webhook_sources` table** (migration 063), NOT a
   configcore entry. No `tenant_id`, no RLS — the same posture as the global
   `outbox_entries` relay. Secrets are encrypted-at-rest by reusing the SAME
   `kernel/crypto.ValueTransformer` (vault-transit / local-aes) configcore uses;
   no synthetic platform tenant, no cross-cell coupling.

2. **Eager-load-into-snapshot, immutable at runtime.** A composition-root loader
   (`cellmodules/webhooksource.LoadSourceStore`) reads every row at boot,
   decrypts each secret, and populates a `kwh.SourceRegistry` snapshot injected
   via `bootstrap.WithWebhookSourceStore`. The hot `Lookup` path is unchanged —
   persistence is about *where secrets come from at boot*, not runtime mutability.
   Rotation / add / **delete** = write or remove the row + restart: the loader
   builds an immutable snapshot at boot, so a `SourceRepo.Delete` only removes the
   persisted row and does **not** hot-revoke a source already live in the running
   `SourceRegistry`. Every mutation — add, rotate, *and delete* — takes effect
   only on the next restart (matches the issue's "运行时不可变").

3. **A bidirectional sealed crypto funnel in `kernel/webhook`.** `Source.Encrypt`
   and `NewSourceFromCiphertext` seal/unseal the secret. The plaintext is never a
   loose returnable value outside the package — `Source` keeps its secret
   unexported with no getter (**Hard**). It *is* observed by the caller-supplied
   `ValueTransformer` those methods run, so "no in-process code sees the plaintext"
   is a composition-root **trust** property (**Medium**), not a package boundary —
   locked by `WEBHOOK-SOURCE-CRYPTO-FUNNEL-01` to the sole sanctioned caller. The
   persistence repo (`runtimewebhook.SourceRepo`, implemented by
   `adapters/postgres.WebhookSourceRepository`) deals exclusively in the sealed
   `kwh.Source` type and ciphertext columns — never a loose `[]byte` secret. The
   AAD that binds each ciphertext to its `source_id` (`cell:webhook/source:{id}`)
   is computed inside the funnel from the sealed id, so the repo cannot pass a
   wrong AAD.

4. **Write path this PR = repo CRUD (Upsert / LoadAll / Delete) + boot loader.**
   Secrets are provisioned out-of-band (e.g. a provisioning tool calling
   `repo.Upsert(kwh.NewSource(...))`). A self-service HTTP admin endpoint
   (contract + authz) is a tracked follow-up — it is a separable contract/authz
   surface, not part of the persistence increment.

## Security model (AI-robust grading)

Encryption-at-rest rests on schema-shape + cryptography + sealed value (Hard)
plus two Medium machine guards. This is an honest split: PR #1929 review (C1/C2)
corrected an earlier over-grade that called the plaintext-funnel and the
column-set freeze Hard when neither was actually enforced.

| Invariant | Carrier | Grade |
|-----------|---------|-------|
| A webhook secret must be encrypted at rest | `webhook_sources` has **no plaintext column** (`value_cipher BYTEA NOT NULL`); `schema_guard.verifyFrozenColumnSets` freezes the column set **exactly** — any live column not in the registered expected set fails `ErrAdapterPGSchemaShape` (a positive column check alone does NOT catch an added `value`/`secret`; the exact-set check does, with an integration red test) | **Hard** (schema-shape exact freeze) |
| Cross-source / cross-cell ciphertext transplant rejected | AAD computed by the kernel from the sealed id (`cell:webhook/source:{id}`, distinct from configcore's domain), AES-GCM tag fails closed | **Hard** (cryptographic + unrepresentable wrong AAD) |
| Secret never logged / to wire | existing `Source.LogValue/String/GoString` redaction + `WEBHOOK-HMAC-FUNNEL-01` | **Hard** (existing) |
| Plaintext secret is never a loose returnable value | sealed `Source` (unexported `secret`, no getter); the only bytes entry is the validated minter `NewSource`; no API returns the raw secret | **Hard** (type-system: unrepresentable as a returnable value) |
| Encryption observes the plaintext only inside the trusted transformer | `Source.Encrypt` / `NewSourceFromCiphertext` take a **caller-supplied** `ValueTransformer`, so any holder of a `Source` could pass a capturing transformer — "no in-process code sees the plaintext" is a composition-root *trust* property, not a type-system one. The sole sanctioned caller is the persistence repo, locked by `WEBHOOK-SOURCE-CRYPTO-FUNNEL-01` (caller-allowlist); a future package wiring its own transformer trips CI | **Medium** (caller-allowlist; permanent Go ceiling — an in-process adversary can always implement the leaf `ValueTransformer` / `KeyHandle` interface, so this cannot be type-system Hard — same ceiling family as `WEBHOOK-HMAC-FUNNEL-01/A3-A4`) |
| Postgres persistence never writes plaintext | loader fail-closed (nil key provider → error); `NewWebhookSourceRepository` **rejects** a `runtime/crypto.NoopTransformer` (passthrough), so a non-nil pass-through cannot write `value_cipher = plaintext` | **Medium** (runtime guard, enforced — previously only narrated) |

Two new machine guards land with this PR: `WEBHOOK-SOURCE-CRYPTO-FUNNEL-01`
(`tools/archtest`, the caller-allowlist on the crypto funnel, with anti-vacuity +
RED fixture) and `schema_guard.verifyFrozenColumnSets` (exact-column-set freeze +
integration red test). The Hard rows remain locked by schema-shape, type-system,
and cryptography; the Medium rows are honestly graded with their enforcing guard
named (per the AI-robust charter — Soft is never used as a new mechanism).

## Alternatives considered

- **configcore entries under a platform tenant** — max reuse, but invents a
  synthetic platform tenant (fights the removed `SystemTenantID`) and adds a
  boot-time cross-cell read. Rejected.
- **Vault KV direct** — cleanest "secret manager" story, but the vault adapter is
  Transit-only (would need a new KV client) and makes Vault a hard dependency for
  any persistence. Rejected; vault-transit is still available as the KMS *behind*
  the dedicated store.

## Key-provider wiring

`cellmodules/webhooksource` builds its key provider from `GOCELL_WEBHOOK_*` env,
mirroring `cellmodules/configcore`'s factory rather than sharing it: there are
only two consumers, and the go-standards "重复三次才抽" guideline defers
extraction to a third. The vault-transit branch reuses configcore's vault metric
names; the metrics provider returns the already-registered collectors on a
duplicate name, so running both configcore and webhook on vault-transit is safe.

## Consequences

- Webhook secrets persist encrypted; a restart re-loads them — no code-resident
  secrets in PG-backed deployments.
- `cmd/corebundle` declares no webhook receivers today, so the loader ships as a
  tested, public composition helper (not force-wired into corebundle) ready for a
  webhook-serving assembly; the repo's encrypt/decrypt/AAD roundtrip is proven by
  an integration test against real PostgreSQL + local-AES.
- Follow-up: a self-service webhook-source admin HTTP endpoint (CRUD + authz),
  tracked as a separate backlog issue.
