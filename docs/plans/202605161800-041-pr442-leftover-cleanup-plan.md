# 041 — PR #442 遗留任务最大并行清理计划

## 来源与现状

- **PR #442**（K#09 SCAFFOLD-ONE-CMD，已 merged @ 2026-05-10）round-4/5/6 主线 P1 已闭环。
- **PR #461**（OPEN，docs/backlog-only）对 PR #442 做了 root-cause triage（`docs/reviews/202605111231-804-pr442-root-cause-triage.md`），登记 6 个新 gap + 收窄 3 处 over-claim done。
- 本计划基线 `develop == 41fc70074`（已超过 triage 基线 `47cd9e018`）。**逐项对照源码确认：10 个遗留任务在当前 develop 全部仍然存在**（验证记录见下表）。
- 用户指令：**不登记 backlog**，按文件冲突域**最大并行**处理。PR #461 的 backlog 登记是独立动作，不在本计划内（本计划直接修代码，不依赖 #461 merge）。

### 遗留任务清单（已在 develop 41fc70074 验证仍存在）

| # | ID | 证据 | P/Cx |
|---|---|---|---|
| 1 | GENERATE-HELP-CODEGEN-DEFAULT-DRIFT-01 | `cmd/gocell/app/generate.go:73` 仍 `"Prerequisite: set codegen: true"`；`README.md:114` 旧示例 | docs/Cx1 |
| 2 | SCAFFOLD-ASSEMBLY-YAML-SCALAR-SAFETY-01 | `kernel/assembly/gentpl/scaffold-assembly-yaml.tpl` raw scalar；`scaffoldAssemblyContext`(generator.go:138-143) 裸 string | P1/Cx2 |
| 3 | SCAFFOLD-ASSEMBLY-ID-METADATA-RULE-01 | `validateAssemblyScaffoldSpec` 用 path/control-char 校验，未走 `metadata.MatchAssemblyID` | P2/Cx1 |
| 4 | PATHSAFE-PARENT-SYMLINK-TOCTOU-01 | `pathsafe.go:78,339` 父目录 `os.MkdirAll`+路径式 `ContainPath` 预检，非 handle-based | P1/Cx3 |
| 5 | SCAFFOLD-BUNDLE-VARIANT-DUPLICATE-PATH-01 | `resolveBundleVariants:188-190` 允许 `WithHTTP&&WithEvents`；`planEventExampleArtifacts:219-221` 仅 `WithBoth` 换 sliceID | P2/Cx1 |
| 6 | PATHSAFE-OS-SMOKE-COVERAGE-01 | `_build-lint.yml:682,690` os-smoke 不含 `pkg/pathsafe/...` | test/Cx1 |
| 7 | SCAFFOLD-CELL-BUNDLE-CROSS-STAGE-PLAN-MERGE-01 | `scaffold.go:442` dry-run 在 `:462 autoGenerateCellBundleArtifacts` 前 return；派生文件跨阶段无 rollback | P2/Cx3 |
| 8 | ASSEMBLY-META-SYNTHESIS-FIELD-GUARD | `generator.go:359` 仍 `// Build.Binary intentionally omitted`，浅复制无字段 guard | P2/Cx2 |
| 9 | PATHSAFE-COLLECT-MISSING-DIRS-EACCES-01 | `pathsafe.go:359` `os.Stat;os.IsNotExist`，EACCES 不区分 → rollback 漏删 | P3/Cx2 |
| 10 | SCAFFOLD-INPUT-CONTRACT-TYPED-ID-01 | `pkg/scaffoldid` 不存在；输入校验散在 cmd/cellgen/assembly 三处副本 | P2/Cx3 |

### 显式不在范围（已闭环，不重做）

`PATHSAFE-LEAF-SYMLINK-NOFOLLOW`、`SCAFFOLD-AUTOGEN-SCOPE-SEALED`、`SCAFFOLD-ASSEMBLY-CROSS-STAGE-PLAN-MERGE-01`、`SCAFFOLD-WRITE-FUNNEL-HARD-UPGRADE`/`-DEPGUARD-01`（PR558）、`SCAFFOLD-BUNDLE-ARCHTEST-HARDEN`/`-LISTENER-MARKER-TYPED-CONST-01`（PR558）、`CONTRACT-YAML-CODEGEN-DEFAULT-CLEANUP`（PR559）。

