# PR #403 修复路线图（funnel 化优先 / 最终态）

**对接**: `docs/reviews/202605070153-pr403-third-wave-review.md` §4 修复方案
**关系**: 替代原报告 §4 路线，§1-§3 症状/根因诊断保留
**生成日期**: 2026-05-07
**最后更新**: 2026-05-07（核实段 1 实际状态后修正）

---

## 0. 路线变更摘要（最终态）

原 §4 提出 4 段修复路线。逐项核实实际代码后修正：

| 段 | 原计划 | 最终决策 | 状态 |
|---|---|---|---|
| 段 1 | typed envelope 闭环（双向 + 四件套配齐） | 已在 PR #403 内闭环 | ✅ 已完成 |
| 段 2 | Registry 派生物治理产品化（ADR + 注册表 + CLI + archtest） | **删除** | ✗ 反模式 |
| 段 3 | PR 切片纪律 / four-piece kit ADR / PR template 改造 | **删除** | ✗ 项目不稳定期噪音 |
| 段 3' | 顺手在 CLAUDE.md 加一段 funnel-first 原则（< 10 行） | 顺手做 | ⏳ 待做 |
| 段 4 | 历史 invariant 审计 → 入 Registry | **重定向**：archtest funnel 化降数量（30+ → < 15） | ⏳ 待做 |

变更动因：
- 段 1：14 个症状 + 链尾 archtest 在 PR #403 内已逐项落地（核查见 §3）
- 段 2：主流路线（K8s / CockroachDB / Linux / Rust / Go 工具链）无 prior art，反模式（详见 §2）
- 段 3：项目当前处于大规模重构期，invariant 增删频繁，PR template + ADR 会变成应付式填写而非治理；真正起作用的是段 4 的 funnel 化代码本身
- 段 4：原"反推 invariant 入 Registry"目标错位，重定向为"funnel 化降数量"

---

## 1. 根因（修正后）

原报告 R0 诊断："GoCell 派生物治理无产品化通道"——这是解法形状，不是根因。

### L1 症状

一条 invariant 散在 8-12 处（archtest / governance rule / godoc / ADR / journey YAML / 模板 / IR / 测试），加一条要在 8-12 处同时改，会漏会脱钩。

### L2 直接因

约束没收敛到单一 funnel 节点，被迫在每个出口手抄。archtest 必须守 N 处而非 1 处；godoc / ADR 也要在 N 处呼应。**四件套是散布态的下游补救**，不是治理本体。

### L3 根因

**新增约束的默认动作是"加一条 archtest 守散点"，而不是"重构出 funnel 让散点不可达"**。Go 缺类型系统级表达力（不像 Rust 用 trait + ownership 让非法状态不可表达），开发者顺手用 archtest 当类型系统替代品，但 archtest 守散点 ≠ funnel 守入口。

**问题不是"如何治理多源"，是"为什么会有多源"**。

---

## 2. 主流路线对照

### 三种路线，没有一条是 Registry

| 类型 | 代表 | 机制 | GoCell 适用 |
|---|---|---|---|
| **Funnel + Codegen** | K8s kubebuilder marker / Envoy protoc-gen-validate / Buf protovalidate | 单源 schema/marker → codegen 派生多端执行 → 改一处全部派生物自动重生 | ✅ typed envelope `XxxResponseObject` 走这条 |
| **Type System** | Rust trait + ownership / Linux `__user` annotation + sparse / Haskell GADT | 约束消失为类型签名一部分，违反 = 编译失败 | ❌ Go 不支持（无 newtype / sealed interface / private constructor 强制） |
| **自家 linter（兜底）** | CockroachDB `pkg/testutils/lint/` ~30 / TiDB / Go 工具链 `cmd/api` / Linux sparse+checkpatch+coccinelle | archtest 平铺，每条 lint 一个独立文件，命名约定 + CI 调度 | ✅ GoCell 当前模式（archtest + governance） |

### Registry 模式无 prior art

