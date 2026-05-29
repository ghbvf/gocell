# External Repository Quickstart (Operator-SDK 模式)

> **范围**：本文档展示在**自己的 Go module 内**开发 Cell 的最小步骤。完整 starter repo + 真实业务样板属于 M11 (#1092) 范围；本 quickstart 只覆盖 M1 落地后已经跑得通的最小闭环：让 `gocell validate` / `gocell check` 在外部仓库识别你的 cell.yaml / slice.yaml / contract.yaml。
>
> 完整的双模式产品方向背景见 ADR `docs/architecture/202605281200-adr-cell-development-external-repo.md`。

## 前置

- Go 1.22+
- `go install github.com/ghbvf/gocell/cmd/gocell@latest`（M7 #1088 之前用 `go install ...@develop`）

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

## 已知限制（M1 范围）

| 限制 | 影响 | 解锁条件 |
|------|------|---------|
| archtest 不能 import 进外部仓库做 nightly 守卫 | 外部仓库自定 invariant 需自己写 | M3 #1084 |
| `CellModule` 接口在 `cmd/` 包内私有 | 外部仓库无法 wire 进 corebundle | M4 #1085 |
| 没有 starter repo template | 上面步骤全手抄 | M11 #1092 |

详细路线见 #1081 与 ADR `docs/architecture/202605281200-adr-cell-development-external-repo.md`。

## 反馈

外部仓库开发遇到的卡点请直接对 [#1081](https://github.com/ghbvf/gocell/issues/1081) 评论，或为具体 M2-M12 子 issue 提 PR。
