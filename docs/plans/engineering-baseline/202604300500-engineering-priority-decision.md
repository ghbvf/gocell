# GoCell 非 lint 工程基线对标 — 6 落点优先级决策

> 日期：2026-04-30
> 任务：基于 `202604300430-engineering-research-cross-cut.md` 的 12 维度对标矩阵，从中选出 6 个最高 ROI 工程落点，明确优先级、落地成本、依赖顺序与每个 PR 的三件套要求
> 关联文件：
> - `202604300430-engineering-research-cross-cut.md`（数据底稿，12 维度 × 3 SoT 对标）
> - `../ci-governance/`（lint/CI 治理研究链路，已完成）

---

## §1 6 个落点总览

按 ROI（结构性收益 / 落地成本）+ 依赖关系排序：

| # | 落点 | 维度 | GoCell 现状 | 主要对标 | 落地成本 | ROI | 实施顺序 |
|---|---|---|---|---|---|---|---|
| **L1** | **供应链安全武装**（govulncheck + Semgrep + CodeQL + race 独立 job） | 6 | 缺位 | Vault `security-scan.yml` | 低 | **极高** | **Batch 1（立即）** |
| **L2** | **Codegen 隔离沙箱化**（git worktree verify + marker 体系起点） | 1 | 半成品 | K8s `verify-generated.sh` | 低 | 高 | Batch 1 |
| **L3** | **Lifecycle 对称清理 + 依赖图可视化**（增强 `bootstrap.Lifecycle` + `gocell visualize` 子命令，**不引入 fx 依赖**） | 5 + 4 + 12 | 路径已成立（Option pattern + 10-phase + LIFO）| Temporal/fx 设计语义（不复制实现）| 低 | 中 | Batch 1（收尾） |
| **L4** | **错误库增强**（WithSafeDetails + AssertionFailedf + logtag 静态 msg） | 3 | 成熟 → 进阶 | cockroachdb/errors + temporal tag.Tag（吸收设计，不引依赖） | 中 | 高 | Batch 2 |
| **L5** | **API 治理升级**（storageVersion + kube-api-linter plugin + 弃用窗口） | 9 | 半成品 | K8s deprecation policy + kube-api-linter | 中 | 中 | Batch 2 |
| **L6** | **文档自动化起步**（gen-crd-api-reference-docs + KEP frontmatter） | 10 | 缺位 | K8s `gen-crd-api-reference-docs` + KEP | 中 | 中 | Batch 3 |

> **对标 ≠ 采纳**：CLAUDE.md「参考框架」一节里所有映射（Cell 运行时 → Uber fx、代码生成 → goctl 等）都是**吸收设计语义**，不是引入实现。GoCell 自建 DI/Lifecycle 编排（领域逻辑），第三方实现仅在「实现外部协议/标准」时才采用。L3/L4/L5/L6 的对标对象都按这条原则收口。

**未入选维度**：
- 维度 2 测试（成熟，仅做小补：Vault 全局 mock 注册扩展 in-memory adapter 矩阵）
- 维度 7 发布（GoCell 还无外部 consumer，推迟到 v1.0 临近）
- 维度 8 migration（goose + invalid-index 检测已合理，暂无对标改进点）
- 维度 11 性能基线（GoCell 仍在功能演进期，benchmark gate 推迟）

---

## §2 落点详述

### L1 — 供应链安全武装

**当前空白**：
- 无 govulncheck（CI 完全缺位）
- 无 SBOM / cyclonedx-gomod
- 无静态安全扫描（Semgrep / CodeQL）
- 无 secret scan（trufflehog / gitleaks）
- 无镜像签名（cosign）
- race detector 散在 kernel shard，无独立 job

**对标证据**：
- Vault `.github/workflows/security-scan.yml`：CodeQL + Semgrep `1.45.0`，SARIF 上传 GitHub security dashboard
- Vault `.github/workflows/mend-pr-scan.yml`：Mend 依赖扫描
- Vault `.github/workflows/test-go.yml`：`-race` + `"WARNING: DATA RACE"` 字符串扫描 + `gotestsum`

