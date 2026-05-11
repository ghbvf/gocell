# PR #442 — Kernel Guardian 维度审查

## 总体结论

**通过（绿）**。PR #442 在 Kernel Guardian 关注的三条主线均符合 GoCell 分层与元数据契约：

- **分层隔离**：`kernel/assembly/generator.go` 新增 import 仅 `pkg/pathsafe`+ stdlib `path/filepath`，未触达 `tools/codegen` / `runtime/` / `cells/` / `adapters/`，符合 "kernel 只依赖 stdlib + pkg/" 红线；`kernel/scaffold/` 整层在 merge commit `31070c2f` 的 tree 中确认全删（9 文件 deletion），职责正确迁移到 `tools/codegen/cellgen/`（render 类工具，非内核）和 `kernel/assembly`（已有 assembly 生命周期 owner）两处，没有形成第三个 scaffold 子包。
- **元数据合规**：`ContractMeta.Codegen` 默认翻转通过 AST funnel（`contractYAMLHasKey`）实现，5 个 `kind=command` 例外（iotdevice device-command/{ack,dequeue,enqueue,extend-lease,report}/v1/contract.yaml）已全部显式 `codegen: false`+ 内联注释；`types.go` godoc 同步更新。
- **Archtest 契约完整性**：新增 5 测试（含 `SCAFFOLD-WRITE-FUNNEL-01` Hard、`SCAFFOLD-BUNDLE-MARKER-01` / `SCAFFOLD-BUNDLE-NO-CODEGEN-LITERAL-01` Medium、`SCAFFOLD-CONTRACT-CODEGEN-DEFAULT-TRUE` parser side），均带文件级 `INVARIANT: <ID>` 注释，funnel 正确复用 `tools/archtest/internal/scanner` 的 `EachFile`/`DirsScope`/`MatchRels`，未自起 AST walker。

下面 6 条 finding 是仍可优化的尾巴（含 1 条 Cx2、5 条 Cx1）。

## Finding 列表

### F1 [Cx2] `codegen: true` 字面冗余未在本 PR 清理

**位置**：`contracts/**/contract.yaml` × 74 文件（merge commit `31070c2f` tree）；backlog `CONTRACT-YAML-CODEGEN-DEFAULT-CLEANUP`。

**问题**：K#09 funnel 已让 parser 把 absent `codegen:` 默认为 true，但 PR 没顺手把 74 个既有 contract.yaml 中冗余的 `codegen: true` 行删掉。从语义看现在是 "funnel 默认 true + 显式 true" 二重表达，违反 GoCell 单源治理（"重构直接删旧代码、不造平行结构"）。`SCAFFOLD-BUNDLE-NO-CODEGEN-LITERAL-01` 只守 scaffold 出的新 contract.yaml，对已有 74 处无约束。

**证据**：merge tree 统计 `codegen: true` × 74、`codegen: false` × 5（即 5 个 command）、absent × 7（全是 testdata fixtures）；backlog 第 2667 行登记 follow-up「sed 一次清 + archtest 加 funnel 守卫扫不再含 `codegen: true` 字面」。

**建议**：尽快走 follow-up PR 把 74 行删干净，并在 `tools/archtest/scaffold_bundle_test.go` 之外加一个全仓 `contracts/` 扫描的 archtest，断言 `codegen: true` 字面不再出现（任何状态默认即 true，余下只允许 `codegen: false` 显式 opt-out）。否则下次新人 copy-paste 既有 contract 会持续繁殖冗余。

**AI-rebust 评级**：升级路径 Medium（real-source YAML scan + 全仓 allowlist=空）。

**Backlog 登记**：已登记 `CONTRACT-YAML-CODEGEN-DEFAULT-CLEANUP`（P2/Cx2 黄）。

### F2 [Cx2] `synthesizeAssemblyMeta` 字段集守护仅靠 godoc + grep，未落 archtest

**位置**：`kernel/assembly/generator.go:3133-3155`（PR diff 行号）。

**问题**：`synthesizeAssemblyMeta` 浅复制 5 个字段（ID / Cells / Owner.Team / Owner.Role / Build.Entrypoint），其余 `AssemblyMeta` 字段（含 `Build.Binary`、`MaxConsistencyLevel`、未来新增字段）一律取零值；godoc 注释要求"必须与 GenerateModulesGen/Entrypoint/Boundary 读取的字段集保持同步"，但**没有任何机制把这条约束变成静态可执行**。如果 `metadata.AssemblyMeta` 新增字段且某个 Generate\* 读取了它，新增 assembly 在 K#09 路径上会拿到零值，行为静默不正确。

**证据**：注释自己写 "Field set must stay in sync with what GenerateModulesGen/Entrypoint/Boundary read — see backlog ASSEMBLY-META-SYNTHESIS-FIELD-GUARD"（pkg diff 3138-3139）；backlog `ASSEMBLY-META-SYNTHESIS-FIELD-GUARD` 已登记但状态绿（无触发条件）。

