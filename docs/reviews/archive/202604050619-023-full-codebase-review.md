# GoCell 全代码库 Review 报告

> 日期: 2026-04-05  
> 范围: 63 文件 / ~12,400 行 / 10 个并行 reviewer  
> 分支: develop  
> 基线: 首次 review，Phase 0~1 全部代码

## 总览

| 严重级别 | 数量 | 说明 |
|---------|------|------|
| **BUG** | 6 | 必须修复 |
| **SECURITY** | 2 | 路径遍历风险 |
| **DESIGN** | 19 | 应当修复 |
| **NIT** | ~20 | 锦上添花 |

测试整体通过（governance 3 个用例已修复），覆盖率 kernel/ 层均 >= 90%。依赖合规性 PASS——kernel/ 无违规导入。

### 覆盖率

| 包 | 覆盖率 | 要求 |
|---|--------|------|
| kernel/governance | 96.3% | >= 90% |
| kernel/assembly | 94.4% | >= 90% |
| kernel/scaffold | 93.0% | >= 90% |
| kernel/slice | 94.1% | >= 90% |
| kernel/cell | 100% | >= 90% |
| kernel/registry | 100% | >= 90% |
| kernel/journey | 100% | >= 90% |

---

## BUG — 必须修复

### B1. TOPO-03 未处理通配符 `"*"` — 产生误报

**文件**: `src/kernel/governance/rules_topo.go:111`  
**发现者**: Correctness / Go Idioms / Architecture 三个 reviewer 独立发现

```go
if len(consumers) > 0 && !containsString(consumers, s.BelongsToCell) {
```

当 contract consumers 为 `["*"]`（任意 cell 可消费）时，`containsString(["*"], "my-cell")` 返回 false，所有 consumer-role slice 均被误报。REF-14 正确跳过了 `"*"` 但 TOPO-03 没有。

**修复**: 增加 `&& !containsString(consumers, "*")`。

---

### B2. VERIFY-01 把无效 waiver 当作有效覆盖

**文件**: `src/kernel/governance/rules_verify.go:29-35`  
**发现者**: Correctness reviewer

当 `expiresAt` 为空字符串时，`if w.ExpiresAt != ""` 为 false，waiver 不被跳过，直接加入 `waiverSet`——被视为"永远有效"的覆盖。但 VERIFY-02 独立报告该 waiver 缺失 `expiresAt`。结果矛盾：usage "已覆盖" + waiver "已损坏"。

同理，当 `expiresAt` 为不可解析字符串（如 `"not-a-date"`）时，`time.Parse` 失败，`err == nil` 为 false，`continue` 不执行，waiver 同样加入 `waiverSet`。

**修复**: 当 `expiresAt` 为空或解析失败时，不应将 waiver 加入 `waiverSet`（视为无效 waiver，不提供覆盖）。

---

### B3. FMT-08 静默跳过无 `.` 的 contract ID — 漏报

**文件**: `src/kernel/governance/rules_fmt.go:213`  
**发现者**: Correctness reviewer

`strings.SplitN(id, ".", 2)` 对 `"nodot"` 返回 1 个元素 → `continue` 跳过。ID 为 `"http"`（kind 也是 `"http"`）的 contract 会通过 FMT-08，且没有其他规则捕获格式不合法的 contract ID。

**修复**: `len(parts) < 2` 时应报错"contract ID 格式不合法（缺少 `.` 分隔符）"，而非 `continue`。

---

### B4. metadata parser: 省略 `belongsToCell` 导致 map key 损坏

**文件**: `src/kernel/metadata/parser.go:130-136`  
**发现者**: Metadata reviewer

当 slice YAML 省略 `belongsToCell` 时（JSON schema 明确允许），parser 不从目录路径推断 cell ID，导致 map key 变为 `"/sliceID"` 而非 `"cellID/sliceID"`。下游所有按 key 查找 slice 的逻辑均会失败。

**修复**: 从目录路径 `cells/{cellID}/slices/{sliceID}/` 推断 `belongsToCell`。

---

### B5. `CoreAssembly.Register()` 缺 mutex — 数据竞态

**文件**: `src/kernel/assembly/assembly.go:56-67`  
**发现者**: Assembly reviewer

`Start()` / `Stop()` 持锁，但 `Register()` 不持锁。并发调用 Register + Start 会竞争 `a.cells`（slice）和 `a.cellMap`（map）。

**修复**: `Register()` 加 `a.mu.Lock()` 或文档明确标注 "Register must complete before Start, not safe for concurrent use"。

---

### B6. `slice/verify.go` 路径遍历 — cellID 含 `..` 可逃逸

**文件**: `src/kernel/slice/verify.go:62`  
**发现者**: Slice reviewer