**落地切片**：
- **PR L1.1**：新增 `.github/workflows/security-vuln.yml` —— `go run golang.org/x/vuln/cmd/govulncheck@latest ./...`，required check（成本 ≤ 30s）
- **PR L1.2**：新增 `.github/workflows/security-static.yml` —— Semgrep `p/golang` 官方规则包 + CodeQL 上传 SARIF
- **PR L1.3**：新增 `.github/workflows/test-race.yml` —— 独立 race job 跑 `go test -race ./kernel/... ./runtime/...`，不阻塞主 CI 但 required
- **PR L1.4**（可选）：`syft` SBOM 生成 + `cosign` 签名（GoCell 暂无 release artifact，可推迟到 L7 发布工程）

**三件套**：
- 静态守卫：4 个 workflow 文件
- 同 PR 修光 finding：govulncheck 当前 0 alert / Semgrep p/golang 预期 < 30 finding
- 契约同步：CLAUDE.md 加「供应链安全」章节，列出本仓库已启用的 4 类 scanner

---

### L2 — Codegen 隔离沙箱化

**当前空白**：
- `gocell generate` + CI diff gate 已存在，但用直接 `git diff`，本地工作区 dirty 会污染 verify
- 无 marker 体系（治理规则硬编码在 validate 命令）
- 生成产物分散（`assemblies/corebundle/generated/` 单点）

**对标证据**：
- K8s `hack/lib/verify-generated.sh`：`git worktree add -f -q "${_tmpdir}" HEAD` → 沙箱内重跑 generator → `git status --porcelain | wc -l` 计数
- controller-gen marker：`// +kubebuilder:validation:Required` / `// +optional` / `// +groupName=...`

**落地切片**：
- **PR L2.1**：改 `make verify-generated` 走 `git worktree add` 沙箱模式（替换当前 `git diff --exit-code`）
- **PR L2.2**：试点 marker 体系 —— 选 ADV-06（contractUsages 双向校验）作为第一条 marker 化规则：在 cell.yaml / slice.yaml 字段注释加 `# +gocell:contract:role=subscribe`，`gocell validate` 改 AST-visitor 模式
- **PR L2.3**：生成产物集中到 `generated/schemas/` 而非散落各 cells/

**三件套**：
- 静态守卫：worktree 验证脚本
- 同 PR 修光：gen 产物对齐
- 契约同步：CLAUDE.md「修改代码前」章节加 `make verify-generated` 步骤

---

### L3 — Lifecycle 对称清理 + 依赖图可视化（**不引入 fx 依赖**）

> **路径选择说明**：CLAUDE.md「Cell 运行时 → Uber fx」是**参考关系**，目的是吸收 Lifecycle 对称清理、依赖图可视化、Module 隔离等设计语义，**不是引入 `go.uber.org/fx` 包**。CLAUDE.md 第一条依赖原则就是「实现 GoCell 领域逻辑（Cell/Slice 模型、治理规则、outbox 接口等）保留自建」——DI/Lifecycle 编排是 Cell 模型的一部分，属于领域逻辑。GoCell 现有的 Option pattern + 10-phase 显式编排 + archtest LAYER + boundary.yaml 已经是 self-consistent 的方案，不应被推倒。

**fx 真正值得吸收的设计（不需要引依赖）：**

| fx 能力 | GoCell 已有 / 待补 |
|---|---|
| OnStart 失败 → 已注册 OnStop 按 LIFO 对称清理 | `bootstrap.Lifecycle` phase 间已有 LIFO；**phase 内多个 hook 半路失败的对称清理需查代码** |
| 依赖图可视化（`fx.Visualize`） | `boundary.yaml` 已是机器可消费的依赖图，缺 DOT/SVG 导出工具 |
| Module 隔离 + Private 守可见性 | `cell.yaml allowedFiles` + archtest LAYER + `internal/` 三重守卫 **比 fx.Module 更结构化**（无需双轨） |
| Annotated/Group 多实现注入 | `contractUsages[role=publish/subscribe]` 已是结构化版本（无需 fx 反射） |

**当前差距**（重新评估）：
- 维度 5（DI 组装）从「半成品」**重新评级为「成熟」** —— 它选了一条不同于 fx 的路径（显式 Option pattern + 10-phase），但路径本身合理且与 archtest 等其他守卫 self-consistent
- 真正需要补的只有两点：(a) `bootstrap.Lifecycle` 的对称清理语义在 phase 内 hook 失败场景下要确认；(b) `boundary.yaml` → DOT 可视化工具缺位

