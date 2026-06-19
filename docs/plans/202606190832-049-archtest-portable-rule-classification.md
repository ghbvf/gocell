# 049 archtest 可移植规则分类口径 + 迁移 roadmap（#1878-A）

> **真值源指针**：本文档是时间点迁移计划，**不是**维护中的全量映射。  
> 权威活口径 = #2330 代码注册表（`StandardCellRules()` 函数 + 外部安全 API）；  
> 架构背景 ADR = `docs/architecture/202606041600-1555-adr-archtest-workspace-root-model.md`、`docs/architecture/202605281200-adr-cell-development-external-repo.md`；  
> Epic = GitHub #1878；相关 issue = #2329（RuntimeScopeConfig）、#2330（注册表 API）、#2333（batch-1 迁移 PR）。

---

## 1. 焊接点 / 定位

### 1.1 活口径单源是代码注册表，不是本文档

`tools/archtest/external.go` 的 `StandardCellRules()` 是已迁移外部安全规则的权威集合；其
godoc 块同时列出了"明确不注册"的内部规则及原因。任何规则的当前桶归属，以该文件为准。

本文档的职责仅有两个：

1. 给出**机读派生口径**（§2），让"某规则属于哪个桶"可从规则代码本身计算，而不依赖手工清单；
2. 给出**迁移 roadmap**（§4），为 #2333 提供可操作的 batch-1 列表，并为 batch-2/3 描述信号品类。

若本文档与 `StandardCellRules()` 冲突，**以代码为准，本文档应更新**。刻意不在此维护全量
370-ID 映射——手工映射是第二真值源，规则一旦新增或修改便立即漂移，制造虚假安全感。

### 1.2 与 #2329 RuntimeScopeConfig 的关系

`tools/archtest/scope_config.go` 引入的 `RuntimeScopeConfig`（sealed 构造，`DefaultScopeConfig()`
唯一工厂）将 workspace root、target module path、platform/framework module path、platform cell
scan dirs 四维合并为单一密封值，是规则迁移的运行时载体：

- **conditional 桶规则**迁移的关键在于将 scan dir / platform cells path 从硬编码字面量切换为
  从 `RuntimeScopeConfig` 派生。迁移前规则写死 `"corecells/..."` 扫描目标；迁移后从
  `cfg.PlatformCellScanDirs()` 取，外部消费仓替换为自身 cells 目录即可正确 fire。
- `TestDefaultScopeConfig_ReproducesGoCellDefaults`（#2329 单测锁）保证替换对 GoCell 自身
  行为字节兼容。

---

## 2. 三桶判定口径（可派生机读信号）

下表的三列信号**机器可扫**（grep 路径域字面量、检查 `Production` scope 使用、检查派生 vs 字面量），
故任何规则的桶归属无需查手工清单——从规则源码计算即可。

| 桶 | 判定信号（机器可扫） | 外部仓行为 |
|---|---|---|
| **external-safe** | ① 扫描目标 = running module 自身 Go 包（`Run(t, Typed(...))`/`Production(...)`，路径从 go.mod 推导）；② 平台符号路径从 `PlatformModulePath` / `PlatformFrameworkModulePath` 派生（无裸字面量，由 `ARCHTEST-MODULE-PATH-FUNNEL-01` 守）；③ allowlist 均为 GoCell-internal 包（`isGoCellPlatformPkgPath` 绑定），故在消费仓无豁免位，整规则变成**纯 ban**；④ 无 `corecells/` / `generated/` / `contracts/` / `examples/` 路径域字面量；⑤ 无 sealed self-check / reflect 平台类型 AST-parse | 真实 fire——消费仓代码满足 ban 条件即报错；allowlist 为空（消费仓无 GoCell-internal 豁免路径）→ 纯 ban，无假安全 |
| **gocell-internal** | 任意一项：引用 GoCell 内部布局路径域（`corecells/` / `generated/` / `contracts/` / `examples/`）；或 AST-parse GoCell 平台源文件（如 `pkg/errcode/details.go`）；或 reflect GoCell 平台类型（如 `errcode.PublicDetail`）；或 conformance enrollment 扫描；或 `Production` scope 下扫描硬编码的 GoCell composition root | vacuous-green（消费仓无对应路径/类型，扫零目标→零违规→假通过）或 false-red（消费仓有合理代码被误报） |
| **conditional** | scan dir / module path 通过字面量写死（如 `"./corecells/accesscore/..."`），但将字面量替换为 `RuntimeScopeConfig` 派生后对消费仓有意义；allowlist 不依赖 GoCell-specific 文件路径 | 替换后 fire；迁移成本低，值得列入 batch-2 |