`parseSliceKey` 仅检查空值。输入 `"../../etc/session-create"` 可通过校验，被拼入 `go test ./cells/../../etc/slices/session-create/...` 路径，逃逸出 `./cells/` 目录。

**修复**: 拒绝含 `..` 或 `filepath.Separator` 的 cellID / sliceID。

---

## SECURITY — 安全风险

### S1. governance REF-11/REF-12 `os.Stat` 路径遍历

**文件**: `src/kernel/governance/rules_ref.go:254-255`（REF-11）, `308-309`（REF-12）  
**发现者**: Security reviewer

`build.entrypoint` 或 `schemaRefs` 含 `../../../etc/passwd` 时，`filepath.Join(repoRoot, entrypoint)` + `os.Stat` 可探测任意路径。错误消息泄露完整路径。

攻击面有限（需控制 YAML 内容），但违反纵深防御原则。

**修复**: `os.Stat` 前检查 `filepath.Clean(fullPath)` 是否在预期 root 下：
```go
cleaned := filepath.Clean(fullPath)
if !strings.HasPrefix(cleaned, filepath.Clean(expectedRoot) + string(os.PathSeparator)) {
    // 报告路径非法，不执行 os.Stat
}
```

---

### S2. Nil project 导致 panic

**文件**: `src/kernel/governance/validate.go:51`  
**发现者**: Security reviewer

`NewValidator(nil, ".")` → 所有规则立即 nil 指针崩溃。公共 API 无保障。

**修复**:
```go
func NewValidator(project *metadata.ProjectMeta, root string) *Validator {
    if project == nil {
        project = &metadata.ProjectMeta{}
    }
    return &Validator{project: project, root: root}
}
```

---

## DESIGN — 应当修复

### 治理引擎 (governance)

| # | 文件 | 问题 | 建议 |
|---|------|------|------|
| D1 | `rules_verify.go:11` | `var nowFunc` 包级变量，不支持 `t.Parallel()` | 移到 `Validator` 结构体字段 `now func() time.Time` |
| D2 | `rules_ref.go:243-267` | `os.Stat` 破坏 kernel 纯验证模型 | 注入 `FileExistsFunc func(path string) bool` 到 Validator |
| D3 | `validate.go:109-138` | `HasErrors`/`Errors`/`Warnings` 不用 receiver | 改为包级函数 |
| D4 | `rules_topo.go` + `rules_ref.go` | `contractProvider`/`contractConsumers` + 文件路径 helpers 被全包使用但放错文件 | 抽到 `helpers.go` |
| D5 | cross-cutting | 无 `contract.kind` 枚举校验规则，`kind: "grpc"` 仅间接被 FMT-07 拦截 | 新增 FMT-09 校验 kind 枚举 |
| D6 | `rules_fmt.go:161`, `rules_verify.go:144` | 原始字符串 `"L0"` 比较绕过 `ParseLevel` | 统一使用 `ParseLevel` 后比较 |
| D7 | `validate.go:36-42` | `//nolint:unused` 标注错误：Severity/Message 有使用；`Details` 是死代码 | 移除错误标注；决定 Details 去留 |

### 核心类型 (cell)

| # | 文件 | 问题 | 建议 |
|---|------|------|------|
| D8 | `cell/types.go` | 仅 `ParseLevel` 存在；缺 `ParseCellType`/`ParseContractKind`/`ParseContractRole`/`ParseLifecycle` | 补全 Parse* 函数族 |
| D9 | `cell/base.go:43` | `BaseCell` 无生命周期状态机 — Start without Init、double-Start 均允许 | 增加状态枚举 `{new, initialized, started, stopped}` 并校验转换 |
| D10 | `cell/base.go:137-161` | `BaseContract` 无 `SetLifecycle` | 添加 setter 或构造器参数 |

### Metadata / Parser

| # | 文件 | 问题 | 建议 |
|---|------|------|------|
| D11 | `metadata/parser.go:121` | Parser 无重复 ID 检测 — 静默 map 覆写 | 遇重复 ID 返回错误 |
| D12 | `metadata/schemas/embed.go` | `schemas.FS` 已嵌入但全库无人使用 | 移除或计划使用 |

### Scaffold / Assembly

| # | 文件 | 问题 | 建议 |
|---|------|------|------|
| D13 | `scaffold/templates/*.tpl` | YAML 值未引号包裹 → 含 `:` `#` 的值会破坏输出 | 字符串值加双引号 `"{{.Value}}"` |
| D14 | `scaffold/scaffold.go:130` | `CreateContract` 不校验 ID 前缀 ≠ Kind | 添加 `parts[0] == opts.Kind` 校验 |
| D15 | `assembly.go:96,110,137` | `fmt.Errorf` 代替 `errcode.Wrap` — 违反错误处理规范 | 改用 `errcode.Wrap` |