**落地切片（轻量，2 个 PR 闭环）**：

- **PR L3.1**：审查 `runtime/bootstrap/bootstrap.go` 的 `Lifecycle.Append` 实现：
  - 确认 phase 内多个 OnStart hook，第 N 个失败时，前 N-1 个已注册的 OnStop 是否按 LIFO 触发
  - 如缺，扩展实现 + 写 table-driven test 覆盖 hook 失败矩阵（first-fail / mid-fail / last-fail / panic-during-OnStart）
  - 同步更新 `runtime/bootstrap/lifecycle.go` godoc 注释明确语义
- **PR L3.2**：新增 `gocell visualize` 子命令：
  - 输入：`assemblies/<name>/generated/boundary.yaml`
  - 输出：DOT（默认）或 SVG（`--format=svg`）或 mermaid（`--format=mermaid`）
  - 节点：cells / contracts / actors，边：exportedContracts / importedContracts / role
  - 用 stdlib `text/template` 渲染，不引第三方图渲染库
  - CI artifact：`make verify` 跑一次 `gocell visualize > /tmp/graph.dot`，作为可视化产物存档（不阻塞）

**三件套**：
- 静态守卫：`bootstrap.Lifecycle` table-driven test 锁定对称清理语义（前置条件，不依赖 fx）
- 同 PR 修光：本 PR 范围内的 `Lifecycle.Append` 调用方都通过新测试
- 契约同步：CLAUDE.md「依赖注入 / 模块组装」章节明确「自建 Option pattern + 10-phase 编排，参考 fx 设计语义但不引入实现」

**ROI 重新评估**：
- 落地成本：**低**（2 个 PR，无重构，无外部依赖）
- 价值：补 Lifecycle 的失败场景守护 + 给开发者一个直观的依赖图工具，不动现有架构
- 与原方案对比：原 L3「引入 fx 重构 4-6 PR」是把「对标」误解为「采纳实现」，会引入反射运行时开销 + 与 archtest LAYER/boundary.yaml 的双轨复杂度，且与 CLAUDE.md「领域逻辑保留自建」原则冲突

---

### L4 — 错误库增强 + log tag 静态约束

**当前空白**：
- `pkg/errcode` 已有 `Code + Category + Safe/InternalMessage`，但 `Details map[string]any` 中字段无 safe/unsafe 标注，PII 泄漏风险
- 无 AssertionFailedf 路径区分编程 bug 与操作错误
- log msg 无静态约束，`slog.Info(fmt.Sprintf(...))` 反模式可能漂移
- 无 `pkg/logtag` 包，slog.Attr key 散布在各包

**对标证据**：
- `cockroachdb/errors` `WithSafeDetails(err, "user=%s acct=%s", redact.Safe(userID), password)`：password 自动 redact，userID 因 `Safe()` 包裹明文出现
- `cockroachdb/errors` `AssertionFailedf` + `HasAssertionFailure(err)` 路由到 crash reporter
- `temporal/common/log/interface.go`：msg 必须静态字符串字面量，动态信息通过 `tag.Tag` 传入；tag 命名 kebab-case

**落地切片**：
- **PR L4.1**：`pkg/errcode` 增 `WithSafeDetails(err *Error, safe map[string]any) *Error`，与现有 `WithDetails`（unsafe）并列，audit log / telemetry 路径只读 safe
- **PR L4.2**：`pkg/errcode` 增 `NewAssertion(code, format, args...) *Error` + `IsAssertion(err) bool`，kernel/ 不变量检查改用此路径，HTTP handler 统一映射 500 + slog.Error("assertion failure")
- **PR L4.3**：建 `pkg/logtag` 包，定义领域 slog.Attr 工厂：`CellID / SliceID / ContractID / RequestID / TraceID` 等，统一 kebab-case key
- **PR L4.4**：CLAUDE.md `.claude/rules/gocell/observability.md` 加规约「log msg 必须字面量字符串，动态值只能通过 slog.Attr 传入」
- **PR L4.5**：写 `tools/archtest/log_msg_static_test.go` go/analysis pass 守护 msg 字面量约束