### 2.1 信号的机器可验证性

| 信号 | 怎么扫 |
|---|---|
| 路径域字面量 | `grep -r '"corecells/\|/generated/\|/contracts/'` archtest 源码 |
| Production scope 使用 | `grep 'Production(' tools/archtest/*.go` |
| 平台符号派生 vs 字面量 | `ARCHTEST-MODULE-PATH-FUNNEL-01` 已是 CI 守卫（非裸字面量 = 通过） |
| allowlist 绑 isGoCellPlatformPkgPath | grep `isGoCellPlatformPkgPath` in allowlist/key derivation |
| conformance enrollment | `grep 'checkSagaConformanceEnrollment\|conformance'` tools/archtest |

---

## 3. 现状 seed（引用已编码事实）

### 3.1 已迁移外部安全集（StandardCellRules 当前 10 条）

以下 ID 直接引自 `StandardCellRules()`（见 `external.go:284–334`），不复制判断：

| # | Rule ID |
|---|---|
| 1 | `PANIC-REGISTERED-01` |
| 2 | `ERRCODE-KIND-LITERAL-01` |
| 3 | `MESSAGE-CONST-LITERAL-01` |
| 4 | `EXPORTED-ERROR-NEW-01` |
| 5 | `SCAFFOLD-DERIVED-FORCEOVERWRITE-01` |
| 6 | `OUTBOX-RECONSTRUCTION-CALLER-01` |
| 7 | `PROJECTION-APPLY-HOOK-FUNNEL-01` |
| 8 | `OUTBOX-HANDLERESULT-FACTORY-PREFERRED-01` |
| 9 | `SAGA-STEP-COMPENSATE-PURE-01` |
| 10 | `PROD-MAIN-WIRING-NOOP-REJECT-01` |

这 10 条已完成 M3 迁移（importable Check* + CellRule 注册），是 Epic #1878 的已交付基线。

### 3.2 明确不注册的 gocell-internal 规则

`external.go` godoc 已列出以下"明确不注册"条目及原因摘要（以 external.go 为权威，此处
仅统计分类依据）：

- **vacuous-green 类**：`ERROR-FIRST-API-01`、`ERROR-FIRST-TYPED-NIL-01`（硬编码 22-路径正向
  allowlist，外部仓无对应路径）；`OUTBOX-TOPIC-FAILOPEN-01`（sealed entry → 消费仓 compile error
  前置）；`SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01`（扫 tools/codegen，消费仓无该路径）
- **false-red + internal layout 类**：`DETAILS-SEALED-FIELD-FROZEN-01`（AST-parse
  `pkg/errcode/details.go`，消费仓无该文件）；整个 `SAGA-*` 内部族（14 条，扫
  GoCell runtime/saga layout 或 conformance enrollment）

### 3.3 聚合规模估算

通过路径域信号扫描 `tools/archtest/` 可得：

- 引用 `corecells/` / `generated/` / `contracts/` 的 `.go` 文件（包含 test）约 **130 个**，
  其中非 test 文件约 **33 个**——这 33 个文件内含规则均倾向 gocell-internal。
- archtest 包总 Go 文件 **447 个**（含 test）；非 test 文件远小于此。
- 初步估算：gocell-internal 规则占多数（>60%），external-safe 已迁移 10 条，conditional
  桶约 10–20 条（主要是 cell-scan 类规则，scan dir 参数化后有意义）。

精确按 rule-id 计数不是本文档目标——以 `StandardCellRules()` + 后续 `#2330` 注册表 API 为准。

---

## 4. 迁移 roadmap

### 4.1 Batch-1（近期，~10–15 条，供 #2333 直接执行）

**标准**：已有 importable Check*，allowlist 为 GoCell-internal 包（`isGoCellPlatformPkgPath`
绑定），在消费仓内 allowlist 为空，故整规则是纯 ban，可直接注册无副作用。