---

## 并行结构：5 个 Lane，一 Lane 一 PR

按**文件独占域**切 Lane —— Lane 间零文件重叠 ⇒ 可真正并行 ship；Lane 内任务串行（共享热点文件）。

### 文件归属矩阵（无重叠 = 可并行）

| Lane | 独占文件域 | 任务 | Base 分支 |
|---|---|---|---|
| **A** pathsafe core | `pkg/pathsafe/**` | #4, #9, +API 扩展(ForceOverwrite/dup-guard) | `develop` |
| **B** assembly generator | `kernel/assembly/**`、`tools/archtest/assembly_meta_*` | #2, #3, #8 | `develop` |
| **C** docs/CI 孤立 | `cmd/gocell/app/generate.go`、`README.md`、`.github/workflows/_build-lint.yml` | #1, #6 | `develop` |
| **D** scaffold cell/bundle | `tools/codegen/cellgen/scaffold_bundle.go`、`tools/codegen/contractgen/*`、`cmd/gocell/app/scaffold.go` | #5, #7 | **Lane A 分支**（栈式） |
| **E** typed scaffold ID（收尾） | `pkg/scaffoldid/**`（新）+ 三处 spec 字段类型升级 | #10 | **B+D merge 后的 develop** |

### 依赖 DAG 与调度

```
t0 ┌── Lane A (worktree #1)  pkg/pathsafe          [A-API → A4 → A9]
   ├── Lane B (worktree #2)  kernel/assembly       [B2 → B1 → B8]
   └── Lane C (worktree #3)  docs/CI               [C1 ∥ C6]   ← 最快，先 ship
        │
        └─(A-API commit 落地后)→ Lane D (worktree #4, base=Lane A 分支) [D5 → D7]
                                          │
                          (B + D merge 后)└→ Lane E (worktree #5)  [E10]
```

- **t0 同时启动 3 个 worktree**：A、B、C 完全独立，零文件重叠。
- **Lane D 栈式基于 Lane A 分支**：D 需要 `pathsafe.PlannedFile.ForceOverwrite`（#7）+ duplicate-AbsPath preflight（#5）这两个 API。Lane A **第一个 commit 必须是纯加性 API 扩展**（A-API，Cx1），随后 A 继续做 #4/#9 的同时，Lane D worktree 以 Lane A 分支为 base 启动 D5/D7。A 的 PR 先 merge，D 的 PR rebase 到 develop 后 merge（PR 栈：A → D）。
- **Lane E 收尾串行**：#10 把 B 的 assembly-ID validator（#3）+ D 的 cellgen reject + cmd flag 绑定**收编进单源 `pkg/scaffoldid`**，必须等 B、D merge 后基于最新 develop 起，避免三方 rebase 冲突。

**关键路径** ≈ `A-API → D7(Cx3) → E10(Cx3)`。Lane C（Cx1×2）最短，优先 ship 清掉噪音。

---

## Lane 详情

> 全程 TDD：每个任务先 RED（失败测试/复现）再 GREEN。worktree 遵循 `git-worktree` skill 约定（编号、base 分支、删除安全）。不写向后兼容/双写/软回退（CLAUDE.md「不考虑向后兼容」+ [[feedback_no_soft_fallback]]）。

### Lane A — pathsafe 文件系统加固（PR: `fix(pathsafe): parent symlink TOCTOU + EACCES rollback + plan API`）

**A-API（首 commit，Cx1，纯加性，解锁 Lane D）**
- `pkg/pathsafe/pathsafe.go`：`PlannedFile` 加 `ForceOverwrite bool` 字段；`WritePlannedFiles` 的 `conflictPass` 对 `ForceOverwrite==true` 的条目跳过 conflict-reject（走 explicit `os.Remove` + `O_EXCL|O_NOFOLLOW` rewrite，对齐 `WriteFileForce` 语义）；`writePass` 增加 **duplicate `AbsPath` preflight**：同一 plan 内两条目 AbsPath 相同直接 `errcode.ErrConflict` fail（fail-closed）。
- RED：plan 含两条同 AbsPath → 期望 conflict；`ForceOverwrite` 条目覆盖既存文件成功。
- 必须保持 `SCAFFOLD-WRITE-FUNNEL-01` / depguard `scaffold-os-ban` 绿（新 os 调用只能在 `pkg/pathsafe/**` 豁免域内）。