**三件套**：
- 静态守卫：`tools/archtest/log_msg_static_test.go` + 现有 OBS-01 metric schema gate（已有）
- 同 PR 修光：扫存量 `slog.X(fmt.Sprintf(...))` 反模式（预期 < 30 处）
- 契约同步：observability.md 规约 + pkg/errcode godoc 完整

---

### L5 — API 治理升级

**当前空白**：
- contracts/ 多版本目录（`v1/`、`v2/`）但缺 storageVersion 概念
- 无 API spec lint（kube-api-linter / api-linter / buf 都不适用 YAML contract）
- contract.lifecycle 有 deprecated 字段但无时间窗口约束

**对标证据**：
- K8s `spec.versions[].storage`：多版本中只有一个标 `storage: true`（Hub），其他靠 conversion 互转
- kube-api-linter 30+ golangci-lint plugin 规则：`OptionalOrRequired / JSONTags / NoNullable / SSATags / DefaultOrRequired / CommentStart`
- K8s deprecation policy：Beta API 弃用后必须再支持 9 个月或 3 release（取长）

**落地切片**：
- **PR L5.1**：`contract.yaml` schema 加 `storageVersion: bool`；`gocell validate` ADV-06 扩展为多版本中**有且仅有一个** `storageVersion: true`，缺失或多于一个均 error
- **PR L5.2**：`contract.yaml` schema 加 `deprecatedAt` 字段（sprint 编号或 ISO date）；CI archtest 检查超过 3 个 sprint 仍 deprecated 且无 active 替代的 contract，warning（不立即 fail）
- **PR L5.3**：评估 kube-api-linter plugin 是否能用于 `kernel/metadata` Go struct（CellMeta/SliceMeta/ContractMeta）—— 如果可用则启 `OptionalOrRequired / JSONTags / CommentStart` 三条；不可用则记录为长期 backlog
- **PR L5.4**：`gocell validate` 加 `--deprecation-report` flag，列出所有 lifecycle=deprecated 的 contract + deprecatedAt + 距离过期 sprint 数

**三件套**：
- 静态守卫：ADV-06 扩展、deprecation 时间窗口检查
- 同 PR 修光：当前 31 个 contract 加 storageVersion 标注（v1 全部默认 storage=true）
- 契约同步：`.claude/rules/gocell/api-versioning.md` 加「多版本 storage」+「弃用窗口」章节

---

### L6 — 文档自动化起步

**当前空白**：
- 无 godoc 完整度检查
- `metadata-model-v3.md` 手写维护（与 `kernel/metadata` Go struct 漂移风险）
- 无 changelog 自动化
- 无 ADR 体系（`docs/adr/` 不存在）
- plan 文档无结构化 lifecycle 字段

**对标证据**：
- `github.com/ahmetb/gen-crd-api-reference-docs`：从 godoc 注释生成 API ref HTML
- kubebuilder literatec：代码片段标注嵌入 doc + CI verify-docs git diff 守护
- KEP frontmatter：`stage: alpha/beta/stable` + `status: implementable/implemented` + SIG approver

**落地切片**：
- **PR L6.1**：引入 `gen-crd-api-reference-docs`：`hack/gen-metadata-api-ref.sh` 用 `gen-crd-api-reference-docs -api-dir ./kernel/metadata -out-file docs/references/metadata-api.html`，CI `make verify-docs` 守护
- **PR L6.2**：删 `metadata-model-v3.md` 手写副本，改为指向 `metadata-api.html`（保留迁移说明）
- **PR L6.3**：plan 文档结构化 frontmatter 模板：`status / stage / approvers / milestone / linked-pr`，写到 `docs/plans/_template.md`
- **PR L6.4**（可选）：引入 `git-cliff` 基于 conventional commits 自动生成 CHANGELOG.md
- **PR L6.5**（可选）：建 `docs/adr/` 目录 + ADR-001 模板（决策模板，与 plan 区分：plan 是实施计划，ADR 是技术决策）

