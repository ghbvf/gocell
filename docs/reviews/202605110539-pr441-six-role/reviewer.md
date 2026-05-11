# PR #441 — Reviewer 维度审查

## 总体结论

**需修复（少量）**。PR 核心交付质量高：ISP 拆分四子接口 + ADR 记录完整、sealed marker type system Hard 防线有效、archtest 覆盖严密（三档分级均符合 ai-collab.md 要求）、rollback ctx 解耦测试全面。但存在以下须修复问题：

1. `kernel/cell/celltest/mux_test.go` 在 `kernel/` 层直接 import `runtime/auth`，违反分层规则
2. `interfaces_isp_test.go` 的函数注释含误导性陈述（"both sub-cases hit pairing invariant"）但测试体完全没调用 `ResolveCellEmitter`
3. `CELL-RAW-INFRA-WRAPPER-LOCATION-01` archtest 对 `kernel/cell/demo_tx_runner.go` 的 allowlist 扩展未做明确的 backlog 登记

---

## Finding 列表

### 分层合规

#### F1 [P0] [Cx2] [分层合规] `kernel/cell/celltest/mux_test.go:13`

**问题**：`kernel/cell/celltest/mux_test.go` 直接 import `"github.com/ghbvf/gocell/runtime/auth"`。根据 CLAUDE.md 和 go-standards.md，`kernel/` 禁止依赖 `runtime/`。该文件名虽为 `_test.go`，但它位于 `kernel/cell/celltest/` 包内（package `celltest`，非 `package celltest_test`），属于 kernel 层的测试辅助包，不属于游离测试。

**证据**：
```
kernel/cell/celltest/mux_test.go:1  package celltest
kernel/cell/celltest/mux_test.go:13 "github.com/ghbvf/gocell/runtime/auth"
```
文件本身注释也承认 `kernel/ 不依赖 runtime/`（第 174 行），但仍引入了该 import。

**建议**：
- 方案 A（Cx1 后续）：将引用 `runtime/auth` 的辅助函数 `kernelLocalRequireAuthenticated` 移到 `_test` 包（`package celltest_test`），彻底隔离。
- 方案 B：抽取 `auth.Principal`/`PrincipalAnonymous` 等常量到 `kernel/cell/auth_types.go`（已有该文件），在 test 中不 import `runtime/auth` 而只用 kernel 内部类型。
- 考虑在 `tools/archtest/module_order_test.go` 或 depguard 中显式守卫 `kernel/ → runtime/` 的测试文件路径。

**AI-rebust 评级**：此情形属 Soft（规则在 CLAUDE.md 中以文字约定，无 depguard 对 `_test.go` 的 kernel→runtime import 的机器守卫）。应登记 backlog 升级到 Medium（depguard 或 archtest 扫 `kernel/**/*_test.go` 中的 `runtime/` import）。

**Backlog 登记建议**：新增条目 `KERNEL-TEST-RUNTIME-IMPORT-BAN-01`，用 `.golangci.yml` depguard 或 archtest 守卫 `kernel/**/*_test.go` 禁止直接 import `runtime/`。

**Cx 分级**：Cx2（需修改 `mux_test.go` + 可能重构 auth helper 函数跨越包边界，但不改接口）。

---

#### F2 [P1] [Cx1] [分层合规] `kernel/governance/rules_http_test.go:37,63,800,1055`

**问题**：`kernel/governance/rules_http_test.go` 中四处直接 import `"github.com/ghbvf/gocell/runtime/auth"`，`kernel/metadata/meta_struct_guard_test.go:9` import `runtime/devtools/catalog`。虽为 `_test.go` 文件，但 `package governance` 内部测试包仍在 `kernel/` 层。

**证据**：
```
kernel/governance/rules_http_test.go:37  "github.com/ghbvf/gocell/runtime/auth"
kernel/metadata/meta_struct_guard_test.go:9 "github.com/ghbvf/gocell/runtime/devtools/catalog"
```

**建议**：这些 import 若是 PR #441 引入则需优先修复；若为历史遗留，应在 F1 的 backlog 条目内一并覆盖，不引入新平行依赖。[需确认] PR diff 是否新增了这些文件的 import——若为历史问题，降级为 P2 DX 项。