**A4 — #4 PATHSAFE-PARENT-SYMLINK-TOCTOU-01（P1/Cx3）**
- 父目录创建/进入改 handle-based fail-closed：Unix 用 `openat`/`mkdirat`（`O_NOFOLLOW|O_DIRECTORY`）逐级下降做 nofollow + containment 复核，取代「`ContainPath` 路径预检 → `os.MkdirAll`」的 check-then-use。`pkg/pathsafe/nofollow_unix.go` 扩 syscall 封装；`nofollow_windows.go` 给出降级语义（保持 `O_EXCL` 兜底 + 文档说明 Windows 不保证 parent-walk nofollow）。
- RED：race fixture —— 预检后写入前替换父目录为 symlink，断言写入 fail-closed（非逃逸）。
- AI-rebust：syscall 级 fail-closed = **Hard**（违反不可表达，bypass 需改 syscall 封装且 diff 可见）。

**A9 — #9 PATHSAFE-COLLECT-MISSING-DIRS-EACCES-01（P3/Cx2）**
- `collectMissingDirs` 改签名 `(missing []string, err error)`：`os.Stat` 错误分支区分 `os.IsNotExist`（继续）vs 其它（EACCES 等，直接返回 err 让 caller fail 并经 rollback）。`mkdirAllTracked` 传播该 err。
- RED：中间目录 0o000 注入 EACCES，断言 rollback 不漏删孤立目录。

完工 gate：`golangci-lint run ./...` 0 issues（[[feedback_lint_before_push]]）；`go build -tags=integration ./...`（[[feedback_integration_tag_build]]）；`go test ./pkg/pathsafe/...`；`go test ./tools/archtest/...`（funnel/depguard 绿）。

### Lane B — assembly generator（PR: `fix(assembly): scaffold YAML scalar safety + metadata ID rule + synthesis field guard`）

**B2 — #3 SCAFFOLD-ASSEMBLY-ID-METADATA-RULE-01（P2/Cx1，先做，最小风险打底）**
- `validateAssemblyScaffoldSpec`（generator.go:497）入口调用 `metadata.MatchAssemblyID(spec.ID)`（已存在，`^[a-z][a-z0-9]+$`，FMT-A1 同源），失败映射 CLI 友好错误。Cells[] 同理用既有 cell ID 规则。保留现有 path/control-char 校验作为正交 defense-in-depth。
- RED：表驱动 —— `foo-bar`/`Foo`/`9foo`/含 `\n` 全 reject；`foocell` accept。覆盖 `cmd/gocell/app` + `kernel/assembly` 双层。

**B1 — #2 SCAFFOLD-ASSEMBLY-YAML-SCALAR-SAFETY-01（P1/Cx2）**
- `scaffoldAssemblyContext`（generator.go:138-143）用户输入字段 `ID/OwnerTeam/OwnerRole/DeployTemplate/Cells[]` 类型改 `pkg/yamlsafe.Scalar`（已存在 `Quote(raw) Scalar` 单漏斗，`String()` 渲染引号安全标量；与 cmd/cellgen 既有范式同源）。`buildScaffoldContext`(generator.go:386) 用 `yamlsafe.Quote(...)` 填充。`scaffold-assembly-yaml.tpl` 不变（text/template 经 `Scalar.String()` 输出已安全）。
- RED：`OwnerTeam` 含 `: #{}"` / 换行 → 渲染产物 `yaml.Unmarshal` 回来字段值完整且无注入新键。
- AI-rebust：typed newtype，裸 string 回退需对每个 struct 字段 diff-visible 类型变更 = **Hard**。