**建议**：把 backlog 触发条件改"deferred"为"K#09 ship 后即做"，落 reflect-based archtest：枚举 `AssemblyMeta` 全部字段，断言要么在 `synthesizeAssemblyMeta` 显式赋值、要么在 allowlist（含"故意置零"注释）。这就是典型的 godoc-Soft → reflect-Hard 升级。

**AI-rebust 评级**：当前 Soft（注释 + grep 约定）。目标 Hard（reflect 字段计数）。按 `.claude/rules/gocell/ai-collab.md`，"既有 Soft 的补丁要优先讨论升级到 Hard/Medium"，本 PR 既然新增了这个 in-memory inject 机制，本应同包发 reflect guard。

**Backlog 登记**：`ASSEMBLY-META-SYNTHESIS-FIELD-GUARD`（P2/Cx2 绿，触发条件偏被动）；建议改为 P2/Cx2 黄（主动 ship 时机=本 PR 后第 1 个 release window）。

### F3 [Cx1] `Generator` 并发不安全 + map mutation 仅靠 defer revert，多 goroutine 测试会污染全局

**位置**：`kernel/assembly/generator.go:3092-3102` `appendGeneratedFiles` 的 `g.project.Assemblies[spec.ID] = synth ... defer revert`。

**问题**：注释明说 "Generator is not safe for concurrent use" 后紧跟 "may be called sequentially on the same Generator instance"。这是程序员承诺，非 enforcement。如果未来某测试或 batch tool 起 2 个 goroutine 同时调 `PlanAssemblyScaffold`（同一 spec.ID 或不同 spec.ID）：
- 不同 spec.ID：两个 goroutine 写 `g.project.Assemblies` 同一 map 触发 Go runtime fatal race
- 同一 spec.ID：第二个 goroutine `hadPrior=true && prior=synth1`，第一个 defer 跑完后第二个 revert 把状态恢复为 synth1（污染）

**证据**：`appendGeneratedFiles` 全程裸 map 写 + defer，无 mutex / atomic / clone-then-modify。

**建议**：两条路径任选一：
1. 让 `synthesizeAssemblyMeta` 不污染 `g.project.Assemblies`——把三个 Generate\* 改造为接受 `AssemblyMeta` 参数注入（pure render），从根上消除 in-memory mutation；这与 backlog `SCAFFOLD-GENERATOR-PURE-BYTES`(已 ✅) 的精神一致，但派生 generator 没走到底。
2. 退而求次：加 `g.scaffoldMu sync.Mutex` 守 `PlanAssemblyScaffold` 入口；archtest 静态扫此函数体禁止跨 goroutine 调用。

**AI-rebust 评级**：当前 Soft（godoc 约定）。路径 1 是 Hard（type system + pure function）；路径 2 是 Medium。

**Backlog 登记**：未登记。建议新增 `ASSEMBLY-GENERATOR-CONCURRENT-SAFE-01`（P3/Cx2 绿，触发条件=出现并行 scaffold 需求 / 第二个 generator 调用方）。

### F4 [Cx1] `validateAssemblyPathComponent` 与 `cmd/gocell/app.validateScaffoldID` 双源复制

**位置**：`kernel/assembly/generator.go:3248-3273`。注释本身坦承"kernel-side mirror of cmd/gocell/app.validateScaffoldID — duplicated rather than shared because kernel/ may not import cmd/. Rule must stay synchronized."

**问题**：分层约束（kernel 不能 import cmd）正确，但解法不彻底——两份 validator 共享同一规则集（traversal char 集 + control char 集），靠人工保持同步。这正是 `SCAFFOLD-INPUT-CONTRACT-TYPED-ID-01` backlog 项指向的根因。本 PR 没动这条线，于是 K#09 又引入了一份新副本（kernel 一份、cmd 一份），双源问题加剧。

**证据**：backlog 第 2684 行 `SCAFFOLD-INPUT-CONTRACT-TYPED-ID-01` 显式列触发条件"跨包 spec 输入误用 / 第 4 个 scaffold 入口出现"——当前已经是第 3 处（cmd validateScaffoldID + cellgen.ScaffoldCellBundle round-5 一份 + kernel validateAssemblyPathComponent），距离触发只差 1 处。

**建议**：建一个 `pkg/scaffoldid`（已在 backlog 中规划路径），把 traversal char / control char 检查集中到一处常量 + 一个 `Validate(value, field string) error`。kernel 和 cmd 都 import 这个 pkg。

**AI-rebust 评级**：当前 Soft（注释 + 人工同步）。目标 Hard（typed `ScaffoldID` newtype + 单一构造函数漏斗）。

**Backlog 登记**：已登记 `SCAFFOLD-INPUT-CONTRACT-TYPED-ID-01`（P2/Cx3 绿）。建议立即把 status 升黄，触发条件"已到 3 处"。

### F5 [Cx1] `SCAFFOLD-WRITE-FUNNEL-01` archtest 的扫描谓词靠字符串 prefix 判断，新增 scaffold 文件命名漂移即逃逸

**位置**：`tools/archtest/scaffold_write_funnel_test.go:7439-7453`。