**Cx 分级**：Cx1（仅需确认范围，若是历史问题无需在本 PR 处理）。

---

### 测试/回归

#### F3 [P1] [Cx1] [测试] `kernel/cell/interfaces_isp_test.go:67-70`

**问题**：`TestCellSubInterfaces_IndependentMockability` 的函数注释第 69-70 行声称"Both sub-cases hit cell.ResolveCellEmitter::resolveDemoEmitter pairing invariant (writer XOR txRunner = error)"，但测试体（71-103 行）完全不调用 `ResolveCellEmitter` 或任何 emitter 相关 API，仅进行四子接口的编译期赋值断言。该注释是错误的，会误导读者和 AI 实施者以为这里测了 emitter 配对不变式。

**证据**：
```go
// interfaces_isp_test.go:67-70
// TestCellSubInterfaces_IndependentMockability documents that each sub-interface
// can be mocked without implementing the others, satisfying ISP.
// Both sub-cases hit cell.ResolveCellEmitter::resolveDemoEmitter pairing invariant  ← 错误
// (writer XOR txRunner = error).
func TestCellSubInterfaces_IndependentMockability(t *testing.T) {
    // ... 仅测试接口赋值，无任何 ResolveCellEmitter 调用
```

实际 `ResolveCellEmitter` 测试在 `kernel/cell/mode_resolver_test.go:265`。

**建议**：删除 69-70 行注释，改为准确描述："Verifies each sub-interface can be used independently, satisfying ISP. Negative paths cover unhealthy/unready status mock."

**Cx 分级**：Cx1（仅改注释，1 文件）。

---

#### F4 [P1] [Cx1] [测试] `kernel/cell/interfaces_isp_test.go`

**问题**：`TestCellSubInterfaces_IndependentMockability` 覆盖四子接口独立 mockability，但缺少对 `CellLifecycle` 的负向路径测试（如 `Init` 在非法状态下的错误路径）。测试仅调用 `cl.Start()` 的正向路径。`CellStatus` 负向路径通过 `unhealthy` mock 覆盖（第 95-102 行）。

这是边界用例不完整问题：`CellLifecycle` 子接口只有 mock 的 happy path，没有验证 "lifecycle 违规时可独立 mock 返回 error" 的行为（这一能力是 ISP 价值体现之一）。

**建议**：增加 `lifecycleMock` 的错误返回变体，验证 `CellLifecycle` 可以独立返回错误（对 ISP mock 独立性才是完整验证）。

**Cx 分级**：Cx1（1 文件内加 1 个子测试）。

---

#### F5 [P2] [Cx1] [测试] `kernel/cell/interfaces_isp_test.go:109-120`

**问题**：`TestCell_CompositeEquivalence` 只做编译期 nil pointer 断言（`var c Cell = (*BaseCell)(nil)`），不包含任何运行时行为断言。这与 PR 标题的"equivalence"目标相比偏弱——复合接口等价性（composite ≡ 四子接口并集）在运行时路径上没有回归保护。

**证据**：
```go
func TestCell_CompositeEquivalence(t *testing.T) {
    t.Parallel()
    var c Cell = (*BaseCell)(nil) // compile-time only
    _ = c
```

**建议**：此为 P2，编译期断言已由 `base.go` 中 4 段式 `var _` 提供 Hard 保护，测试仅为文档级补充。可维持现状并在注释中说明"编译期已守卫，此测试为文档断言"。

**Cx 分级**：Cx1（补 1 行 godoc 说明即可）。

---

### DX / 可维护性

#### F6 [P1] [Cx1] [DX] `kernel/cell/interfaces_isp_test.go:37,50`

**问题**：`TestCellIfaceISP03_BaseCellFourSegmentCheck`（archtest）的失败消息在 `subSeen` 漏掉某个子接口时，只报"missing `var _ X = (*BaseCell)(nil)` compile-time check"，但不显示当前文件中实际发现了哪些子接口断言（`subSeen` 的内容）。当实施者不小心添加了拼写错误的变体（如 `CellIDentity`）时，失败消息没有指明"你有哪些、缺了哪些"，调试成本高。