**B8 — #8 ASSEMBLY-META-SYNTHESIS-FIELD-GUARD（P2/Cx2，引入新约束 → 同 PR 闭环）**
- `synthesizeAssemblyMeta`（generator.go:362）补齐 `Build.Binary`（删除「intentionally omitted」死注释，按 spec 推导或显式声明缺省）。
- 新 archtest `ASSEMBLY-META-SYNTHESIS-FIELD-GUARD`：reflect 数 `metadata.AssemblyMeta` 字段集，断言与 `synthesizeAssemblyMeta` 已覆盖字段清单一致 —— 字段集变更不同步即 CI 红。
- **新约束必须同 PR 三件套闭环**（[[feedback_constraint_self_close]]）：(1) reflect 字段计数 archtest（静态守卫）；(2) `tools/archtest/` 文件头 `// INVARIANT: ASSEMBLY-META-SYNTHESIS-FIELD-GUARD` + 盲区清单 + 反向自检测试（ai-collab.md §工具选定后强制盲区自检）；(3) 字段新增的回归测试。
- AI-rebust：reflect 字段计数 = **Hard**（charter §三档：reflect 字段冻结属 Hard 范本）。本 PR 内把 godoc Medium 直接做到 reflect Hard，不留升级 backlog。

完工 gate：同 Lane A 全套 + `go test ./kernel/assembly/... ./tools/archtest/...`。

### Lane C — docs/CI 孤立（PR: `docs(cli): generate help codegen-default + ci(pathsafe): os-smoke coverage`）

**C1 — #1 GENERATE-HELP-CODEGEN-DEFAULT-DRIFT-01（docs/Cx1）**
- `cmd/gocell/app/generate.go:73` 改为「默认生成；仅需 opt-out 的 command/特殊 contract 写 `codegen: false`」。同步 `README.md:114` 及任何残留 `codegen: true` 过时示例（grep 全仓核销）。

**C6 — #6 PATHSAFE-OS-SMOKE-COVERAGE-01（test/Cx1）**
- `.github/workflows/_build-lint.yml` os-smoke job（:682/:690）vet+test 包列表加 `./pkg/pathsafe/...`，保留 advisory 语义，让 Windows/macOS `O_NOFOLLOW`→`O_EXCL` 降级路径漂移早暴露。

完工 gate：`golangci-lint run ./...`；`go test ./cmd/gocell/...`；CI yaml 用 `act` 或 lint 校验语法。

### Lane D — scaffold cell/bundle plan（base=Lane A 分支；PR 栈 A→D：`fix(scaffold): bundle variant dedup + cell cross-stage single-plan merge`）

**D5 — #5 SCAFFOLD-BUNDLE-VARIANT-DUPLICATE-PATH-01（P2/Cx1）**
- `resolveBundleVariants`（scaffold_bundle.go:188）把 `WithHTTP && WithEvents`（无 WithBoth）规范化为 `WithBoth` 语义；或始终在双 variant 时给 event 分配独立 `eventSliceID`。叠加 Lane A 的 duplicate-AbsPath preflight 做兜底。
- RED：`spec{WithHTTP:true,WithEvents:true}` → plan 无重复 AbsPath；旧行为下断言会命中 dup-guard。

**D7 — #7 SCAFFOLD-CELL-BUNDLE-CROSS-STAGE-PLAN-MERGE-01（P2/Cx3）**
- 把 `autoGenerateCellBundleArtifacts`（scaffold.go:468 contractgen/cellgen 派生）合并进单 `WritePlannedFiles` plan funnel，派生文件标 `ForceOverwrite:true`（regenerate 语义，来自 Lane A API）。dry-run 打印含派生文件的完整 plan；跨阶段单次 all-or-nothing rollback —— 与 round-6 assembly 路径对称（参照已闭环的 `SCAFFOLD-ASSEMBLY-CROSS-STAGE-PLAN-MERGE-01`）。
- RED：dry-run 输出包含 contractgen/cellgen 派生路径；中途注入写失败 → 零半成品（含派生文件）。

完工 gate：Lane A 全套 + `go test ./tools/codegen/... ./cmd/gocell/...`；`hack/verify-scaffold-bundle.sh`（sandbox 默认）；CI integration 实跑（[[feedback_ci_exact_integration_scope]]）。