以下是从 signal 判定口径（§2）和已有规则源码派生出的候选 rule-id。这是 **短暂性工作列表**，
由 #2333 消费后失去维护价值，以实际 PR 合并结果为准：

| Rule ID | 当前状态 | 迁移阻力 | 备注 |
|---|---|---|---|
| `CLOCK-POSITIONAL-INJECTION-01` | importable，未注册 | 低 | allowlist = composition-root carve-outs（GoCell-internal）→ 外部仓无豁免位，纯 ban；godoc 注明"vacuous/false-red 外部"但实为 allowlist 全空 = 纯 ban 而非假通过，须复核 |
| `BCRYPT-COST-FUNNEL-01` A1 臂 | importable，未注册 | 中 | A1 callee-location 绑 `credential/hasher.go`（GoCell-internal）→ 纯 ban；A2 allowlist 含 GoCell 测试路径，注册时考虑拆臂 |
| `AUTHZ-MUTATION-APPLY-FUNNEL-01` | importable，未注册 | 中 | allowlist 绑 `PlatformCellsModulePath/accesscore/internal/...`（isGoCellPlatformPkgPath 派生），外部仓无豁免 → 纯 ban；须确认 scan target = 消费仓模块 |
| `DOMAIN-AUTHZ-FIELD-PRIVATE-01` | importable，未注册 | 中 | 扫 domain.User 类型，allowlist 绑 accesscore internal，外部仓 → 消费仓如无同名 domain.User 则 vacuous-green，须标注此盲区 |
| `AFTERCOMMIT-HOOK-PURE-TRANSIENT-01` | importable，未注册 | 中-高 | A3 drain-caller allowlist 含 GoCell-internal TxRunner 路径；注册前须支持 `ConfigForExternalCell.ExtraAllowlist` 或改为纯 ban（`RegisterAfterCommit` 调用即报，无豁免）—— 二选一 |
| `ERRCODE-PREFIX-OWNERSHIP-01` | importable，未注册 | 高 | 扫 `pkg/errcode/testdata/prefix_set.golden`（GoCell-internal 文件），外部仓该文件不存在 → false-red；需 `RuntimeScopeConfig` 参数化 golden 路径才可迁移；归 conditional 桶或 batch-2 |
| `DISTLOCK-LOCK-NOT-CONTEXT-01` | importable，未注册 | 低 | self-check：`runtime/distlock.Lock` 不 implement `context.Context`；scan scope = GoCell framework module，消费仓无该类型 → vacuous-green；不可迁移（应归 gocell-internal） |

> **注**：上表为派生候选，不是冻结清单。`DISTLOCK-LOCK-NOT-CONTEXT-01` 经信号判定后
> 归 gocell-internal，不应进入 batch-1，列出仅作对比说明口径正确性。`CLOCK-POSITIONAL-INJECTION-01`
> 须在 #2333 中实证：若 carve-out allowlist 经 `isGoCellPlatformPkgPath` 绑定，则为纯 ban；
> 若为路径字面量，则归 conditional 桶先行参数化。

**真正适合 batch-1 的核心条件**：allowlist 要么为空（纯 ban），要么全部经 `isGoCellPlatformPkgPath`
绑定（消费仓无对应包 → allowlist 运行时为空 → 等价纯 ban）。满足此条件的规则零改动可注册。

### 4.2 Batch-2（signal-category，conditional 类，不逐一枚举）

**信号品类**：scan dir 字面量硬编码（`"./corecells/..."` 等），但逻辑本身与消费仓 cells 目录结构兼容。

**迁移路径**：将字面量替换为 `RuntimeScopeConfig.PlatformCellScanDirs()` 供给，外部消费仓
通过 `ConfigForExternalCell`（或后续 `#2330` 扩展字段）覆盖扫描根。`DefaultScopeConfig()`
的默认值锁测试保证 GoCell 自身不回归。

**代表品类**（不枚举 ID）：`cell-scan 类`（扫 platform cells 的 layer/import/style 规则）、
`composition-root 类`（扫 `cmd/` / `cellmodules/`，外部仓有类似 composition root 需参数化路径）。