**证据**：
```go
// tools/archtest/cell_iface_isp_test.go:153-158
for _, name := range expectedSubInterfaces {
    if !subSeen[name] {
        t.Errorf("CELL-IFACE-ISP-BASECELL-CHECK-01: kernel/cell/base.go missing "+
            "`var _ %s = (*BaseCell)(nil)` compile-time check", name)
    }
}
```

**建议**：在循环之前添加 `t.Logf("found checks: %v", subSeen)`，让失败时可见"你有哪些"。

**Cx 分级**：Cx1（1 文件，1-2 行 `t.Logf`）。

**AI-rebust 评级**：不涉及新增 enforcement 机制，此为纯 DX 优化，不适用三档评级。

---

#### F7 [P2] [Cx1] [DX] `docs/architecture/202605101800-adr-cell-interface-isp-split.md:82-119`

**问题**：ADR §D6 正文保留了"已删除的旧 archtest CELL-RAW-DEPS-01"的实施细节（三元组 allowlist 格式、allowlist 示例表格、SHA-256 hash guard 等约 37 行），标注为"以新 ADR 为准"。这部分历史细节读起来像当前状态，可能引导 AI 在后续 session 重建已删除的 scanner。

**建议**：在 §D6 保留"已删除"声明，折叠或删除具体实施细节，仅保留到新 ADR 的交叉引用。

**Cx 分级**：Cx1（1 文件，文档编辑）。

---

#### F8 [P2] [Cx1] [DX] `kernel/command/sweeper.go:86-106`（AI-rebust 评级项）

**问题**：`Sweeper` 的 `built` sentinel 模式在 godoc 中自标注为 Medium，并提到"Hard upgrade path: make Sweeper an opaque interface returned only by NewSweeper"。按 ai-collab.md，Medium 暂留须在 backlog 显式登记升级条目，且注释中有"Out of scope for the current hardening pass"字样，说明这是一个 **silent carryover**。

**证据**：
```go
// kernel/command/sweeper.go:95-106
// AI-rebust 评级：**Medium (runtime fail-closed sentinel)**.
// ...
// Hard upgrade path (backlog): make Sweeper an opaque interface returned
// only by NewSweeper, so the zero value is unspeakable at the type level.
// Out of scope for the current hardening pass; ...
```
搜索 `docs/backlog.md` 未见 `COMMAND-SWEEPER-HARD-01` 或等价条目。

**建议**：在 `docs/backlog.md` 添加：
```
| COMMAND-SWEEPER-OPAQUE-INTERFACE-01 | Sweeper built sentinel 升级 Hard — 现状: Medium runtime guard; 修复: 改为 opaque interface，零值不可表达 | arch-opt | P3/Cx3 | 🟢 | Sweeper 被第 2 个 cell 使用 | kernel/command/sweeper.go | sweeper.go godoc |
```

**AI-rebust 评级**：Cx1（仅 backlog 文件新增 1 行条目）。

---

### 安全

#### F9 [P2] [Cx1] [安全/权限] `kernel/outbox/cell_marker.go:63-76`

**问题**：`WrapForCell` / `WrapPublisherForCell` / `WrapWriterForCell` 的 godoc 中 `Allowed callers` 列表包含 `kernel/cell/demo_tx_runner.go`（`DemoCellTxManager()` factory），但 PR #441 是本次新增该 allowlist 扩展点。ADR 202605101900 §D5 已说明理由，但 `tools/archtest/wrapper_location_test.go` 中对 `demo_tx_runner.go` 的 allowlist 项应有对应的 negative probe 验证 scanner 识别该文件路径。

[需确认] 在 `wrapper_location_test.go` 中是否有针对 `demo_tx_runner.go` 路径的 negative/positive probe 测试；若无，`DemoCellTxManager` 路径可能被 AI 在其他文件仿写而不被 archtest 拦截。

**Cx 分级**：Cx1（确认后若缺少，加 1 条 fixture case）。

---

### 运维/部署

#### F10 [P2] [Cx1] [运维] `kernel/assembly/assembly.go:491-519`（rollback ctx 变更）

