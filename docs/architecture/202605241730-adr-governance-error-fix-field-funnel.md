# ADR: governance ValidationResult — typed Fix field + 构造函数 funnel（INV-3 Soft→Hard）

- 日期：2026-05-24
- 状态：Accepted
- 关联：issue #689（本变更）、issue #922（上游 Hard 化跟踪）、archtest `GOVERNANCE-RULE-ERROR-FIX-FIELD-01`（前身 `GOVERNANCE-RULE-ERROR-MESSAGE-FIX-SUFFIX-01`）、`GOVERNANCE-RULE-CODE-CONST-SINGLE-SOURCE-01`、`kernel/governance/emitter_invariant.go`

## Context

`kernel/governance` 每条规则产出 `ValidationResult`。错误类 finding 历史上把「修复指导」用 `"; fix: …"` 子串拼进 `Message`，由 archtest INV-3（`GOVERNANCE-RULE-ERROR-MESSAGE-FIX-SUFFIX-01`）扫描 `Message` 是否含该子串来强制「每条 error 给 remediation」。

这是 AI-robust **Soft** 形态（字符串锚点，见 `.claude/rules/gocell/ai-robust.md`）：i18n / 模板化会让锚点漂移，子串扫描误判风险高，且「修复指导」与「问题陈述」挤在同一无结构字段里。该 enforcement 此前仅活在 archtest godoc，无 ADR。

## Decision

把 fix 指导从 `Message` 子串提升为 `ValidationResult.Fix` **typed 字段**，并用**类型化构造函数 funnel** 收口构造（AI-robust「typed function choice」范本）：

| 构造函数 | 位置 | severity | fix |
|---------|------|---------|-----|
| `newError(code, typ, file, field, msg, fix)` | `*locator` 方法 | Error | 必填 |
| `newWarning(code, typ, file, field, msg)` | `*locator` 方法 | Warning | 无 |
| `newScopedError(code, typ, scope, field, msg, fix)` | `*locator` 方法 | Error（跨文件） | 必填 |
| `newErrorAt(code, typ, file, pos, field, msg, fix)` | **包级函数** | Error（content-scan 位置） | 必填 |

- 选 error 语义 = 调用结构上强制带 `fix` 参数的 API（arity）；选错 = 选错 API 名。
- `newErrorAt` 是包级函数（非 locator 方法）：它不查 yaml.Node 缓存（位置由调用方给），使包级 emit/doc scan helper（无 locator receiver）也能经 funnel 构造，而非裸 `ValidationResult{}` 字面量。
- 旧 free-`Severity` 三件套 `newResult`/`newResultAt`/`newScopedResult` **删除**，无兼容别名（项目无外部消费方）。
- 输出：text printer 渲染独立 `fix:` 行，JSON 加 `"fix"` 字段，SARIF 在 message 尾部追加 `; fix: …`（SARIF 无独立 remediation slot）。Message 不再含 `"; fix:"`。
- 迁移 ~203 个构造站点 + 5 个裸字面量；`advisory_hints.go` 的 30 个 `advHint*` const 拆成「问题 const + `*Fix` const」。

## AI-robust 评级（Funnel 双向锁，诚实）

| 方向 | 约束 | 机制 | 评级 |
|------|------|------|------|
| 下游 | error 构造必须带 fix 参数 | 构造函数签名，少传参编译失败 | **Hard**（编译期） |
| 下游 | fix 实参非空 | archtest `GOVERNANCE-RULE-ERROR-FIX-FIELD-01` 解析 fix 实参（`resolveStringFragments`：literal / const ident / `+` concat / `fmt.Sprintf` template）非空 | **Medium**（Go 无法类型化「非空字符串」；「typed marker funnel for unbounded ops」可达上限。比旧 Message 子串扫描强：专用参数位 vs 自由文本锚点，与 INV-2 的 Code-const 解析同范本） |
| 上游 | 不能绕过构造函数直接 `ValidationResult{}` 字面量 | archtest 同规则 CompositeLit ban（governance 包内、locator.go 外） | **Medium**（所有 rule 都在 `package governance`，Go 包内可见性无法编译期阻挡同包字面量；unexport 字段对包内零收益且逼全部消费方加 accessor） |

**上游 Hard 化**（把 `ValidationResult` 连 unexported 字段 seal 进 `kernel/governance/result` 子包 + 私有构造 + accessor，使包内字面量构造跨包不可表达）= 独立的 result-type 封装 invariant family（实测 ~700 个字段读取点需改 accessor，是本变更 ~203 构造站点的 3 倍，且与 fix 指导正交）。**不在本变更**，由 **issue #922** 跟踪，archtest godoc 点名该 issue。

