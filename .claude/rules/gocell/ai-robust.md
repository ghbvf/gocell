# AI-robust 治理章程

GoCell 的治理机制默认面向 AI co-author。新增约束必须让错误尽量不可表达，
至少做到机器可判定；纯口头约定不是可接受的新增 enforcement。

## 适用范围

本章程适用于新增或修改下列机制：

- archtest
- `gocell validate` / `gocell check` governance rule
- bootstrap 启动期 fail-fast guard
- codegen funnel 与 golden
- sealed type、typed marker、reflect field freeze
- 带 `INVARIANT:` 的 package godoc 约束

不适用于普通业务开发、常规 lint/test/build，也不把 bug 修复本身包装成新治理机制。

## 分级

| 级别 | 定义 | 典型载体 |
|------|------|----------|
| Hard | 违反不可表达或改动必触发编译/golden/field freeze | codegen、type system、sealed construction、reflect freeze |
| Medium | 违反可表达，但 CI 中由 type-aware scan 或 runtime guard 抓住 | archtest typed scan、governance rule、bootstrap guard |
| Soft | 依赖人记住、注释、命名习惯或手工清单 | 禁止作为新增机制 |

新机制最低门槛是 Medium。若当前只能做到 Soft，必须换载体或缩小目标。

## 载体选择

优先级：

1. schema / marker / tag 单源派生代码，再用 golden 锁字节输出。
2. Go type system 表达约束，例如 sealed type、私有字段、typed constructor。
3. archtest typed scan，使用公开 façade：`Run`、`Typed`、`Production`、`Fixture`。
4. metadata / YAML / Markdown 规则使用 `EachContentFile`，并配 synthetic red case。
5. runtime guard 只用于 type system 不可表达的边界，错误必须 fail-fast。

禁止直接在规则文件中维护落地实例清单。实例、符号、盲区和评级证明写在对应
archtest package godoc、ADR 或代码注释中。

## Hard 范本

- typed function choice：不同语义拆成不同 API。
- typed marker funnel：`any` 或字符串入口必须经单一 marker constructor。
- string-typed concept funnel：独立语义字符串使用专有类型和值集。
- input struct field exclusion：公开输入类型不暴露不该由业务传入的字段。
- single sanctioned holder：只允许一个结构持有 raw infra 字段。
- sealed construction：包外不可结构字面量构造或伪造语义值。
- reflect schema freeze：wire/schema struct 字段集、tag、类型身份精确冻结。
- codegen funnel + golden：声明源派生执行体，输出 drift 由字节 diff 暴露。

新机制若不属于这些范本，先写 ADR 说明为何需要扩展范本。

## archtest 文件命名

- 单条独立规则：`{rule}_test.go`
- 同主题三条及以上：`{theme}_invariants_test.go`
- 文件头必须列 `INVARIANT: <ID>`
- 内容扫描规则必须有 synthetic red case 和 anti-vacuity

CI、本地触发方式和 shard 策略见 `hack/README.md`；规则文件不复制执行细节。

## 审查要求

涉及 enforcement 的 finding 必须给出 Hard / Medium / Soft 评级。

- Hard：保留，符号和证明写入对应代码 godoc。
- Medium：保留；若有低成本 Hard 化路径，登记 GitHub Issue。
- Soft：新增时 reject；既有 Soft 修改优先升级到 Medium 或 Hard。

Funnel 类约束必须分别说明上游和下游强度。只锁 callsite 不是闭环 funnel。

ADR amendment 落地时，必须同步重评原 ADR 的威胁矩阵或安全模型；冲突段落在同一改动中重写。