- **K8s** 30+ `hack/verify-*.sh`，各自独立 shell 脚本，**无中心 Registry**
- **CockroachDB** 30+ linter，各自独立 Go 文件，**无中心 Registry**
- **Linux** 三套独立工具（sparse / checkpatch / coccinelle），**无中心 Registry**
- **Go 标准库** `cmd/api` golden 文件 + `cmd/dist test` 各自独立，**无中心 Registry**

**没有任何主流项目把"代码中已存在的约束"再写进注册表去守完整性**。原段 2 是 GoCell 自创设计，反模式。

### funnel 化的物理天花板

GoCell ~50% 约束物理上 funnel 不掉：

| 约束类型 | funnel 可能性 | 原因 |
|---|---|---|
| HTTP 出口 PII strip | ✅ 强制走 `httputil.WriteError` | 已 funnel（archtest 守"必经"） |
| typed adapter return | ✅ codegen `XxxResponseObject` interface | type system 拦，最干净 |
| buffer-then-commit 顺序 | ❌ funnel 不到 | "语句顺序"无汇聚点，必须 AST 模式守 |
| message 必须 const literal | ❌ funnel 不到 | Go 编译器不区分 const string / runtime string |
| 不暴露 errors.New | ❌ funnel 不到 | Go 无 sealed package |
| panic 三类白名单 | ❌ funnel 不到 | Go 编译器不知道 panic 语义 |
| readyz probe snake_case 命名 | ❌ funnel 不到 | 字符串 literal，编译器不感知语义 |

**做不到 100% funnel 是 Go 的语言天花板，不是 GoCell 的失败**。CockroachDB 同样吃这个亏，他们的解法是接受 + 平铺管理。

---

## 3. 段 1 状态核查（已完成证据）

逐项 grep 实际代码，14 个症状 + 链尾 archtest 全部已在 PR #403 内闭环：

| 症状 | 状态 | 证据 |
|---|---|---|
| S1 sentinel 内联化 | ✅ | `pkg/httputil/response.go:323` 调 `writeInternalErrorSentinel(w)` |
| S2 AppendCorrelationAttrs 注册 | ✅ | `kernel/governance/rules_http_response_alignment.go:730` |
| S3 idutil godoc 删 X-Request-Id | ✅ | `pkg/idutil/id.go` 不再举 X-Request-Id 名 |
| S4 redaction LogValuer 测试 | ✅ | `pkg/redaction/redaction_test.go` `customLogValuer / secretLeakingLogValuer` |
| S5 encodeErrorEnvelopeTo FailingWriter | ✅ | `pkg/httputil/response_test.go:759` `TestEncodeErrorEnvelopeTo_FailingWriter` |
| S6 死代码删除 | ✅ | `response_test.go:591-599` 已为 marshalFailErrcodeWrapper 注释 |
| S7 builder_test success-only 反例 | ✅ | `tools/codegen/contractgen/builder_test.go:1023` `"success-only no responses"` |
| S8 archtest 排除注释 | ✅ | `tools/archtest/visit_buffer_then_commit_test.go:82-83` 写明 NoContent/Error 排除理由 |
| S9 journey mode auto→manual | ✅ | `journeys/J-typed-envelope-roundtrip.yaml` buffer-then-commit 行 `mode: manual` |
| S10 generator_test.go | ✅ | `tools/codegen/contractgen/generator_test.go` 存在 |
| S11 doc 错位修复 | ✅ | `docs/guides/codegen-new-endpoint.md` `responses:` 在 `endpoints.http` 下 |
| S12 C18 success-only 收紧 | ✅ | `builder.go:380` 加 `hasError` 检查，无 4xx/5xx 报错 |
| S13 synth_http_minimal 含 400/500 | ✅ | `testdata/synth/synth_http_minimal/.../contract.yaml` |
| S14 45 真实 contract.yaml 扫补 | ✅ | `grep -L "responses:" contracts/http/**/contract.yaml` 输出空 |
| 链尾 archtest ADAPTER-RETURNS-DECLARED-TYPES-01 | ✅ | `tools/archtest/adapter_returns_declared_types_test.go` 存在 |
| ADR D6/D7 升级 | ✅ | `docs/architecture/202605061500-adr-typed-response-envelope.md` |