### CLI / Runtime

| # | 文件 | 问题 | 建议 |
|---|------|------|------|
| D16 | `cmd/gocell/verify.go:70` | CLI verify 命令未接入 `kernel/slice.Runner` — 功能断裂 | 接入 Runner |
| D17 | `kernel/slice/verify.go:41` | `Runner.cells` 字段从未使用 — 死代码 | 移除未用字段 |

### 其他

| # | 文件 | 问题 | 建议 |
|---|------|------|------|
| D18 | `rules_verify.go:31,120` | `time.Truncate(24h)` 隐式 UTC 假设 | 显式 `.UTC().Truncate(...)` |
| D19 | `go.mod:4` | `go 1.25` 尚不存在（截至 2026-04） | 确认是否 intentional |

---

## NIT — 精选要点

| 文件 | 问题 |
|------|------|
| `rules_fmt.go` FMT-01/02/05 | 常量 lookup `map[string]bool` 每次调用重建 → 提到包级变量 |
| `rules_ref.go:271-280` | `repositoryRoot` 启发式脆弱 — 仅检查目录名 `"src"` → 加文档或用 `.git`/`go.mod` 探测 |
| `rules_ref.go:397` | `actorExists` 线性扫描 Actors → 预建 `map[string]bool` |
| `rules_ref.go:208 vs 326` | `contractFile` 和 `contractDirFromID` 重复逻辑 + 路径分隔符不一致（`/` vs `filepath.Separator`） |
| `rules_topo.go:206` | TOPO-06 map 遍历顺序不确定 → 错误消息非确定性 → 排序 key 或对称报告 |
| `cell/interfaces.go:63-76` | `Cell` 接口 11 方法偏大 → 考虑拆分 `Lifecycle` / `HealthChecker` 子接口 |
| `cell/interfaces.go:73-75` | `OwnedSlices()` / `ProducedContracts()` 返回可变切片 → 文档说明或返回 copy |
| `cell/base.go` | 缺接口合规编译检查 `var _ Cell = (*BaseCell)(nil)` |
| `assembly.go:105` | rollback `_ = Stop()` 忽略错误 → 至少 `slog.Warn` |
| `slice/verify.go:213` | `isExitError` 用类型断言 → 应用 `errors.As` |
| `outbox.go:12` | `Entry` 缺 `Metadata`/`Headers` 字段（参考 Watermill `Message.Metadata`） |
| `idempotency.go` | 缺 `DefaultTTL = 24 * time.Hour` 导出常量 |
| `pkg/ctxkeys` | 全库零消费者 — 当前死代码；常量应 unexport，仅暴露 `With*`/`*From` 函数 |
| `cmd/gocell/main_test.go` | 多处 `_ = err` 忽略测试结果 → 至少 assert error 类型 |
| `validate_test.go` | 旧测试（REF-01~09, FMT-01~05）不校验 `Severity`/`IssueType`，新测试已修正 |
| `journey/catalog.go:28` | nil journey entry 未过滤（registry 包有 nil guard，catalog 没有） |
| `generator.go:100` | `GeneratedAt` 用 `time.Now()` 导致 boundary.yaml 非确定性输出 |

---

## 推荐优先级

| 优先级 | 范围 | 项目 |
|--------|------|------|
| **P0 合入前** | 8 项 | B1~B6 + S1~S2 |
| **P1 本迭代** | 10 项 | D1~D7 + D10 + D16~D17（核心设计 + 功能断裂） |
| **P2 下个迭代** | 12 项 | D8~D15 + D18~D19 + nit 精选 |

---

## Review 方法论

本次 review 采用 10 个并行 agent，按角色和模块分工：

**Governance 专项（5 个）**:
1. Correctness — 规则逻辑正确性、边界 case、false positive/negative
2. Architecture — 规则组织、扩展性、依赖合规、IO 合理性
3. Testing — 测试失败修复、覆盖率缺口、edge case
4. Security/Robustness — 路径遍历、nil 指针、竞态、资源增长
5. Go Idioms — 命名、分配效率、复杂度、时间处理

**全库模块（5 个）**:
6. `kernel/cell` — 核心类型 + 接口 + base 实现
7. `kernel/metadata` — YAML 类型 + parser
8. `kernel/assembly + scaffold + registry + journey` — 支撑模块
9. `kernel/slice + outbox + idempotency + cmd/gocell` — 运行时接口 + CLI
10. `pkg/ + internal/meta + 架构横切分析` — 共享包 + 重复代码检测
