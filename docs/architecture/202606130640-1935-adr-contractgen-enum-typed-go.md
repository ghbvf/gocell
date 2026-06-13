# ADR 1935 — contractgen: JSON-schema `enum` → typed Go enum + 常量

## Status

Accepted (2026-06-13)

## Context

**Issue #1935** (来源 PR #1926 finding F2)。`event.devicecert-rotation-resolved.v1`
的 `outcome` 闭值集（`succeeded|failed|rejected`）三处无机器单源：

- `payload.schema.json` 用 description prose（`"One of: succeeded | failed | rejected"`）
  表达，字段仅 `"type":"string"`，**无 `enum`**；
- Go 本地常量 `outcomeSucceeded/Failed/Rejected`（消费 slice 手维护）；
- consumer switch + `default → DLX`。

生成物 `types_gen.go` 的 `Outcome` 是裸 `string`。根因：`contractgen` 的
`checkUnsupportedKeywords` **显式拒绝 `enum`**（与 `oneOf`/`const` 等并列），所以
被 type-generate 的契约（事件 payload、HTTP req/resp）无法在 schema 落 `enum` 单源——
闭值集只能在 Go 侧重复维护，schema 与 Go 之间无机器约束，drift 全靠人。

全仓 83 个 type-generated 契约扫描后，prose-only 闭值集**恰好 2 个**：本事件
`outcome` 与 `http.orderfulfillment.orderstatus.v1` 的 `data.status`。

## Decision

扩展 `contractgen` 支持 string `enum`，把闭值集坍缩成单一 schema 源：

1. **解析**：`checkUnsupportedKeywords` 移除 `"enum"`；`fillEnum` 把 `enum` 写入
   `Schema.Enum []string`（保留源顺序，稳定 codegen）。

2. **仅 string，非 string fail-fast**：codegen 只为 string 字段生成 typed enum。
   `enum` 出现在非 string 类型（或非数组、空集、非字符串项）→ **解析期报错**，不静默
   丢约束。int/number enum 暂不在范围（无现存需求；扩展时再立）。

3. **命名 `<Parent>+goPascalCase(field)`**（单一 source `enumTypeName`，被
   `schemaGoType` 与 `collectDTOs` 共用，零漂移）：顶层 `Payload.outcome` →
   `type PayloadOutcome string`；嵌套 `ResponseData.status` → `type ResponseDataStatus string`。
   常量 `<TypeName>+goPascalCase(value)`（`PayloadOutcomeSucceeded`）。沿用既有
   嵌套对象 `<Parent><Field>`（`RequestUser`）约定，**零碰撞**：同包不同 DTO 的同名字段
   不会撞类型/常量名。两个值 PascalCase 撞名（如 `in-progress`/`in_progress`）→ fail-fast。

4. **渲染**：`types.tmpl` 在 struct 后按 DTO 的 `Enums` 渲染 `type X string` + `const(...)`；
   字段 GoType 引用具名类型。`gocell generate` 重生成，输出由 `testdata/golden` 字节 golden
   + `gocell verify generated` 冻结。

5. **应用**：device-cert event `outcome` 与 orderfulfillment orderstatus `data.status`
   两处 schema 加 `enum`，重生成，消费侧删本地常量、改引用生成常量。

## Wire-trust 边界（关键，纠正 finding 种子）

finding 种子写「consumer 删 switch 防御（值集由类型保证）」——**对 consumer 侧错误**。

typed enum 是 **produce 侧 + 单源** 保证，**不是 consume 侧 wire 校验**：

- **produce 侧**：`outcomeFromReason` 返回 `PayloadOutcome` 常量，编译期防 typo；
  HTTP response handler 用生成常量构造 `data.status`，类型级闭合。
- **consume 侧**：`p.Outcome` 来自**不可信 wire**，`encoding/json` 解码 **不校验 enum
  成员**——任意 wire 串解码成 `PayloadOutcome("weird")`。故事件 consumer 的
  `switch … default → DLX` **必须保留**作为 wire-trust 纵深防御。本 ADR **不**生成
  `UnmarshalJSON`/`Valid()` wire 校验器（消费侧 DLX 兜底已足）。

「单源」收益 = schema prose + Go 常量 + 消费字面量三处坍缩为一个 schema `enum` 源，
**不**等于绕过 DLX。

## Consequences / AI-robust 评级

- **Hard（codegen funnel + golden 范本）**：schema `enum` 是单源；派生 const 由字节 golden +
  `gocell verify generated` 冻结。删一个 enum 值 → 生成的 const 消失 → 引用它的 producer/
  consumer **编译失败**。比手写常量 + freeze-test（`adapters/mqtt PublishFailureReason`）
  更硬：后者靠 archtest 扫描手写常量，本机制靠类型系统 + golden。
- 上游强度：schema 是唯一 mint 入口（`fillEnum` funnel）；下游强度：const 唯一来源是生成包，
  消费侧引用生成常量，无平行字面量。
- **未纳入（backlog）**：① 强制「prose-only 闭值集必须落 schema enum」的 archtest——通用形态
  从 prose 不可机器判定（Soft），窄 content-scan 形态 Medium 但启发式有误报且迁完后残留实例=0
  属过早；② 非 type-generated 的 prose 闭值集（`policy.updated`、`policy.shared rule`、
  `command.ack codegen:false`）；③ `placeorder.status` 裸 string 字面量（非本模式）。

## 参考

- 对标：go-zero `goctl`（`.api` enum → 常量块），见 `docs/references/framework-comparison.md`。
- 实现：`tools/codegen/contractgen/{jsonschema.go,builder.go,spec.go,templates/types.tmpl}`。
- 测试：`jsonschema_test.go`、`builder_test.go`、`render_test.go`（`synth_enum` golden）。