该「Medium 上游 + Hard 下游 + gh issue」是 `.claude/rules/gocell/ai-robust.md` §Funnel 双向锁明文许可的过渡形态，对齐既有终态先例 observability `SPAN-SETATTR-REDACT-01` / #851（package-internal Medium + gh issue）。立项门槛 ≥ Medium 满足——本变更**删除**了旧 Soft（Message 子串扫描），新立项机制全部 ≥ Medium，无 Soft。

### Enforcement 通道（哪条检查在哪跑，不夸大）

- **下游 Hard（fix 参数 arity）= 编译期，PR-gated**：`newError`/`newErrorAt`/`newScopedError` 的 `fix` 是必填位置参数，少传即 `go build` 失败——PR CI 的 `build-test` lane 直接拦截。这是 PR-time 强制的。
- **fix 非空 + 裸字面量 ban（两条 archtest，Medium）= nightly + 本地 `make verify`**：`GOVERNANCE-RULE-ERROR-FIX-FIELD-01` 与全部 governance INV-1/INV-2 一样，**不在** PR-time `hack/verify-archtest-invariants.sh` 白名单（该白名单按 CPU/RSS 预算只含 clock/duration/time-literal/panic 四类核心）；由 `archtest-nightly.yml` + 开发者本地 `make verify` 兜底。这是 governance invariant 的既有 posture，非本规则特例；本 ADR 不单独把 INV-3 提到 PR-time（要提须连同 INV-1/2 一起评预算，属另一决策）。

## 威胁模型（INV-3 此前无 ADR，故为首次成文，非 amendment 重评）

| 威胁 | 覆盖 |
|------|------|
| error finding 无修复指导 | fix 参数 arity（Hard）+ fix 非空 archtest（Medium） |
| 用裸字面量绕过 funnel | CompositeLit ban（Medium 上游，#922 跟踪升 Hard） |
| code 用非 const（漂移） | INV-2 `...CODE-CONST-SINGLE-SOURCE-01`（不变，构造函数名同步更新） |
| 新增非 locator receiver 冒充 emitter | `emitter_invariant.go` 受体 type-identity gate（Hard）；包级 emitter 仅 `newErrorAt`，signature-gated（无 name anchor） |
| 构造函数经 value/func 变量间接调用绕过扫描 | governance 无此间接；archtest godoc 列盲区 + production scan 绿 + 负向 fixture 用直调反向自检 |
| `fmt.Sprintf("%s", fixParam)` 模板无字面内容绕过非空检查 | `fixHasLiteralContent` 剥离 printf verb 后须有字面文本，纯 verb 模板判空 |
| **导出类型在 governance 外被构造（跨包）** | **见下「跨包构造缺口」——本变更补 Fix 值，archtest 守护由 #922 seal 统一** |

### 跨包构造缺口（C1，已知，#922 跟踪）

`governance.ValidationResult` 是**导出类型**，除 governance rule pipeline 外，`cmd/gocell/app/check.go` 也直接构造它（~17 处 `SeverityError` 裸字面量，check 命令的 contract-health / slice-coverage 诊断）。构造函数 `newError` 等是 unexported `*locator` 方法，cmd 无法调用，故 cmd 只能用裸字面量。

- **本变更已补**：check.go 这 ~17 处的 `Fix` 值（关闭 `gocell check --format=json` 输出 `"fix":""` 的可见契约洞；Fix 文本无损带入 #922）。
- **archtest 守护 defer #922**：INV-3 的 production scan 与 funnel ban 只覆盖 `./kernel/governance`；让 cmd 构造点也受守护的正确做法是 #922 的 seal——届时 `ValidationResult` 字段 unexported、构造函数导出为唯一路径，cmd 跟随改走构造函数（强制 Fix）。本变更**不**建临时的「cmd 字面量必须有 Fix」cross-package archtest，因 #922 seal 会整体推翻它（避免 throwaway 机制）。#922 body 已含此构造点迁移范围。

## Consequences

- 正：fix 指导结构化，UX 更清晰（独立 `fix:` 行 / JSON 字段）；Soft 锚点消除；构造收口便于未来 #922 升 Hard。
- 负：上游仍 Medium（#922 跟踪）；`newErrorAt` 是包级函数，与三个 locator 方法形态不一（已在 godoc + `emitter_invariant.go` 说明：因其不依赖 yaml 缓存，供无 receiver 的 scan helper 使用）；governance 外的 `ValidationResult` 构造（cmd/gocell/app）Fix 值已补但**无 archtest 守护**，待 #922 seal 统一（见上「跨包构造缺口」）。
- 中性：CLI text/JSON/SARIF 输出形态变化（项目无外部消费方，无兼容负担）。