**问题**：`startCellWithHooks` 中 `BeforeStart` 失败走 `rollbackCells(i - 1)`，不包含当前 cell i；`AfterStart` 失败走 `rollbackCells(i)`，包含 cell i。PR `rollback_ctx_test.go` 覆盖了这两个分支（`TestRollbackCells_DerivedCtx` + `TestRollbackCells_AfterStartFail_DerivedCtx`）。

但有一个边界情形缺少覆盖：**`i == 0` 时 `BeforeStart` 失败走 `rollbackCells(-1)`**，代码路径是 `upTo < 0 → return early`。虽然 `rollbackCells` 有 `if upTo < 0 { return }` 守卫，但没有测试断言当第一个 cell 的 `BeforeStart` 失败时，assembly 正确回到 `stateStopped` 且不产生 goroutine 泄漏。

**建议**：在 `rollback_ctx_test.go` 增加一个 `i=0, BeforeStart fails` 的 table case，用 `goleak.VerifyNone` 验证无泄漏，并断言 `a.Start` 返回 error 后 `a.Snapshots() == nil`。

**Cx 分级**：Cx1（1 文件加 1 table case）。

---

## 复杂度汇总

| 等级 | 数量 | Findings |
|------|------|----------|
| Cx1 | 10 | F1(部分)、F2、F3、F4、F5、F6、F7、F8、F9、F10 |
| Cx2 | 1 | F1 |
| Cx3 | 0 | — |
| Cx4 | 0 | — |

## 优先级汇总

| 优先级 | 数量 | Findings |
|--------|------|----------|
| P0 | 1 | F1（kernel/runtime 分层违规） |
| P1 | 4 | F2、F3、F4、F8 |
| P2 | 5 | F5、F6、F7、F9、F10 |

---

## 修复分流建议

**Cx1/Cx2 → 派发 `developer` agent**：
- F1：修复 `kernel/cell/celltest/mux_test.go` 的 `runtime/auth` import（改 package 或抽内联 helper）
- F3：删除 `interfaces_isp_test.go:69-70` 的错误注释
- F4：补 `CellLifecycle` 负向 mock 路径测试
- F6：在 archtest 的 `subSeen` 缺失错误中增加 `t.Logf`
- F8：在 `docs/backlog.md` 添加 `COMMAND-SWEEPER-OPAQUE-INTERFACE-01` 条目
- F10：在 `rollback_ctx_test.go` 增加 `i=0, BeforeStart fails` table case

**需人工确认后决策**：
- F2：确认 `kernel/governance/rules_http_test.go` 的 `runtime/auth` import 是否为 PR #441 新引入或历史遗留
- F9：确认 `wrapper_location_test.go` 是否已有 `demo_tx_runner.go` 的 positive probe

---

## 正向亮点（非 Finding，供参考）

以下是本 PR 质量较高的部分，建议作为范本：

1. **sealed marker 三层防线设计**：type system Hard（字段赋值不可达）+ archtest Medium（公开参数签名形态）+ wrapper location archtest Medium（调用站点约束）——完全符合 ai-collab.md 载体决策分层原则，且每层都有对应的 negative fixture 验证 scanner 检测能力。

2. **ADR 正文与 backlog 同步**：ADR 202605101800 §D4 的 `SLICE-ISP-DEFERRED` 在 `docs/backlog.md:327` 明确登记，触发条件清晰，无 silent carryover。

3. **rollback ctx 解耦测试**：`rollback_ctx_test.go` 的 table-driven 测试覆盖了三种 HookTimeout 场景（default/positive/negative=disable），并通过 `single-budget invariant` 断言两个 cell 的 Stop 共享同一 rollback ctx deadline——这是对 ADR 202605051800 的精确回归。

4. **sealed Nooper 透传**：`internalCellTxManager.Noop()` / `internalCellPublisher.Noop()` / `internalCellWriter.Noop()` 通过局部 anonymous interface 断言透传内层 Nooper，避免 kernel/persistence 反向依赖 kernel/cell，边界意识清晰。

5. **SHA-256 hash guard**：`TestCellIfaceISP00_MethodSetsHashGuard` 通过哈希锁定子接口方法集合，修改需同步更新常量并附 ADR，达到 Hard 级自描述约束。
