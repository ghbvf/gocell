# ADR: contractgen 不生成 errcode constants — 推到 C5 治理

> Status: Superseded by PR#368 Kind-based errcode cleanup
> Date: 2026-05-04
> ref: `docs/plans/202605011500-029-master-roadmap.md` Lane K #06（PR-V1-CONTRACT-DTO-CODEGEN）+ Track C #C5（PR-V1-KERNEL-IFACE-EXPLICIT）
> Implementation: PR-V1-CONTRACT-DTO-CODEGEN（feat/166-codegen-contract-gen）

## Context

K#06 PR-2（CONTRACT-DTO-CODEGEN）的 roadmap row 71 文字描述目标产出为：

> generated/contracts/<id>/{types,iface,errors}.go

实施时发现 `errors.go` 的"contract-scoped errcode constants 生成"会与 K#06 PR-2 范围发生张力。本 ADR 记录当时决定不生成 errcode constants 的取舍。PR#368 之后 HTTP status 已由 `errcode.Kind` 单源驱动，本 ADR 中关于旧中央状态表的前提已不再成立；typed response envelope 后续项登记在 029 roadmap `06.FU`。

### errcode 现状

当时 `pkg/errcode` 同时维护手写 `errcode.Code` constants 和中央 code→HTTP status 表。新增 contract 时作者要同时加 const declaration 和状态映射 entry，漂移由 review 抓，无静态守卫。

PR#368 后该模型已删除：`errcode.New/Wrap` 必须显式传入 `Kind`，`httputil.WriteError` 只从 `Kind` 派生 HTTP status。

### 三个备选方案与各自代价

**方案 A**：PR-2 仅生成 errors.go 常量，不动中央状态表
- contractgen 输出 `Err<Domain><Reason> errcode.Code = "ERR_<DOMAIN>_<REASON>"`
- 但状态表没补 → 运行时 fallback 500
- **回归现有 4xx 行为**（`responses.400` 在 contract.yaml 声明，但 generated const 触发 500）
- 这是引入回退，不是消除漂移

**方案 B**：PR-2 同时改中央状态表为 reflect 自动构建
- 需要在 `pkg/errcode` 加 `RegisterStatus(code, status)` API + 每个 generated 包 init() 副作用注册，**或** 整改为运行时 reflect 扫所有 generated 子包
- init-side-effect 注册是 anti-pattern（与 PROD-CLOCK-INJECTION-01 治理方向相反）
- reflect 自动构建是 C5（PR-V1-KERNEL-IFACE-EXPLICIT）的明确范围："治理表 reflect 自动构建从 pkg/errcode 单源映射"
- **PR-2 同步做 C5 = 范围吃掉独立 PR**，违反 029 roadmap 拆分原则

**方案 C**（采纳）：PR-2 不生成 errcode constants，留给 C5 + 后续 PR
- types_gen.go / iface_gen.go / handler_gen.go 三件保留
- generated handler 直接透传 service error 给当时的 canonical HTTP writer — 沿用现有 errcode → status 映射行为
- contract.yaml 的 `responses.<code>` 仅起文档/contracttest 边界校验作用，不参与 codegen
- C5 ship 后再补 errors_gen.go：届时状态映射机制已自动化，新生成的 contract-scoped const 自动接入，无需 init-side-effect

## Decision

K#06 PR-2 收口为：

```
generated/contracts/<kind>/<domain-path>/v<N>/
  types_gen.go    — Request / Response / Payload / Headers struct
  iface_gen.go    — Service (HTTP) / Handler (event) interface
  handler_gen.go  — HTTP adapter (kind=http only)
```

**不出 errors_gen.go**。errcode 治理留给 C5 PR-V1-KERNEL-IFACE-EXPLICIT。

## Consequences

**Positive**：
- PR-2 范围聚焦 DTO + iface + handler，不引入运行时回退（fallback 500）
- C5 处理状态映射治理时，contractgen 已经存在的 generated 子包不需要重新设计 init-side-effect 桥接
- handler 行为与当时手写 handler 完全一致，迁移面更窄

**Negative**：
- roadmap row 71 文字偏离（"errors.go 不生成"）— 由本 ADR 显式记账
- 添加 contract 仍需手补 `pkg/errcode` 常量 + 状态映射 entry，PR-4（4 cell + 全量 contract 一次到位）期间该手工成本继续存在
- C5 ship 后需要后续 PR 补 errors_gen.go（约 4-6h dev，比 PR-2 同步做更便宜，因为状态映射自动机制已就位）

**Follow-up**（C5 + PR-V1-CONTRACTGEN-ERRORS）：
1. C5 ship → 状态映射从 pkg/errcode 单源自动构建
2. 后续 PR：扩 contract.yaml schema 增 `errors:` 字段（contract 显式列出可能返回的 errcode + status + description）
3. contractgen 增 errors.tmpl + render → errors_gen.go 输出 contract-scoped const + 扁平 errStatusMap
4. C5 reflect 机制自动收集所有 generated errStatusMap，无需 init-side-effect

## References

- roadmap row 71 (K#06): `docs/plans/202605011500-029-master-roadmap.md` line 71
- roadmap C5: `docs/plans/202605011500-029-master-roadmap.md` line 128
- 现有 errcode: `pkg/errcode/errcode.go`、`pkg/errcode/status.go`
- 现有 handler 错误响应: `pkg/httputil/response.go::WriteError`
