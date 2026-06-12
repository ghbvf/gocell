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
   Rotation / add = write the row + restart (matches the issue's "运行时不可变").

3. **A bidirectional sealed crypto funnel in `kernel/webhook`.** `Source.Encrypt`
   and `NewSourceFromCiphertext` seal/unseal the secret; the plaintext bytes
   never leave the package. The persistence repo (`runtimewebhook.SourceRepo`,
   implemented by `adapters/postgres.WebhookSourceRepository`) deals exclusively
   in the sealed `kwh.Source` type and ciphertext columns — never a loose
   `[]byte` secret. The AAD that binds each ciphertext to its `source_id`
   (`cell:webhook/source:{id}`) is computed inside the funnel from the sealed id,
   so the repo cannot pass a wrong AAD.

4. **Write path this PR = repo CRUD (Upsert / LoadAll / Delete) + boot loader.**
   Secrets are provisioned out-of-band (e.g. a provisioning tool calling
   `repo.Upsert(kwh.NewSource(...))`). A self-service HTTP admin endpoint
   (contract + authz) is a tracked follow-up — it is a separable contract/authz
   surface, not part of the persistence increment.

## Security model (AI-robust grading)

Encryption-at-rest is enforced **Hard** (violations unrepresentable), not by a
new bespoke scan:

| Invariant | Carrier | Grade |
|-----------|---------|-------|
| A webhook secret must be encrypted at rest | `webhook_sources` has **no plaintext column** (`value_cipher BYTEA NOT NULL`, no `value`); `schema_guard.go` freezes the column set — adding a plaintext column trips `SCHEMA-GUARD-COVERS-EVERY-OWNED-TABLE-01`/shape verify | **Hard** (schema-shape freeze) |
| Plaintext secret bytes never leave kernel/webhook | sealed `Source` + `Source.Encrypt` / `NewSourceFromCiphertext`; no API returns the raw secret, the only bytes entry is the validated minter `NewSource`; repo/composition handle only ciphertext | **Hard** (type-system funnel) |
| Cross-source / cross-cell ciphertext transplant rejected | AAD computed by the kernel from the sealed id (`cell:webhook/source:{id}`, distinct from configcore's domain), AES-GCM tag fails closed | **Hard** (cryptographic + unrepresentable wrong AAD) |
| Secret never logged / to wire | existing `Source.LogValue/String/GoString` redaction + `WEBHOOK-HMAC-FUNNEL-01` | **Hard** (existing) |
| Postgres persistence requires a transformer | loader fail-closed (nil key provider → error); no `NoopTransformer` path for webhook secrets | Medium (runtime guard) |

No new archtest invariant is added: the three load-bearing properties are locked
by schema-shape + type-system + cryptography (the AI-robust charter's top-priority
carriers), which is stronger than a scan.

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
