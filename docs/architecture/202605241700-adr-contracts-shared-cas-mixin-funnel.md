# ADR: contracts/shared CAS expectedVersion 单源 mixin funnel

- 时间: 2026-05-24
- 状态: Accepted
- 适用范围: `contracts/` 下所有携带 CAS `expectedVersion` 守卫的写接口契约（body schema + DELETE query param）
- ref: ADR `docs/architecture/202605031600-adr-v1-schema-evolution.md`（v1 schema 演化）
- ref: ADR `docs/architecture/202605061500-adr-typed-response-envelope.md`（codegen 合同）
- ref: OpenAPI 3 components 单源 + JSON Schema `$ref`（对标）
- ref: gh #829 CONTRACT-SHARED-MIXIN-FUNNEL-01

## Context

S6 PR 给 6 个 configcore 写接口加 CAS（compare-and-swap）守卫字段
`expectedVersion: {type: integer, minimum: 1, maximum: 99999}`，但把该字段**手抄进 6 处**：

- 4 份 body `request.schema.json`（config update / rollback / flags update / flags toggle）
- 2 份 DELETE `contract.yaml` `endpoints.http.queryParams`（config delete / flags delete，DELETE 无 body）

一致性靠 archtest `CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01` 在 6 份副本间 cross-validate
（AI-robust **Medium**：副本可漂移，靠 CI 比对兜底）。第一性原理（AI-robust 章程）要求把"违反靠
比对识别"升级为"违反不可表达"。

## Decision

把 CAS 字段定义收口到**单一来源** `contracts/shared/cas/v1/expected_version.schema.json`
（standalone JSON Schema 文档，`type: integer / minimum: 1 / maximum: 99999`），6 处全部改为通过
`$ref` 引用它。**一份 mixin，两种载体，两条解析路径，同一真值源**：

### 载体 1 — body schema（JSON Schema `$ref`）

`properties.expectedVersion` 改为 `{"$ref": "<相对路径>/expected_version.schema.json"}`。两条消费路径：

- **DTO 生成**：`tools/codegen/contractgen/jsonschema.go::fillRef` 本就解析跨文件 `$ref`（展平进 DTO struct）。
- **runtime 校验 embed**：`builder.go` embed 路径原先读 raw bytes 不解析 `$ref`，外部 `$ref` 会让
  `schemavalidate.NewValidator`（santhosh-tekuri，`mem:///` base，无 loader）在 codegen 期编译失败。
  **新增 `bundleSchemaRefs`**（`refbundle.go`）：compact 前递归内联 `$ref`、剥离 document-meta
  （`$schema`/`$id`/`title`）、`governance.IsWithinRoot` 路径守护、无 `$ref` 文件原样透传（golden 不变）。
  生成的 embed 自包含、无 `$ref`，runtime 无需文件系统 loader。

### 载体 2 — DELETE query param（contract.yaml `$ref`，解析期回填）

`queryParams.expectedVersion` 改为 `{$ref: "<相对路径>", required: true}`（`required` 是 call-site
语义，留 inline；`$ref` 供 value-shape）。`kernel/metadata` 在**解析期**（`parser.go::parseContract`
→ `paramref.go::resolveParamRefs`）读 mixin 回填 `type/minimum/maximum` 进 `ParamSchema`，下游
（governance FMT-25、contractgen `buildQueryParams`、生成的 DELETE handler 范围检查）**全透明**——
DELETE `handler_gen.go` 重生成字节不变（回填值 == 原 inline 值）。`$ref` 与 inline 约束**互斥**
（解析期 + contract meta-schema `paramSchema` if/then 双重强制）。

### 单源 resolver

`metadata.ResolveParamRef`（注入 IO reader）是 param `$ref` 解析的**唯一语义源**，由 parser（`fs.FS`）
与 contract test harness（`tests/contracttest` fixtureload）共用，避免两套解析逻辑漂移。

### 跨文件 `$ref` 的三个解析者

contract 文件域无 Go 类型系统，跨层不能共享 tools/ 代码，故三处各自解析跨文件 `$ref`（语义一致：
相对当前文件目录、根内守护）：contractgen DTO parse（既有）、contractgen embed bundler（新）、
governance FMT-25 `inlineCrossFileSchemaRefs`（新，否则 `scanSchemaForInputConstraints` 报
"non-local $ref not supported"）。这是分层架构的必要代价，非可消除的重复。

**新增第四个跨文件 `$ref` 解析者的 checklist**（如 fixture generator / scenario test）：