**段 1 完成，无遗留。**

---

## 4. ~~段 2（删除）~~

原计划：建 `kernel/governance/invariants.go` Registry + `INVARIANT-REGISTRY-COMPLETENESS-01` archtest + `gocell check invariants` CLI + four-piece kit ADR。

**删除理由**：

1. **无 prior art**：K8s / CockroachDB / Linux / Go 工具链 / Rust 都不建 Registry
2. **加 4 个新源头**（ADR + 注册表 + 守 archtest + CLI），把"散布态"工业化，不是解决散布
3. **维护负担**：archtest 改名 → Registry 字段引用失效 → 守 Registry 完整性的 archtest 本身又成第 N+1 条 invariant，递归
4. **解决错问题**：真问题是"散布"，Registry 解的是"中心索引"

由 §6 段 4 的 funnel 化代码 + CLAUDE.md 一段原则替代。

---

## 5. ~~段 3（删除）~~

原计划：`.claude/rules/gocell/pr-slicing.md`（一个 PR ≤ 2 个新 invariant / L3 切片纪律）+ funnel-first ADR + PR template 增 invariant 字段。

**删除理由**：

1. **项目不稳定期噪音**：当前处于大规模重构期，invariant 增删频繁，PR template 字段会变成应付式填写
2. **ADR 过度形式化**：funnel-first 是判断框架（< 10 行能讲清），不是需要长文档承载的架构决策
3. **硬规则基于猜测**："PR ≤ 2 个 invariant" 没有数据支撑，等段 4 funnel 化数据出来再决定是否值得加

由段 3' CLAUDE.md 一段补丁替代。等项目稳定（typed envelope / contract.yaml schema 不再大改）后再评估是否升级 ADR。

---

## 6. 段 3'（CLAUDE.md funnel-first 原则补丁，Cx1）

`CLAUDE.md` 新增一节（< 10 行）：

```markdown
## 新增 invariant 决策原则

新增任何"约束"（archtest / governance rule / godoc 强约定）前，按以下优先级决策载体：

1. **funnel + codegen**：能否 schema / marker / interface 单源 → codegen 派生执行体？能 → 走这条
2. **type system 自然拦**：能否用 Go interface / typed struct / 编译期 const 让违反不可表达？能 → 走这条
3. **archtest 平铺兜底**：上面两条都不行 → 一个独立 `tools/archtest/{ID}_test.go` 文件，文件头 godoc 写约束 ID + 理由 + 不能 funnel 的原因

**不准建 Registry / 中心化注册表**。多份文档用 grep 锚点串联（`// INVARIANT: {ID}` 在所有相关文件出现，grep 一次跳全套）。

