# PR #243 Kernel Guardian 审查

- PR: https://github.com/ghbvf/gocell/pull/243
- 分支: `228-cross-platform-bootstrap-redo` → `develop`
- 范围: `cells/accesscore/initialadmin/` 跨平台化 + `runtime/shutdown/` 信号按 OS 拆分 + CI os-smoke 矩阵
- HEAD: 82b03a6c
- 审查视角: GoCell 分层隔离 + 治理合规

## 审查结论

Ready to merge (Kernel Guardian 视角) — **PASS**。未发现必须修复项。

## 1. 分层隔离 — PASS

PR 仅动 `cells/accesscore/initialadmin/` 与 `runtime/shutdown/`；无反向依赖、无越层 import：

- `cells/accesscore/initialadmin/credfile_security_windows.go:6-9` 仅 import `fmt`, `os`, `golang.org/x/sys/windows`（官方标准扩展，非 `adapters/`）。
- `cells/accesscore/initialadmin/credfile_security_unix.go`、`path_default_{linux,darwin,windows,unsupported}.go`、`path_default.go` 全部只依赖 stdlib。
- 既有 internal 引用保持收敛：`bootstrap.go` 只引用 `cells/accesscore/internal/{domain,ports}`（同 Cell 内部），无跨 Cell 边界穿透（`grep gocell/cells\|gocell/adapters cells/accesscore/initialadmin/ runtime/shutdown/` 仅返回 accesscore 内部 internal/，符合“同 Cell 自包含”）。
- `runtime/shutdown/shutdown.go`、`signals_unix.go`、`signals_windows.go`、`signals_other.go`、`notify_context_test.go`、`shutdown_sigterm_unix_test.go` 均只依赖 stdlib + testify；未出现 `cells/`、`adapters/` 导入。分层方向 `cells → runtime` 正确，`runtime` 仍是叶子层。
- Build tag 切分干净：`signals_unix.go:1` `//go:build unix`、`signals_windows.go:1` `//go:build windows`、`signals_other.go:1` `//go:build !unix && !windows`，三者互斥全覆盖。Windows 侧仅注册 `os.Interrupt`，与 kratos 对标方案一致，解决了此前“Windows 上 SIGTERM 静默半残”问题。

## 2. 元数据合规 — PASS

真实运行结果（工作目录 `/Users/shengming/Documents/code/gocell`，HEAD=82b03a6c）：

```
$ go run ./cmd/gocell validate
WARNINGS (1):
  [REF-16] assembly "corebundle" has no generated boundary.yaml ...
Validation complete: 0 error(s), 1 warning(s)

$ go run ./cmd/gocell check contract-health
... PASS: all contracts healthy
```

- **REF-16 warning 是 pre-existing**（与本 PR 无关，属 `generate` 未跑；PR 自述也声明此项）。
- **本 PR 未触发任何新 error 或 warning**：`cells/accesscore/cell.go` 的 +1/-1 仅为注释文案（`git diff` 确认），不触发 `cell.yaml` 元数据变更；`cell.yaml` 零改动。
- **allowedFiles 覆盖核验**：`cells/accesscore/initialadmin/` 属 **cell 级目录（非 slice 子目录）**，FMT-14/FMT-17 (kernel/governance/rules_fmt.go:668, rules_strict.go:106) 仅约束 slice 目录内部文件所有权，对 cell 级支撑模块不强制归属。identitymanage/slice.yaml:40-41 的 `allowedFiles: [cells/accesscore/slices/identitymanage/**]` 保持 slice 边界清晰，未把 initialadmin 文件越界纳入。这与“initialadmin 是 Cell 启动期共享逻辑、不属于单一 slice 用例”的设计一致。**无需修改 slice.yaml**。
- **禁用动态字段核验**：`grep 'readiness:\|nextAction:\|verified:\|done:' cells/accesscore/*.yaml cells/accesscore/slices/*/*.yaml` 返回空。PR 未向任何 cell/slice yaml 注入违规字段。

## 3. 契约完整性 — PASS

`git diff develop..HEAD -- contracts/` 输出为空。本 PR 为实现层（bootstrap + shutdown）改造，未引入新契约、未修改既有契约。`contract-health` 全量扫描 PASS，证明 ownerCell / 角色拓扑 / 端点引用无退化。

## 4. 治理字段 — PASS

- 无新建 cell.yaml / slice.yaml / assembly.yaml / journey.yaml。
- 本 PR yaml 变更仅涉 `.github/workflows/*.yml`（CI 配置，不受 gocell 元数据约束）。
- `journeys/status-board.yaml` 未改动。

## Findings

无 Cx1/Cx2/Cx3 级问题。

以下属于范围外观察（不阻塞 merge）：

- **C-info-1** `assemblies/corebundle/generated/boundary.yaml` 长期缺失（REF-16 warning）。PR 自述已知；属既有 backlog，不在本 PR 范围。建议在独立 chore PR 中 `gocell generate` 补回。
- **C-info-2** `cells/accesscore/initialadmin/` 作为 cell 级非-slice 目录存在（bootstrap / path_default / credfile_*），与 identitymanage slice 并列但未被任何 slice.yaml 的 allowedFiles 覆盖。当前 governance 模型允许此模式，但未来若想收紧“Cell 内所有 Go 文件必须归属某 slice”可纳入 FMT-14 的扩展路线（而非本 PR 的修复项）。

## Phase 评审（7 维度）

| 维度 | 评级 | 依据 |
|------|------|------|
| A. 工作流完整性 | 绿 | 8 个顺序 commit 覆盖 signal 拆分 → path_default → credfile 拆分 → bootstrap promote → 调用方迁移 → 测试跳过 → CI 矩阵 → docs，阶段完整 |
| B. 工具合规 | 绿 | `gocell validate` / `check contract-health` 真实通过；PR 未手写元数据；Windows ACL 走 `x/sys/windows` 官方库（对标 kubernetes permissions_windows） |
| C. 角色完整性 | 绿 | 实现 + 测试 + CI + 文档 + review 参考齐全 |
| D. 内核集成健康度 | 绿 | `runtime/shutdown` 从“半残”变“按 OS 分治”，修复 V-A3 退化；未引入新退化 |
| E. 标准文件齐全度 | 绿 | build tag 全覆盖 (unix / windows / !unix&&!windows / unsupported)；test 文件 per-OS 对称 |
| F. 反馈闭环 | 绿 | 明确回应 PR#236 Sonar 退化（去 `unsafe.Pointer`、去重复代码、覆盖率 87.8%）——按反馈重写 |
| G. Tech Debt 趋势 | 绿 | 解决 V-A2 / V-A3 / V-A4 三项 backlog；新增净负债为 0 |

**合并建议**: APPROVE。Kernel Guardian 视角无阻塞项；merge 前等待 `os-smoke` (windows-latest / macos-latest) CI 绿灯即可（PR 作者已在 test plan 中标注）。
