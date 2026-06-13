# ADR 2004 — contractgen: 契约 schema → TypeScript 类型 emitter

## Status

Accepted (2026-06-13)

## Context

**Issue #2004**（来源 PR #1975 finding F2）。后端 `http.admin.health.cells.v1`（#1860）
的响应 envelope 与前端 gocell-web BR-001 跨仓契约不兼容。三维根因中的**架构根因**是
跨仓契约**单源缺失**：前端 `packages/observability/src/api/health.ts` 手写 `assertHealthShape`
shape guard 表达期望结构，未消费任何 codegen 派生类型（前端代码已留 TODO「待
`HttpAdminHealthCellsV1Response` codegen 后改用 `@gocell/contracts`」）。后端 schema 与前端
手写守卫之间无机器单源，结构 drift 全靠人。

`contractgen` 在本 PR 前**只发 Go**（`types_gen.go`/`iface_gen.go`/`handler_gen.go` 等，模板
全在 `tools/codegen/contractgen/templates/`），无 TypeScript / JSON-schema 导出路径。issue 文档
的「minimal」方案（前端改用 `@gocell/contracts` 派生类型）的**使能前置**正是一个 TS emitter——
没有它，前端无类型可消费，单源无从谈起。

范围经激进三原则 + AI-HARD 自审收敛为 **A-only**：本 PR 只做 emitter（框架能力），**不**把
「后端富化 BR-001 字段(B)」并入——B 与 A 方向相反（A 立「契约即单源真理、前端适配契约」，B 是
「富化后端贴近前端旧愿望」），且 B 字段当前无消费方（前端采纳是跨仓）。B 外移为独立 backlog。

## Decision

给 `contractgen` 增 TypeScript emitter，从既有 IR（`buildContractSpec` 产出的 `ContractGenSpec`）
单源派生 `.ts`，输出到非 Go 树 `generated-ts/`：

1. **复用 IR、独立 render pass**：emitter 复用 `DTOSpec`/`DTOField`/`EnumSpec`（同一 schema
   funnel，含 #1935 的 enum），只做 TS 渲染——`text/template.Execute` 入 buffer，**绝不**走
   `codegen.Render`（其强制 goimports→gofumpt，会拒绝 `.ts`）。模板自身输出 canonical TS，
   golden 才字节稳定。载体：`tsemit.go` + `templates/{types.ts.tmpl,barrel.ts.tmpl}`。

2. **Go→TS 映射**（用 `DTOField` 结构化字段，非 re-parse GoType）：`string→string`；
   `int64/float64→number`；`bool/*bool→boolean`；对象（`ItemDTO!="" && !IsList`）→ 具名 interface；
   对象数组→`Item[]`；scalar 数组→`scalar[]`；`any/[]any→unknown`；enum 字段→引用 `export type
   X = 'a' | 'b'` union。字段键用 **wire JSON key**（`BareJSONTag`，camelCase），`Required==false`
   → `field?: T`。interface 名与 Go DTO 逐字一致（`Response`/`ResponseData`/`ResponseDataCellsItem`…），
   即前端 `import type { Response } from '…'` 的契约。

3. **输出树 `generated-ts/`**：`PackagePath`（`generated/contracts/{kind}/{path}/{version}`）的
   `generated/`→`generated-ts/` 前缀替换 + `types.ts`。**不放 `generated/`**——后者是独立 Go
   module（`generated/go.mod`、go.work 成员），`.ts` 落入会被 `go build ./...` 收。`generated-ts/`
   无 go.mod、非 go.work 成员，对 Go 工具链不可见。

4. **barrel `generated-ts/index.ts`**：`export * as <alias> from './contracts/.../types';`
   namespaced re-export（alias 由包路径派生，collision-safe，按 contractID 排序）。**从全项目派生**
   （`RenderTSBarrel(root, p)`，非 generate scope）——故 scoped `generate contract <id>` 与
   `--all` 写出**同一** barrel，不会 clobber。这是产物「可消费形态」的单一 import 入口；npm 包化
   （package.json/exports/publish）是独立 packaging 层（backlog）。

5. **接线零 CLI 改动**：emitter 接进 `generateOneContract`（per-contract `types.ts`，磁盘）+
   `Generate`（full-project barrel）+ `RenderContractArtifacts`（per-contract `types.ts`，
   in-memory manifest）。`gocell generate contract --all/--verify` 自动覆盖 TS。`responseProjection`
   契约（`data` 重写为 sealed `projection.ResourceProjection`，无固定 wire shape）+ webhook/grpc
   由 `kindEmitsTS` 跳过。

## 真 Hard 双向闭合（关键，本 ADR 的核心技术点）

把 `.ts` 纳为 codegen funnel 必须**双向**闭合，缺一侧即 Soft 逃生门（`ai-robust.md` 禁止）：

