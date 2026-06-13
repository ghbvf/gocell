# External Repository Quickstart (Operator-SDK Mode)

> **Scope**: This document shows the minimal steps to develop a Cell inside your own Go module. A complete starter repo with real-world business boilerplate is not yet available; this quickstart covers only the minimal end-to-end flow that already works: making `gocell validate` / `gocell check` recognize your `cell.yaml` / `slice.yaml` / `contract.yaml` in an external repository.
>
> Full background on the dual-mode product direction is in ADR `docs/architecture/202605281200-adr-cell-development-external-repo.md`.

## Prerequisites

- Go 1.25.11+
- `gocell` CLI: install from this repo's source (the only available entry point at present):

  ```bash
  git clone https://github.com/ghbvf/gocell.git
  cd gocell
  go install ./cmd/gocell
  ```

  The framework itself is a public module; `go get github.com/ghbvf/gocell@v0.1.0` works without GOPRIVATE. #1843 adds per-module release tags for every **library** satellite (`adapters/*`, `corecells`, `cellmodules`; `tools` is published for toolchain consumers such as external `archtest` users, not business Cells), synchronized to the same `vX.Y.Z` as the root — so `go get github.com/ghbvf/gocell/adapters/postgres@vX.Y.Z` resolves (see §6). These satellite tags exist **from the first stable release that ships #1843 onward** (the root-only `v0.1.0` predates it and has no satellite tags). `go install github.com/ghbvf/gocell/cmd/gocell@vX.Y.Z` is still **not** available — `cmd/gocell` is a `go install` binary, and `go install pkg@version` is rejected outright when the target module's `go.mod` carries the local `replace` directive it needs for monorepo development. #1843 deliberately excludes the `cmd/*` modules for exactly this reason; making the CLI installable at a version needs release-time replace-strip, tracked as **#1088**. Install from source (above) for now.

- **Verify CLI ↔ framework version compatibility**: before running `gocell validate`, confirm the
  installed CLI is compatible with the framework version your project depends on:

  ```bash
  gocell version
  # Example output:
  #   cli_version:                v0.1.0
  #   framework_version:          v0.1.0
  #   compatible_framework_range: >=v0.1.0 <v0.2.0

  go list -m github.com/ghbvf/gocell
  # Confirm the framework version falls within compatible_framework_range above
  ```

  See `docs/guides/cli-version-compatibility.md` for the full compatibility matrix and version
  policy (v0.x = best-effort intent; v1.0 GA = SemVer hard guarantee).

## Steps

### 1. Initialise the Module

```bash
mkdir -p ~/work/acme-payment-cell && cd ~/work/acme-payment-cell
go mod init github.com/acme/payment-cell
go get github.com/ghbvf/gocell@vX.Y.Z   # pin a stable tag (see Releases; @develop = prerelease snapshot)
# Synchronized-version contract: pin every library satellite you import at the
# SAME vX.Y.Z as the core, in one command — Go MVS otherwise resolves a satellite's
# internal require to a different version and the gocell module graph won't line up:
go get github.com/ghbvf/gocell@vX.Y.Z github.com/ghbvf/gocell/adapters/postgres@vX.Y.Z
go list -m all | grep ghbvf/gocell   # verify every gocell module sits at vX.Y.Z
```

### 2. Place the Manifest File

```bash
mkdir -p .gocell
cat > .gocell/manifest.yaml <<'EOF'
version: v1
modules:
  - path: .
    excludes:
      - "generated/**"
      - "vendor/**"
EOF
```

Locator switches to manifest mode automatically (auto-detect) when it finds `.gocell/manifest.yaml`.

### 3. Declare the Cell, Slice, and Contract

```bash
mkdir -p cells/payment/slices/charge
mkdir -p contracts/http/payment/charge/v1
```

`cells/payment/cell.yaml`:

```yaml
id: payment
type: core
consistencyLevel: L2
durabilityMode: demo
lifecycle: experimental
owner:
  team: acme-platform
  role: payment-cell-owner
schema:
  primary: payments
verify:
  smoke:
    - smoke.payment.charge
```

`cells/payment/slices/charge/slice.yaml`:

```yaml
id: charge
belongsToCell: payment        # required (no path-based auto-derivation in manifest mode)
consistencyLevel: L2
allowedFiles:
  - "cells/payment/slices/charge/**"
contractUsages:
  - contract: http.payment.charge.v1
    role: serve
verify:
  unit:
    - unit.payment.charge
  contract: []
  waivers:
    - contract: http.payment.charge.v1
      owner: acme-platform
      reason: "contract still in draft lifecycle; no executable contract test yet"
      expiresAt: "2026-12-31"
```

`contracts/http/payment/charge/v1/contract.yaml`:

```yaml
id: http.payment.charge.v1
kind: http
lifecycle: draft
ownerCell: payment
consistencyLevel: L1
endpoints:
  server: payment
  http:
    method: POST
    path: /api/v1/payment/charge
    successStatus: 204
    noContent: true
```

### 4. Run gocell validate

```bash
# Must be run from the module root (directory containing go.mod / .gocell/manifest.yaml);
# or use --root=/path/to/repo to specify explicitly
gocell validate
```

Expected: exit code 0, stderr/stdout contains `INFO metadata: locator mode resolved mode=manifest`, stdout outputs `No issues found.` or advisory warnings only; governance rules that are specific to the conventional layout are automatically skipped (because the `examples/` subtree does not exist in an external repo).

> **Note**: External repositories have no `examples/` subtree; conventional-layout-specific rules (such as ADV-04 examples reverse coverage) are automatically skipped in manifest mode. See ADR § AI-robust rating for details.

When an explicit override is needed:

```bash
gocell validate --layout=manifest
gocell validate --layout=manifest --manifest=./config/manifest.yaml
```

> Successfully running the `gocell validate` above means the acceptance criteria for external repository support have been reached. See the known limitations table below for remaining capabilities.

### 5. Workspace Mode

To develop against both gocell itself and a business cell module simultaneously, use a Go workspace. The workspace root is the directory containing `go.work` + `.gocell/manifest.yaml`; all `modules[].path` entries must be **relative to that directory** and must not contain `..` (Locator rejects `../foo`-style module paths as an escape-protection measure).

```bash
mkdir -p ~/work/platform-workspace && cd ~/work/platform-workspace
# Clone or symlink dependencies inside the workspace root (NOT siblings)
git clone https://github.com/ghbvf/gocell.git ./gocell
git clone https://github.com/acme/payment-cell.git ./acme-payment-cell
go work init ./gocell ./acme-payment-cell
```

Place `.gocell/manifest.yaml` at the workspace root:

```yaml
version: v1
modules:
  - path: gocell                     # subdirectory of the workspace root; holds actors / status-board singleton
  - path: acme-payment-cell          # second module
```

> The `path:` field in the manifest does not require a `./` prefix; `path: gocell` is equivalent to `./gocell` — Locator normalizes it internally with `path.Clean`.

`gocell validate --root=.` aggregates cells/slices/contracts from both modules. Expected exit code 0; output ends with `No issues found.` or advisory warnings only.

### 6. Migrations (Per-Namespace)

When an external Cell module carries its own database migrations, it does **not** share the platform's global version space — each namespace tracks versions independently with no numbering conflicts.

**Naming convention**:

- Migration files use goose-native `NNN_desc.sql` naming (your own `001..N` sequence); **do not** add a namespace prefix to the filename. Prefixes like `platform_001_x.sql` cause goose's version parser (`NumericComponent` = `ParseInt(strings.Cut(name,"_")[0])`) to fail; the namespace lives in the **tracking table name**, not the filename. ⚠️ **`.sql` files that do not start with `NNN_` are silently skipped by goose and never applied** (goose defaults to non-strict collection), so this naming convention is mandatory. The platform's own migrations are guarded by archtest `MIGRATION-FILENAME-GOOSE-PARSEABLE-01`, but that rule **only scans `adapters/postgres/migrations/`** — external repositories are not currently in its scope and must follow the convention on their own.
- Embed your own `migrations/*.sql` with `embed.FS`.
- Register via `composition.WithMigrations(ns, fs)`, where `ns` is a `pkg/migration.Namespace` (`migration.ParseNamespace("yourcell")`, lowercase identifier, length ≤ 45).
- This namespace's migrations are tracked in a dedicated `schema_migrations_<namespace>` table; the platform itself uses the reserved namespace `"platform"` (tracking table `schema_migrations_platform`); external modules **must not** register `"platform"`.