**问题**：`scaffoldOnlyPred` 用 `strings.HasPrefix(base, "scaffold")` / `strings.HasPrefix(base, "generate_")` 作为"什么文件该扫"的判定。这是文件名 convention，违反 `ai-collab.md` "名字 convention → sealed interface / receiver type 识别"——理论上属于 Soft 形态。例如：
- `cmd/gocell/app/setup.go`（未来新增的 scaffold 入口若不以 `scaffold` 开头）→ 直接逃逸
- `tools/codegen/cellgen/bundle.go`（重命名 `scaffold_bundle.go` 即逃逸）

**证据**：archtest 文件级注释自己声明 "AI-rebust: Hard"，但实际枚举谓词靠字符串前缀，且 file-level 注释也承认 "Adding any NEW file under cmd/gocell/app/ must either: 1. Match the scaffold\*.go prefix 2. Justify exemption in this comment block before merging. The scaffoldOnlyPred predicate enforces #1; #2 is the human-review gate."——human-review gate 即 Soft。

**建议**：把 Hard 评级降级为 Medium 更诚实，或同步登记升级条目 `SCAFFOLD-WRITE-FUNNEL-PREDICATE-HARDEN`（与现有 `SCAFFOLD-WRITE-FUNNEL-HARD-UPGRADE` 配套）：在 `pkg/pathsafe` 暴露 typed `Writer` interface + `.golangci.yml` depguard ban 在 scaffold/codegen 包 import `os`（让"非 funnel 写"在编译期不可表达）。backlog `SCAFFOLD-WRITE-FUNNEL-HARD-UPGRADE` 已经在做这件事，但当前评级矛盾应在 archtest 注释里调一致。

**AI-rebust 评级**：实际 Medium（real-source AST scan + 字符串前缀谓词）；声明 Hard 偏乐观。

**Backlog 登记**：已部分覆盖 `SCAFFOLD-WRITE-FUNNEL-HARD-UPGRADE`（P3/Cx3 绿）。建议在 archtest 文件级注释里把 "AI-rebust: Hard" 改为 "Medium（谓词字符串，Hard 路径见 backlog ...）"。

### F6 [Cx1] `Generator` import `pkg/pathsafe` 后，kernel 类型签名暴露 pkg 类型

**位置**：`kernel/assembly/generator.go` `PlanAssemblyScaffold(spec) ([]pathsafe.PlannedFile, error)`。

**问题**：分层规则允许 kernel 依赖 pkg/，所以 import 本身合规。但把 `pathsafe.PlannedFile` 直接放到 kernel 公开方法签名，意味着任何调用方（cmd/gocell/app）都得跨包传 `pathsafe.PlannedFile`，而 `PlannedFile` 在 pkg 层定义、内含 absolute path 字段；kernel 输出的是 "plan"（抽象语义），pkg 类型是"path-safe write 实施物"，两层语义耦合在了一起。理想分层下，kernel 应返回更抽象的 `assembly.ScaffoldPlan`（含 relative path + content），由 cmd 层 adapter 转 `pathsafe.PlannedFile` 再 `WritePlannedFiles`。

**证据**：`PlanAssemblyScaffold` 内部 `pathsafe.ContainPath(realRoot, d.relPath)` 已经把 absolute path resolution 做在了 kernel 里——kernel 本不该感知项目文件系统真实根（这是 cmd CLI 的职责）。

**建议**：低优先级重构。如果未来 kernel 测试或第二个调用方（e.g. server-side scaffold API）出现，应把 `ContainPath` 调用上提到 cmd 层；kernel 只产 `(relPath, content)` 对。当前单调用方场景下耦合代价低，不建议立项独立 PR。

**AI-rebust 评级**：不适用（架构观感问题，非 enforcement 规则）。

**Backlog 登记**：未登记，建议新增 `ASSEMBLY-SCAFFOLD-PLAN-RELATIVE-PATH-01`（P3/Cx2 绿，触发条件=出现第二个 PlanAssemblyScaffold 调用方 / kernel 单元测试需 stub filesystem）。

## 维度评分

| 维度 | 评分 | 证据 |
|------|------|------|
| 分层隔离 | 绿 | kernel/assembly import 仅 stdlib + pkg/{errcode,pathsafe}；kernel/scaffold 整层删除（merge commit tree 验证）；scaffold render 职责迁移 tools/codegen/cellgen，未污染 kernel |
| 元数据合规 | 绿 | ContractMeta.Codegen funnel + AST 检测正确实现；5 个 command 例外全显式 `codegen: false`+ 注释；types.go godoc 同步；F1 是冗余清理而非合规漏洞 |
| Archtest 契约完整性 | 黄 | 5 archtest 全带 INVARIANT 标头；复用 scanner 包；但 F5（谓词字符串 prefix）+ F2（reflect guard 缺位）让 "Hard" 声明偏乐观 |
| Tech Debt 趋势 | 净减少 | 删 kernel/scaffold 约 1200 LOC 双源；新增 6 条 follow-up backlog 4 条已 ✅；新增双源（F4 validator 复制）+ 1 个 Soft enforcement（F5）小幅抵消 |

字数：约 1480。