- **reverse-enum 侧**：`kernel/governance.ListGeneratedInHEAD` 的 `git grep` globs 加 `*.ts`
  （pattern `^GoGeneratedPrefix` 已匹配 `//`-prefixed TS 头，无需新 const；其 godoc 明文要求
  「any future generator producing a different extension must extend this filter」——本 PR 正是
  履行该约定）。`verify generated` 由此发现committed `.ts`。
- **forward-manifest 侧**：`generatedverify.ExpectedArtifacts` 经 `RenderContractArtifacts`
  发 per-contract `types.ts` + 经 `RenderTSBarrel` 发 `index.ts` 聚合 artifact。

`verify generated` 取二者**交集**校验：committed `.ts` ∈ expected manifest **且** expected ∈
committed。只补 reverse-enum（emitter 初版的盲区）会让 `.ts` 全被判 `not in expected manifest`。
两侧 + `*_types_ts.golden`/`barrel_index_ts.golden` 字节 golden + `generate --verify` 逐文件
diff = **三重冻结**。手改 `generated-ts/**.ts`：撞 golden（producer test）或 `verify generated`
（反枚举 ∩ manifest）或 `--verify`（drift）。

barrel 单源（`RenderTSBarrel`）被磁盘 `Generate` 与 manifest `generatedverify` **共用**，两侧
byte-identical——否则 manifest 与磁盘对不上，CI 永红。

## Wire-trust 边界

TS `interface` 是**消费端编译期形状提示**，**非运行时 wire 校验**（镜像 #1935 enum 的消费侧
caveat）：

- gocell-web 用生成 interface 做静态类型检查（删 `assertHealthShape` 手写守卫），编译期捕获
  字段错配；
- 但 TS interface 在运行时被擦除，**不**校验真实响应 body。**服务端**用契约嵌入的 JSON-schema
  在 HTTP ingress 校验**入站请求** body（handler 既有机制，不变）。注意：JSON-schema ingress
  校验面向**入站请求** body——对于 GET /health 这类无请求 body 的端点不适用，亦不校验出站响应。
  前端删除 `assertHealthShape` 后，**出站响应**的形状保障依赖 golden + `generate --verify` 防
  后端 schema 漂移；TS 类型仅是消费端编译期提示，运行时不提供校验。TS emitter 不生成运行时
  validator，也不改任何 wire 语义——它只为现有契约**派生类型视图**，是纯加性框架能力。

## Consequences / AI-robust 评级

- **Hard（codegen funnel + golden 范本）**：schema 是唯一 mint（复用 `fillEnum`/`buildContractSpec`
  funnel）；TS 字节由 golden + `ListGeneratedInHEAD` 反枚举 + `generate --verify` 三重冻结。
  上游强度：唯一 mint 入口是 schema；下游强度：`generated-ts/**` 唯一来源是 emitter，无平行手写
  类型（前端删 `assertHealthShape` 后，跨仓亦无平行源）。
- **载体守卫**：archtest `CONTRACTGEN-TS-EMIT-FUNNEL-01`（`tools/archtest/contractgen_ts_emit_funnel_test.go`，
  Medium 广度腿）断言唯一 TS 模板在 contractgen、`renderTS`/`renderBarrel` 不经 `codegen.Render`、
  所有 TS 路径在 `generated-ts/` 下——防第二个 TS emitter 旁路 funnel。
- **scaffold 集成**：`gocell scaffold cell` 流程经同一 generate 路径，新 cell 的契约自动产 `types.ts`
  （`scaffold_golden` 已纳入）。
- **未纳入（backlog）**：① `@gocell/contracts` npm 打包/发布（package.json/tsconfig/publish；
  `generated-ts/`+barrel 已产但未成 registry 包）；② gocell-web 采纳生成 `.ts`、删
  `assertHealthShape`（跨仓 `ghbvf/gocell-web` 独立 PR；本 PR 仅解锁）；③ 运维 health 富化（原 B：
  health-cells additive 补 type/durability/sliceCount/lifecycle+summary，以字段独立运维价值评判，
  非 BR-001 对齐）；④ responseProjection 契约的 TS 发射（本 PR 跳过——掩码读端点 wire shape 动态）；
  ⑤ health `status` 等剩余 prose 闭值集迁 schema `enum`（#1935 ADR 已列 orderfulfillment 一项）。

## 参考

- 对标：OpenAPI / protobuf 跨语言 codegen（schema 单源 → 多语言类型派生），见
  `docs/references/framework-comparison.md`；同形前例 #1935（schema enum → typed Go）。
- 实现：`tools/codegen/contractgen/{tsemit.go,generator.go,templates/types.ts.tmpl,templates/barrel.ts.tmpl}`、
  `tools/generatedverify/generatedverify.go`、`kernel/governance/genheader.go`。
- 测试：`tsemit_test.go`、`render_test.go`（`*_types_ts`/`barrel` golden）、
  `tools/archtest/contractgen_ts_emit_funnel_test.go`（CONTRACTGEN-TS-EMIT-FUNNEL-01）、
  `kernel/governance/genheader_test.go`（`.ts` 反枚举）。
