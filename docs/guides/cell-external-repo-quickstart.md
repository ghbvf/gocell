# External Repository Quickstart (Operator-SDK 模式)

> **范围**：本文档展示在**自己的 Go module 内**开发 Cell 的最小步骤。完整 starter repo + 真实业务样板属于 M11 (#1092) 范围；本 quickstart 只覆盖 M1 落地后已经跑得通的最小闭环：让 `gocell validate` / `gocell check` 在外部仓库识别你的 cell.yaml / slice.yaml / contract.yaml。
>
> 完整的双模式产品方向背景见 ADR `docs/architecture/202605281200-adr-cell-development-external-repo.md`。

## 前置

- Go 1.25.11+
- `gocell` CLI：M7 #1088 发布独立安装入口前，先从本仓源码安装：

  ```bash
  git clone https://github.com/ghbvf/gocell.git
  cd gocell
  go install ./cmd/gocell
  ```

  `go install github.com/ghbvf/gocell/cmd/gocell@latest` 需要等 #1088
  补齐版本化发布策略后再作为稳定入口。

## 步骤

### 1. 初始化 module

```bash
mkdir -p ~/work/acme-payment-cell && cd ~/work/acme-payment-cell
go mod init github.com/acme/payment-cell
go get github.com/ghbvf/gocell@develop
```

### 2. 放 manifest 文件

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

Locator 探测到 `.gocell/manifest.yaml` 即自动切到 manifest 模式（auto-detect）。

### 3. 声明 cell + slice + contract

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
belongsToCell: payment        # 必填（manifest 模式无路径自动派生）
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

### 4. 跑 gocell validate

```bash
# 必须在 module root（go.mod / .gocell/manifest.yaml 所在目录）运行；或用 --root=/path/to/repo 显式指定
gocell validate
```

预期：退出码 0，stderr/stdout 中出现 `INFO metadata: locator mode resolved mode=manifest`，stdout 输出 `No issues found.` 或仅 advisory warnings；governance rules 在 conventional-mode-specific 规则上自动 skip（因 `examples/` 子树在外部 repo 不存在）。

> **说明**：外部仓库无 `examples/` 子树，conventional-layout-specific 规则（如 ADV-04 examples 反向覆盖）在 manifest 模式下自动 skip；详见 ADR § AI-robust 评级。

需要显式 override 时：

```bash
gocell validate --layout=manifest
gocell validate --layout=manifest --manifest=./config/manifest.yaml
```

> 跑通上方 `gocell validate` 即表示 M1 (#1082) acceptance criteria 已达到，后续能力见下方已知限制表。

### 5. Workspace 模式

如需同时联调 gocell 自身 + 业务 cell module，用 Go workspace。Workspace 根目录是包含 `go.work` + `.gocell/manifest.yaml` 的目录；所有 `modules[].path` 必须**相对该目录** 且不含 `..`（Locator 会拒绝 `../foo` 形式的 module path，逃逸保护）。

```bash
mkdir -p ~/work/platform-workspace && cd ~/work/platform-workspace
# Clone or symlink dependencies inside the workspace root (NOT siblings)
git clone https://github.com/ghbvf/gocell.git ./gocell
git clone https://github.com/acme/payment-cell.git ./acme-payment-cell
go work init ./gocell ./acme-payment-cell
```

workspace 根放 `.gocell/manifest.yaml`：

```yaml
version: v1
modules:
  - path: gocell                     # workspace 根的子目录，持 actors / status-board singleton
  - path: acme-payment-cell          # 第二个 module
```

> manifest 的 `path:` 字段不需要 `./` 前缀；`path: gocell` 等价于 `./gocell`，Locator 内部 `path.Clean` 会规范化。

`gocell validate --root=.` 会聚合两个 module 的 cell/slice/contract。预期退出码 0，输出末尾包含 `No issues found.` 或仅 advisory warnings。

### 6. Migrations（per-namespace，M8 #1089）

外部 Cell module 自带数据库 migration 时，**不**共享平台的全局版本空间——每个 namespace 独立追踪，互不撞号。

**写法约定**：

- migration 文件用 goose-native `NNN_desc.sql` 命名（自己的 `001..N` 序列），**不要**在文件名里加 namespace 前缀。`platform_001_x.sql` 这类前缀会让 goose 的版本解析器（`NumericComponent` = `ParseInt(strings.Cut(name,"_")[0])`）失败；namespace 活在**追踪表名**里，不在文件名里。⚠️ **非 `NNN_` 前缀的 `.sql` 会被 goose 静默跳过、不被应用**（goose 默认非严格收集），所以务必遵守该命名。平台自身的 migration 由 archtest `MIGRATION-FILENAME-GOOSE-PARSEABLE-01` 守，但该规则**只扫 `adapters/postgres/migrations/`**——外部仓库目前不在其覆盖内，需自行遵守约定（随 M3 archtest library #1302 迁入外部规则集后可机器守）。
- 用 `embed.FS` 嵌入自己的 `migrations/*.sql`。
- 经 `composition.WithMigrations(ns, fs)` 注册，`ns` 是 `pkg/migration.Namespace`（`migration.ParseNamespace("yourcell")`，小写标识符，长度 ≤ 45）。
- 该 namespace 的 migration 追踪在独立的 `schema_migrations_<namespace>` 表；平台自身是保留 namespace `"platform"`（追踪表 `schema_migrations_platform`），外部 module **不可**注册 `"platform"`。

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

**执行桥**：`composition.Build()` 本身**不**跑 migration（cell 启动前 schema 必须已就位，且 `runtime/` 不依赖 `adapters/`）。在 composition root（可同时 import 两层）里，于 `Build` **之前**把注册的 migration set drain 进 `adapters/postgres.MigrationSet` 并应用——`NewMigrationSetWithPlatform` 已把平台 namespace 排在最前（platform-first 顺序保证：外部 cell 的 migration 可以 FK 平台表）：

```go
set, err := adapterpg.NewMigrationSetWithPlatform() // seeds "platform" first
if err != nil { return err }
for _, r := range builder.Migrations() {
    if err := set.Add(r.Namespace, r.FS); err != nil { return err }
}
if err := set.ApplyAll(ctx, pool); err != nil { return err } // or set.VerifyAll(ctx, pool) in prod

app, err := builder.Build(ctx, shared, runtimeOpts)
```

## 已知限制（M1 范围）

| 限制 | 影响 | 解锁条件 |
|------|------|---------|
| 平台标准规则（PANIC-REGISTERED-01 / ERRCODE-KIND-LITERAL-01 / MESSAGE-CONST-LITERAL-01 / EXPORTED-ERROR-NEW-01 / SCAFFOLD-DERIVED-FORCEOVERWRITE-01）已可通过 `archtest.RunStandardCellRules` 在外部仓库直接使用；外部自定规则通过 `cfg.ExtraRules` 追加。**精确集合以代码 `StandardCellRules()` + `TestStandardCellRulesComposition` 为单源**（本表手写枚举仅供参考，以代码为准） | 仅限上述平台规则；reason about GoCell 自身内部布局的规则（ERROR-FIRST-API-01 / ERROR-FIRST-TYPED-NIL-01 / DETAILS-SEALED-FIELD-FROZEN-01 / SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01）刻意不注册（外部 vacuous，见 external.go StandardCellRules godoc） | M3 #1084 已部分落地，剩余规则迁移追踪 #1302 |
| `CellModule` 接口在 `cmd/` 包内私有 | 外部仓库无法 wire 进 corebundle | M4 #1085 |
| 没有 starter repo template | 上面步骤全手抄 | M11 #1092 |

详细路线见 #1081 与 ADR `docs/architecture/202605281200-adr-cell-development-external-repo.md`。

## 反馈

外部仓库开发遇到的卡点请直接对 [#1081](https://github.com/ghbvf/gocell/issues/1081) 评论，或为具体 M2-M12 子 issue 提 PR。