**前置条件**：#2329 `RuntimeScopeConfig` 落地（本 PR 分支已完成）；#2330 注册表 API 提供
consumer scan-dir 覆盖扩展点（blocked-by #2330）。

**风险**：双维护成本——GoCell 自身路径 + 消费仓路径均须在规则内正确派生，测试须覆盖两侧。

### 4.3 Batch-3（gocell-internal，永不迁移）

**信号品类**：conformance enrollment scan、reflect 平台类型 self-check、AST-parse 平台源文件、
`Production` scope 下扫描写死 GoCell composition root。

**处置**：永远留在 `tools/archtest/` in-repo，不注册进 `StandardCellRules()`。`external.go`
godoc 已列出不注册理由，是这批规则的权威说明文本。

**代表品类**（以 external.go 现有文本为真值源，不另列）：`SAGA-*` 内部族（~14 条）、
`DETAILS-SEALED-FIELD-FROZEN-01`、`ERROR-FIRST-*`（正向 allowlist 硬锁平台路径）、
`DISTLOCK-LOCK-NOT-CONTEXT-01`（self-check，消费仓无目标类型）。

### 4.4 优先级与风险矩阵

| Batch | 优先级 | 主要风险 | 缓解 |
|---|---|---|---|
| Batch-1 | 高（#2333 即期交付） | **vacuous-green 假安全信号**：allowlist 误判导致规则在消费仓静默通过但无真实覆盖 | 注册前用 `isGoCellPlatformPkgPath` 验证 allowlist 绑定；补外部仓 RED fixture |
| Batch-1 | 高 | **forged-path bypass**：外部仓伪造 GoCell-internal 相对路径骗取豁免 | allowlist 必须经 `isGoCellPlatformPkgPath(pkgPath)` 绑定（package-identity，非 rel-path），见 `outbox_reconstruction_caller.go` §forged bypass 描述 |
| Batch-2 | 中（blocked by #2329/#2330） | **双维护成本**：scan dir 参数化后每次 GoCell 内部布局调整须同步 consumer 文档 | `DefaultScopeConfig()` 锁测试保底；consumer 显式声明 scan dirs（opt-in） |
| Batch-3 | 不迁移 | N/A | 永远 in-repo；external.go godoc 持续记录原因 |

---

## 5. #2333 验收口径

一条规则迁移为 external-safe（进入 `StandardCellRules()`）须同时满足以下门禁：

| # | 验收条件 | 验证方式 |
|---|---|---|
| G1 | 规则逻辑位于非 test `.go` 文件（`Check*` 函数可被外部 module 编译） | `go build ./tools/archtest/...` 在外部模块 `go get` 后成功 |
| G2 | 所有平台符号路径从 `PlatformModulePath` / `PlatformFrameworkModulePath` 派生，无裸字符串字面量 | `ARCHTEST-MODULE-PATH-FUNNEL-01` CI 通过 |
| G3 | 扫描目标 = running module（`Run(t, Typed(...))` 或 `Production(...)` 路径从 go.mod 推导，非硬编码 GoCell 路径） | 规则代码不含 `"./corecells/..."` 等路径字面量，或已参数化为 `RuntimeScopeConfig` 派生 |
| G4 | allowlist 完全经 `isGoCellPlatformPkgPath(pkgPath)` 绑定：在消费仓（pkgPath ∉ `PlatformModulePath/*`）中运行时 allowlist 运行时为空，规则退化为**纯 ban** | 补一个外部仓 RED fixture（`testdata/` 下造违规文件），验证规则对消费仓代码真实报错 |
| G5 | `CellRule` 注册 `{ID: ruleXxx, Run: CheckXxx}` 进 `StandardCellRules()` 返回切片 | `TestRunStandardCellRules` 在 CI 通过 |
| G6 | 如规则依赖 `ConfigForExternalCell` 扩展字段（如 `BuildTags`/`ProductionMainPkgs`），文档注明 opt-in 语义且空值不产生 false-positive | `RunStandardCellRules` 的零值 cfg 场景覆盖 |
| G7 | 无新增 Soft-grade enforcement（见 ai-robust.md §分级） | PR 内含 AI-robust 评级注释（archtest godoc 写 Hard/Medium/Soft） |