1. root-guard：拒绝绝对路径 `$ref` + `IsWithinRoot`/`fs.ValidPath`/allow-list 守护（三者择一，按所在层的 IO）。
2. 拒绝 `http(s)://` 绝对 URL。
3. cycle guard（visited set，DFS-scoped）。
4. missing-file 错误包成本层的领域错误类型（不要裸 `*os.PathError` 泄漏给上层误判），点名缺失的是 `$ref` target 而非引用者。
5. 同 PR 更新本 ADR。

## AI-robust 评级

- **value 单源 = Hard**：CAS 约束只有 1 份定义；codegen flatten/bundle（body）+ parser 回填（query）
  全派生自它。"6 份副本取不同值"失效模式**不可表达**（只剩 1 份）。属「codegen funnel + golden」范本。
- **codegen embed bundler + golden**：上游 Hard（embed 由 bundler 从 mixin 单源派生，render golden +
  regenerate-diff 锁字节）/ 下游 Hard（generated embed 无 `$ref`，golden byte-pin）。
- **conformance funnel = 闭环 auto-scan archtest（无 enumeration 漏洞）**：`CAS-CONTRACT-EXPECTED-
  VERSION-SCHEMA-01` 删除硬编 target 列表，改为 auto-scan 全 `contracts/`——任一 `expectedVersion`
  carrier 必 `$ref` mixin（上游禁绕过 + auto-enroll；下游禁 inline）。机制层级 = archtest（按章程属
  Medium-tier，因 contract 文件域无 Go 类型系统可依托），但已是该域**天花板**且为**双向闭环**，与既有
  被接受的 `CONTRACT-WIRE-FIELD-CAMELCASE-01` wire-field-name 扫描范式同形。
  - 更高的编译期 Hard（让通用 contractgen 拒收 inline `expectedVersion`）需把通用 codegen 耦合到领域
    字段名 —— **违背优雅简洁，故意不取**（非低成本路径，无需 gh issue 跟踪）。

### Funnel 双向锁

| 方向 | 形态 | 评级 |
|------|------|------|
| 下游（禁 inline） | archtest 断言任一 `expectedVersion` carrier 恰为 `{$ref: canonical}`（body）/ `{$ref, required}`（query），inline 即 CI 红 | Hard（archtest，无灰区） |
| 上游（必经 funnel + auto-enroll） | auto-scan 全 `contracts/`，新契约无需改测试即纳入；mixin 内容经 codegen flatten/bundle + parser 回填强制下沉 | value=Hard；conformance=闭环 archtest |

## Threat Model

| 风险 | 评估 | 缓解 |
|------|------|------|
| 新 CAS 契约 inline `expectedVersion` 绕过单源 | 低 | auto-scan archtest 无 enumeration 漏洞，inline 即 CI 红 |
| 跨文件 `$ref` 路径逃逸读任意文件 | 低 | 三处解析者均 `IsWithinRoot`/`fs.ValidPath`/allow-list 守护 |
| embed bundler 漂移产生与源不一致的 runtime schema | 低 | render golden + `make generate` + `git diff --exit-code generated/` 字节锁 |
| query `$ref` 回填后 FMT-25 漏检 min/max | 低 | FMT-25 `inlineCrossFileSchemaRefs` 前置内联，约束检查见 mixin 实际 type/min/max |
| `$ref` 与 inline 约束并存（作者错误） | 低 | 解析期互斥 error + contract meta-schema if/then 双重强制 |

## Out of scope

- examples/ 下契约无 `expectedVersion`（已 grep 确认），不在扫描误报范围
- DB 层 CAS（`config_entries.version` 乐观锁）由独立 migration 治理，与 wire 契约 mixin 无关
- 其他 wire 字段（pageSize / eventId 等）有各自的 archtest baseline，不纳入本 mixin
- `contracts/shared/` 是 mixin-only 命名空间（跨 kind 共享 schema 片段，如 errors / cas）；CLAUDE.md 的
  `{kind}` 枚举（http / event / command / projection）针对契约本体，`shared` 不是一个 kind，不与之冲突

## Related

- gh #829 CONTRACT-SHARED-MIXIN-FUNNEL-01 — 本 ADR 直接动机
- archtest `CAS-CONTRACT-EXPECTED-VERSION-SCHEMA-01`（`tools/archtest/cas_request_schema_test.go`）
- `.claude/rules/gocell/ai-robust.md` §Hard 范本目录「codegen funnel + golden」/ §Funnel 双向锁评级