### Lane E — typed scaffold ID 单源收编（base=B+D merge 后 develop；PR: `refactor(scaffold): typed ScaffoldID single-source input contract`）

**E10 — #10 SCAFFOLD-INPUT-CONTRACT-TYPED-ID-01（P2/Cx3，收尾）**
- 新 `pkg/scaffoldid`：`type ScaffoldID string` + 单源 `Parse(raw) (ScaffoldID, error)` 共享校验器（内部复用 `metadata.MatchAssemblyID`/`MatchCellID` 等既有规则，**不复制 pattern**）。
- `cellgen.ScaffoldSpec` / `assembly.AssemblyScaffoldSpec` / `cmd/gocell/app` flag 绑定三处 ID 字段类型升级为 `scaffoldid.ScaffoldID`；**收编** B2 的 assembly-ID validator、D5 的 cellgen reject、cmd 三处 `validateAssemblyPathComponent`/同义副本为单一漏斗，消除全部副本（无副本残留，[[feedback_no_lazy_deferral]]）。
- AI-rebust：string-typed concept funnel（charter §载体决策原则范本）：类型化 + 声明集中 + 构造点 typed —— **Hard**（裸 string 传入被类型系统拒）。同 PR 落 archtest 守声明位置 + 盲区自检。
- RED：跨包传裸 string 编译失败；非法 ID 在单源 validator 统一 reject 的表驱动测试。

完工 gate：全仓 `go test ./...` + `go test -tags=integration` CI 实际范围 + `golangci-lint run ./...` + `go test ./tools/archtest/...`。

---

## 全局执行约束

1. **PR 栈关系**：A、B、C 独立基于 develop，可乱序 merge。D 基于 A 分支，A merge 后 D rebase develop。E 必须 B+D merge 后起。C 优先 ship（清噪音）。
2. **每 PR 独立 review/merge**，互不阻塞（用户指令：最大并行）。每 PR 描述含 contract-fanout implementation matrix（涉及 schema/interface/CI 变更的 Lane 适用）。
3. **不登记 backlog**（用户指令）。PR #461 的 backlog 登记是独立轨，不阻塞本计划；本计划完成后这 10 条 gap 实质消失，#461 的对应行应在其自身轨道改判 done（不在本计划动作内）。
4. **新约束同 PR 闭环**：仅 B8（reflect 字段 guard）、E10（typed funnel）引入新 enforcement，均要求同 PR 三件套（静态守卫+文档契约+回归测试）+ AI-rebust ≥ Medium（实际均 Hard），无 Soft 立项，无升级 backlog 甩单。
5. **质量门**：每 Lane push 前本地 `golangci-lint run ./...` 0 issues、改导出签名跑 `go build -tags=integration ./...`、按 `.github/workflows/_build-lint.yml` integration-test job 实跑（非仅 `./...`）。
6. **激进三层自审**（[[feedback_three_layer_audit]]）：L1 各任务补丁；L2 PR 栈整体决策（A→D 栈、E 收编 #3 不留副本）；L3 概念一致性（pathsafe funnel 单源、scaffold 输入契约单源、assembly meta synthesis 与类型集冻结一致）。

## 验证矩阵

| Lane | 关键复现命令 | archtest 守卫 |
|---|---|---|
| A | `go test ./pkg/pathsafe/ -run 'TOCTOU|EACCES|ForceOverwrite|DupAbsPath'` | SCAFFOLD-WRITE-FUNNEL-01 / depguard 保持绿 |
| B | `go test ./kernel/assembly/ -run 'YAMLScalar|AssemblyIDRule|MetaSynthesis'` | 新 ASSEMBLY-META-SYNTHESIS-FIELD-GUARD（Hard） |
| C | `go test ./cmd/gocell/... -run GenerateHelp`；CI yaml lint | — |
| D | `hack/verify-scaffold-bundle.sh`；`go test ./tools/codegen/cellgen/ -run 'VariantDedup|CrossStage'` | SCAFFOLD-WRITE-FUNNEL-01 |
| E | `go test ./pkg/scaffoldid/...`；跨包裸 string 编译失败断言 | 新 typed-funnel 声明位置 archtest（Hard） |