```go
//go:embed migrations/*.sql
var paymentMigrations embed.FS

paymentNS, err := migration.ParseNamespace("payment")
if err != nil { return err }
paymentFS, err := fs.Sub(paymentMigrations, "migrations")
if err != nil { return err }

builder := composition.New(cellIDs...).
    With(platformModules..., paymentcell.Module()).
    WithMigrations(paymentNS, paymentFS)
```

**Execution bridge**: `composition.Build()` itself does **not** run migrations (the schema must be in place before cells start, and `runtime/` must not depend on `adapters/`). In the composition root (which can import both layers), drain the registered migration set into `adapters/postgres.MigrationSet` and apply it **before** `Build` — `NewMigrationSetWithPlatform` places the platform namespace first (platform-first ordering guarantees: external cell migrations can FK platform tables):

> **Note (per-adapter module split #1558 / external publishing #1843)**: `adapters/postgres` — like every `adapters/*` — is a **separate satellite module** (`github.com/ghbvf/gocell/adapters/postgres`), no longer part of the root `github.com/ghbvf/gocell` module. As of #1843 the release pipeline tags every library satellite per module (`adapters/postgres/vX.Y.Z`), **synchronized to the same `vX.Y.Z` as the root** — so `go get github.com/ghbvf/gocell/adapters/postgres@vX.Y.Z` now resolves directly. **Pin it at the same `vX.Y.Z` you use for the core `go get github.com/ghbvf/gocell@vX.Y.Z`** (the synchronized-release contract holds every gocell module at one version). Note that `go get github.com/ghbvf/gocell@vX.Y.Z` alone does **not** pull `adapters/postgres` — it is a distinct module; add it explicitly. The Go workspace / local `replace` route (Workspace Mode, §5 above) remains available as a dev-time convenience but is no longer required for consumption.

```go
set, err := adapterpg.NewMigrationSetWithPlatform() // seeds "platform" first
if err != nil { return err }
for _, r := range builder.Migrations() {
    if err := set.Add(r.Namespace, r.FS); err != nil { return err }
}
if err := set.ApplyAll(ctx, pool); err != nil { return err } // or set.VerifyAll(ctx, pool) in prod

app, err := builder.Build(ctx, shared, runtimeOpts)
```

## Known Limitations

| Limitation | Impact | Unlock condition |
|-----------|--------|-----------------|
| Standard platform rules (`PANIC-REGISTERED-01` / `ERRCODE-KIND-LITERAL-01` / `MESSAGE-CONST-LITERAL-01` / `EXPORTED-ERROR-NEW-01` / `SCAFFOLD-DERIVED-FORCEOVERWRITE-01`) can already be used in external repositories via `archtest.RunStandardCellRules`; custom rules are added via `cfg.ExtraRules`. **The exact set is defined in code at `StandardCellRules()` + `TestStandardCellRulesComposition`** (this table is for reference only; code is authoritative) | Limited to the above platform rules; rules that reason about GoCell's own internal layout (`ERROR-FIRST-API-01` / `ERROR-FIRST-TYPED-NIL-01` / `DETAILS-SEALED-FIELD-FROZEN-01` / `SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01`) are deliberately excluded (vacuous in external repos; see `external.go` `StandardCellRules` godoc) | Partial landing complete; remaining rule migration tracked separately |
| Stock corebundle does not dynamically load third-party modules | External repositories must build their own composition root and call exported `runtime/composition` module APIs explicitly | Starter repo / template support |
| No starter repo template | The steps above must be followed manually | Planned future work |

See ADR `docs/architecture/202605281200-adr-cell-development-external-repo.md` for detailed roadmap.

## Feedback

If you hit blockers developing in an external repository, open an issue or submit a PR against the relevant tracking issue.