主流对照：K8s / CockroachDB / Linux / Rust 都接受 funnel 不到的残留，平铺管理；不建中心索引。
```

无新代码、无新工具、无新 archtest。

---

## 7. 段 4（archtest funnel 化降数量，独立 PR 系列，Cx3）

### 7.1 目标

archtest 总数从 30+ 降到 < 15，把可 codegen 化 / type system 化的全消掉，剩余的接受为永久残留。

### 7.2 审计步骤

#### 步骤 A：清单化（0.5 天）

`scripts/audit/list-archtests.sh`（30 行 shell）扫 `tools/archtest/*_test.go` + `kernel/governance/rules_*.go`，吐出每条 invariant 的：

- ID（从文件头 godoc 抽，缺则补）
- 守的对象（AST 模式 / 调用点 / 字段集合）
- funnel 节点（如果有）

输出 `docs/audit/archtest-inventory.md`，平铺表格，**不写 Go 注册表**。

#### 步骤 B：funnel 化分类（1 天）

对每条 invariant 判断：

| 类别 | 处置 | 示例 |
|---|---|---|
| **可 codegen 化** | 用 contract.yaml / cell.yaml schema 约束 + codegen 替代 archtest | endpoints.http.responses 声明 → CH-06（已做） |
| **可 type system 化** | 改接口签名让违反不可表达 | typed `XxxResponseObject` 替代 `(*Response, error)`（已做） |
| **可 funnel + 单守门** | 已收敛到 funnel 函数，archtest 只守"必经" | `httputil.WriteError` PII strip |
| **不可 funnel（保留）** | 平铺 archtest，加锚点注释 | message const literal、buffer-then-commit |
| **冗余/重复** | 直接删除 | 多条 archtest 守同一约束的不同侧面 |

#### 步骤 C：funnel 化 PR 系列（按类别分批，主线登记于本路线图）

本路线图 §C 是 PR-FUNNEL-NN 主线工作的唯一 todo 登记位置。复审产生的跨批次 follow-up 以 `docs/backlog.md` 为单源，本节只保留路由索引，避免 roadmap/backlog 双写漂移。`docs/audit/archtest-inventory.md` §不在本 PR 范围 引用本节编号，但不重复内容。

##### PR-FUNNEL-01（本 PR #408，已 ship）

**HTTP-ARCHTEST-CLUSTER-MERGE-01** (Cx2, 已落地)：同主题聚并 + INVARIANT 锚点 + funnel-first CLAUDE.md 原则 + 路线图/inventory 落地（45 文件 → 11 主题文件 + 11 锚点修补 + 1 fail-closed 覆盖 gate `TestSpanRecordErrorScanDirsCoverage`，archtest 文件数 104 → 70）。

##### PR-FUNNEL-02（PR #411 已 ship — 模板侧 4 条；调用侧 1 条保留为 archtest）

**HTTP-RESPONSE-CONSTRAINTS-CODEGEN-MIGRATION-01** (Cx2，✅ 已 ship)：

实际执行结果（与原计划差异）：

- **5 条 archtest 拆为 2 类**：探索发现 `ParamSchema.{MinLength,MaxLength,Minimum,Maximum}` 已在 `kernel/metadata/schema_types.go` 存在；`handler.tmpl` 已 inline 发 len/value 检查、硬编 schema-compile panic、按 auth 模式条件发 policy 参数。Funnel 早已 100% 落地这 4 条模板侧约束，archtest 是 redundant 散点守卫。
- **模板侧 4 条 archtest 删除**：HANDLER-NO-INLINE-LIMIT-PARSE-01 / HANDLER-NO-SCHEMA-FOR-NOBODY-01 / HANDLER-PATH-QUERY-LENGTH-VALIDATION-01 / HANDLER-VALIDATOR-FAIL-FAST-01 由 `tools/codegen/contractgen/testdata/golden/*_handler_gen_go.golden` 字节级锁定取代。新增 `synth_http_auth_modes` fixture 覆盖 Public / Bootstrap / PasswordResetExempt / ClientsOnly / ServiceOwned 五个 auth 分支（之前只覆盖默认 Auth 分支）。
- **调用侧 1 条 archtest 升级为 funnel + 平铺兜底两级**：HANDLER-POLICY-REQUIRED-01 升级实施 F1 review fix：funnel 端通过 `auth.clientsOnly` 与 `auth.serviceOwned` 单参分支（新 `AuthClientsOnly` / `AuthServiceOwned` spec flag + handler.tmpl 分支）+ Default 分支构造期 `if policy == nil { panic(errcode.Assertion(...)) }` 把 route-level nil-policy 的主要产生源消灭；archtest scanner 退化为简化兜底，删除 `handlerPolicyPublicExemptPkgs` 豁免列表 + 删除 alias 字符串匹配，无需 typed-nil 形态分叉（typed-nil 由构造期 panic 在启动时炸出）。保留为 `tools/archtest/handler_policy_required_test.go`（独立文件）。
- **治理侧 follow-up 已收敛**：`auth.clientsOnly` / `auth.serviceOwned` 互斥归入 FMT-27；`auth.bootstrap` 与 `auth.clientsOnly` 的跨字段 shape 约束归入 FMT-28，builder 保留防御性 fail-closed。
- **不做的事**：不引入 invariant Registry；不把 HANDLER-POLICY-REQUIRED-01 升级为 type-aware scanner；不在本 PR 定义 serviceOwned ownership enforcement 静态钩子；不在本 PR 收 schema/governance 显式 `false` 语义一致性（3 项均已登记 backlog，见下方 follow-up 路由）。

**关键文件**：`kernel/metadata/schema_types.go` + `kernel/governance/rules_fmt.go` + `tools/codegen/contractgen/{builder.go,spec.go,templates/handler.tmpl}` + `tools/codegen/contractgen/testdata/synth/synth_http_auth_modes/`（新 fixture）+ `tools/codegen/contractgen/testdata/golden/synth_http_auth_modes_*` 15 个 auth-mode golden + `tools/archtest/handler_policy_required_test.go`（新独立文件）+ `tools/archtest/handler_invariants_test.go`（删 1316 行）+ `tools/archtest/doc.go` / `docs/architecture/202605061500-adr-typed-response-envelope.md` §D5 / `docs/audit/archtest-inventory.md`（去 dangling 引用）。

**实际工时**：~3h dev + 6 角色 L4 review（~14 finding 全部本 PR 修复）。原计划 16-24h 估算偏高，因 funnel 早已就位，实际工作集中在 fixture 补全 + 文档去 dangling。

**性质**：funnel-first 原则的二分裁决落地（"模板可表达 → funnel + freeze；模板看不到 → 保留 archtest"）。

**来源**：本路线图 §不在范围 + R6 architecture finding (PR#408) → PR #411 实际执行。

##### PR-FUNNEL follow-up 路由（backlog 单源）

| Backlog ID | 批次 | 处理策略 |
|---|---|---|
| `PR408-FU-LEGACY-ANCHOR-BACKFILL-01` | 下一批 P1 小 PR | 先补 legacy `// INVARIANT:` 锚点，再删除 inventory fallback；不和 PR411 auth follow-up 混做。 |
| `PR408-FU-GOVERNANCE-OWNER-AST-EXTRACTION-01` | 下一批 P1 小 PR | 与 anchor backfill 同批，修 `list-archtests.sh` owner 提取精度并回灌 inventory。 |
| `PR411-AUTH-SCHEMA-GOVERNANCE-BOOL-SEMANTICS-01` | PR411 后续 Cx2 小 PR | 独立处理 schema/governance 对显式 `false` 的语义一致性，优先级高于 serviceOwned 泛化护栏。 |
| `PR411-SERVICEOWNED-OWNERSHIP-GUARD-01` | ownership hardening 批次 | 延后到 serviceOwned endpoint 增多或 auth ownership 模型硬化时做；先定义 enforcement 契约再加规则。 |
| `PR411-HANDLER-POLICY-TYPEAWARE-SCANNER-01` | scanner 精度批次 | 延后；最好等 shared scanner framework 后再升级为 packages/type-aware。 |
| `PR408-FU-SCANNER-SHARED-FRAMEWORK-01` | 独立 Cx4 大 PR | 不并入小修；先做框架，再渐进迁移现有 scanner。 |

##### PR-FUNNEL-03（待办，~12-16h dev + 4h review）

**GOVERNANCE-RULES-CLUSTER-MERGE-01** (Cx2, 🟡 可延后)：`kernel/governance/rules_*.go` 60+ 条规则分散在 15 个文件，按主题聚并到 ~6 个文件，机械搬迁（类似 PR-FUNNEL-01 archtest 处理）。

**主题分组**：
- `rules_fmt.go`（FMT-01..30）
- `rules_ref.go`（REF-01..17）
- `rules_topo.go`（TOPO-01..09）
- `rules_verify.go`（VERIFY-01..06）
- `rules_http.go`（CH-04/05/06 + http_pathparam_uuid + http_response_alignment + http_typed_envelope）
- `rules_misc.go`（ADV-* / OUTGUARD-01 / DOC-NAME-01 / WRAPPER-* / SLICE-* / CONSISTENCY-* / strict / strict_extra）

**关键文件**：`kernel/governance/rules_*.go` (15→~6) + 配对 `*_test.go`。

**性质**：规则数量不变，影响 `gocell validate` 全流程，需独立测试覆盖（每个目标文件独立 _test.go 验证迁移前后规则集合等价）。

**来源**：本路线图 §不在范围 + R6 architecture finding (PR#408)。

##### PR-FUNNEL-04（按需触发，无固定工时）

**ARCHTEST-REDUNDANCY-AND-TYPE-SYSTEM-MIGRATION-01** (Cx 视情况)：冗余删除 / type system 化（改接口签名让违反不可表达）。

**触发条件**：PR-FUNNEL-02 + PR-FUNNEL-03 完成后，仍发现 ≥ 3 条可通过修改接口签名消除的 archtest，启动本 PR；否则保留为长期残留。

**性质**：每条迁移独立小 PR，按案例评估。无固定 todo 列表。

#### 步骤 D：固化（0.5 天）

- 更新 `docs/audit/archtest-inventory.md` 为最终状态
- archtest 数量验收：≤ 15（基线对标 CockroachDB ~30，GoCell 规模可压更低）

### 7.3 验收标准

- archtest 总数 ≤ 15
- 每条保留 archtest 文件头 godoc 含 INVARIANT/Funnel/References 锚点注释
- `scripts/audit/list-archtests.sh` 输出可重现
- 无 Registry / 无 CLI / 无 four-piece kit 中心化

---

## 8. 时间盘

| 段 | 估时 | 顺序 | 状态 |
|---|---|---|---|
| 段 1 | — | — | ✅ PR #403 已 ship |
| ~~段 2~~ | — | — | ✗ 删除 |
| ~~段 3~~ | — | — | ✗ 删除 |
| 段 3' | < 10 分钟 | 顺手做 | ⏳ |
| 段 4 | 4-5 天（A: 0.5 / B: 1 / C: 2-3 / D: 0.5） | 立即可启动 | ⏳ |

总剩余 4-5 天，比原计划（6-8 天）省 2-3 天，且省掉 Registry + CLI + ADR 的长期维护负担。

---

## 9. 取舍记录

### 9.1 为什么不建 Registry

- 主流项目无 prior art（K8s / CockroachDB / Linux / Go 工具链 / Rust 都不建）
- 加 4 个新源头给"散布态"工业化，不是解决散布
- 维护负担：守 Registry 完整性的 archtest 自身又是 invariant，递归
- grep 锚点 + CLAUDE.md 一段原则已覆盖收益

### 9.2 为什么删段 3 PR template / ADR

- 项目当前处于大规模重构期，invariant 增删频繁，PR template 字段会变应付式填写
- funnel-first 原则 < 10 行能讲清，不需要 ADR 长文档承载
- 硬规则（PR ≤ 2 个 invariant）基于猜测，等段 4 funnel 化数据出来再判断

### 9.3 为什么允许 < 15 条 archtest 残留

Go 语言天花板（无 sealed package / 无 newtype / 无 const string 区分）决定 ~50% GoCell 约束物理上 funnel 不掉。CockroachDB 同样残留 30 条。GoCell 规模比 CockroachDB 小，目标 < 15 是数据量级的合理估计。

### 9.4 为什么段 4 立即启动而非排到 roadmap 末尾

原计划"段 4 依赖段 2 工具"是错位——段 4 自带 30 行 shell 脚本审计，无任何前置依赖。段 1 已 ship，段 4 可立即启动，不必等。

---

## 10. 结论

**PR #403 已 ship，剩余工作只剩段 3' + 段 4。**

- 段 1：✅ 14 个症状 + 链尾 archtest + ADR D6/D7 全部在 PR #403 内闭环
- 段 2：✗ Registry 反模式，删除（无 prior art）
- 段 3：✗ PR template / ADR 不稳定期噪音，删除
- 段 3'：⏳ CLAUDE.md 加一段 funnel-first 原则（< 10 行），顺手做
- 段 4：⏳ archtest funnel 化降数量（30+ → < 15），独立 PR 系列，立即可启动

**下一步**：
1. 段 3'：直接 patch CLAUDE.md，10 分钟
2. 段 4：单开一个 worktree，先做步骤 A 清单化，跑出 inventory 后再分批 PR