**三件套**：
- 静态守卫：`make verify-docs`（gen-crd-api-reference-docs 产物 git diff）
- 同 PR 修光：所有 `kernel/metadata` 导出 struct godoc 完整度
- 契约同步：CLAUDE.md「文档命名规则」章节扩展为「文档自动化」章节

---

## §3 整体路线图

```
Batch 1（立即，2-3 周内）                 Batch 2（增强期，2-3 个 sprint）          Batch 3（成熟期）
────────────────────────────────         ───────────────────────────────         ─────────────────
L1 供应链安全武装                         L4 错误库增强 + logtag                    L6 文档自动化起步
  PR L1.1 govulncheck                     PR L4.1 WithSafeDetails                   PR L6.1 gen-crd-api-ref
  PR L1.2 Semgrep + CodeQL                PR L4.2 AssertionFailedf                  PR L6.2 删 metadata-model-v3
  PR L1.3 race 独立 job                   PR L4.3 pkg/logtag                        PR L6.3 plan frontmatter 模板
  PR L1.4 SBOM（推迟到 v1.0）             PR L4.4 observability.md 规约             PR L6.4 git-cliff（可选）
                                          PR L4.5 archtest log msg static 守护       PR L6.5 docs/adr/（可选）
L2 codegen 隔离沙箱化
  PR L2.1 worktree verify                 L5 API 治理升级
  PR L2.2 marker 体系试点                  PR L5.1 storageVersion 标记 + ADV-06 扩展
  PR L2.3 集中 generated/                  PR L5.2 deprecatedAt + 弃用窗口
                                          PR L5.3 kube-api-linter 评估（可能不适用）
L3 Lifecycle 对称清理 +                    PR L5.4 gocell validate --deprecation-report
   依赖图可视化（不引 fx）
  PR L3.1 Lifecycle 失败矩阵守护
  PR L3.2 gocell visualize 子命令
```

**关键依赖**：
- L1 / L2 / L3 都是低成本独立 PR，可并行启动
- L4 与 L1-L3 无冲突，本可并行；放 Batch 2 是因为 `pkg/errcode` 增强会触动跨 cell 调用方，等 Batch 1 CI 安全网建立后做更稳
- L5 / L6 放 Batch 3 因为：L5 storageVersion 改 schema 会触发所有 contract 文件级改动，需要先稳定其他基础；L6 文档自动化基于 godoc 完整度，应在 L4 错误库 godoc 完善后启动
- **没有任何 PR 因 L3 而阻塞**（修正后 L3 是轻量 2 PR，与 fx 重构无关）

---

## §4 三件套要求（呼应「引入新约束必须同 PR 闭环」feedback）

每个 PR 必须自带：

1. **静态守卫**：archtest / golangci-lint plugin / CI workflow 中的 hard gate
2. **同 PR 修光所有 finding**：不允许 baseline-only / `--new-from-rev`
3. **契约同步**：把规则写入 CLAUDE.md / `.claude/rules/gocell/*.md` / 对应 godoc 注释，让人类规约与工具守护说同一种话

---

## §5 决策点（待确认）

- [x] **L3 不引入 fx 依赖**（已修正：从「fx 真用起来」改为「Lifecycle 对称清理 + 依赖图可视化」，2 PR 闭环；CLAUDE.md「领域逻辑保留自建」原则保护）
- [ ] **是否同意 Batch 1 同时启动 L1 + L2 + L3？**（三者均为独立低成本 PR，可并行）
- [ ] **L1 是否立即启动 govulncheck 单 PR 闭环？**（成本 ≤ 30s CI，零 finding 风险低）
- [ ] **L3 PR L3.1 之前先跑代码审查确认 `bootstrap.Lifecycle` 现状？**（避免重复实现已有功能）
- [ ] **未入选维度（发布工程 / 性能基线 / migration 增强）是否进 backlog 跟踪？**
  - 建议：发布工程（v1.0 临近时启动）/ 性能基线（v1.0 后）/ migration（暂保持现状）

---

## §6 数据来源

- 12 维度 GoCell 现状盘点 + 9 维度 SoT 对标矩阵 + raw URL 证据：见 `202604300430-engineering-research-cross-cut.md`
- 4 个 explorer agent 调研记录：transcript 路径已嵌入研究底稿 §6
